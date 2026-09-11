package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	// Registered for their side effect: image.DecodeConfig can only read a
	// format whose decoder has been linked in.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/domain/media"
	gen "github.com/saeedafri/sms-be/internal/gen/api"
	"github.com/saeedafri/sms-be/internal/platform/mediastore"
	"github.com/saeedafri/sms-be/internal/store"
)

// mediaReadTTL is how long a signed URL stays good.
//
// Long enough that a carrier fetching artwork during a review does not find it
// expired, short enough that a leaked URL is not a permanent hole. Read the
// asset again rather than storing the URL — that is what makes the expiry work
// rather than merely exist.
const mediaReadTTL = 24 * time.Hour

// maxUploadBytes bounds what is read off the wire before any per-purpose rule
// applies.
//
// A cap here as well as in the rules, because the rules cannot run until the
// bytes are in hand: without this, a purpose whose limit is 50 KB would still
// read a gigabyte into memory before deciding it was too large.
const maxUploadBytes = 12 * 1024 * 1024

// UploadMedia accepts one file and returns the asset it became.
//
// THE FILE IS DECODED, NOT DESCRIBED. Both the dimensions and the content type
// are read off the bytes rather than taken from the request, and that is the
// whole point of the handler: a client-supplied dimension is a claim, not a
// measurement. A caller that posts a 4000-pixel photograph while declaring
// 224x224 passes any check written against what it said, and the failure lands
// at the carrier days later, on an agent submission nobody can see the reason
// for.
func (s *Server) UploadMedia(ctx context.Context, request gen.UploadMediaRequestObject) (
	gen.UploadMediaResponseObject, error) {

	identity, ok := identityFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if !s.Media.Configured() {
		return nil, errors.New("api: media storage is not configured")
	}

	part, err := request.Body.NextPart()
	var purpose media.Purpose
	var body []byte
	var filename string
	for err == nil {
		switch part.FormName() {
		case "purpose":
			raw, readErr := io.ReadAll(io.LimitReader(part, 128))
			if readErr != nil {
				return nil, readErr
			}
			purpose = media.Purpose(raw)
		case "file":
			filename = part.FileName()
			// One byte past the cap, so "exactly at the limit" and "over it"
			// are distinguishable rather than both looking full.
			body, err = io.ReadAll(io.LimitReader(part, maxUploadBytes+1))
			if err != nil {
				return nil, err
			}
		}
		part, err = request.Body.NextPart()
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return gen.UploadMedia422JSONResponse(errorBody(codeValidation,
			"That upload could not be read as multipart form data.")), nil
	}

	rule, known := media.RuleFor(purpose)
	if !known {
		return gen.UploadMedia422JSONResponse(errorBody(codeValidation,
			fmt.Sprintf("%q is not a purpose this API accepts.", string(purpose)))), nil
	}
	if len(body) == 0 {
		return gen.UploadMedia422JSONResponse(errorBody(codeValidation,
			"No file was included in the upload.")), nil
	}
	if int64(len(body)) > maxUploadBytes {
		return gen.UploadMedia413JSONResponse(errorBody(codeValidation,
			fmt.Sprintf("That file is larger than the %d MB this API will read.",
				maxUploadBytes/(1024*1024)))), nil
	}

	// Sniffed, not declared. A Content-Type on a multipart part is supplied by
	// the client exactly as the dimensions were, so trusting it would leave the
	// same hole one field over: a caller could store an executable by calling
	// it image/png.
	contentType := sniffContentType(body)
	if err := rule.CheckType(contentType); err != nil {
		return gen.UploadMedia422JSONResponse(errorBody(codeValidation,
			capitalise(err.Error())+".")), nil
	}
	if err := rule.CheckSize(contentType, int64(len(body))); err != nil {
		// 413 rather than 422: the request was well formed and simply too big,
		// and the frontend's error states are written against that distinction.
		return gen.UploadMedia413JSONResponse(errorBody(codeValidation,
			capitalise(err.Error())+".")), nil
	}

	asset := store.MediaAsset{
		TenantID:    identity.TenantID,
		Purpose:     string(purpose),
		ContentType: contentType,
		ByteSize:    int64(len(body)),
	}
	// Dimensions come from DecodeConfig, which reads the header rather than the
	// pixels — so a 200 KB banner costs a header parse, not a decode.
	if config, _, decodeErr := image.DecodeConfig(bytesReaderOf(body)); decodeErr == nil {
		width, height := config.Width, config.Height
		asset.Width, asset.Height = &width, &height
		if err := rule.CheckDimensions(width, height); err != nil {
			return gen.UploadMedia422JSONResponse(errorBody(codeValidation,
				capitalise(err.Error())+".")), nil
		}
	} else if rule.Width != 0 {
		// A purpose with an exact size demands an image we can actually
		// measure. Storing an undecodable file here would defer the refusal to
		// the carrier, which is the failure this handler exists to prevent.
		return gen.UploadMedia422JSONResponse(errorBody(codeValidation,
			fmt.Sprintf("That file could not be read as an image, and this purpose "+
				"needs one exactly %d x %d.", rule.Width, rule.Height))), nil
	}

	created, err := store.CreateMediaAsset(ctx, s.DB, identity, asset,
		func(key string) error {
			_, writeErr := s.Media.Put(key, bytesReaderOf(body))
			return writeErr
		})
	if err != nil {
		return nil, err
	}

	return gen.UploadMedia201JSONResponse(s.mediaAsset(created, filename)), nil
}

// mediaAsset renders a stored asset with a freshly signed URL.
func (s *Server) mediaAsset(asset store.MediaAsset, filename string) gen.MediaAsset {
	if filename == "" {
		filename = "asset"
	}
	return gen.MediaAsset{
		Id:          asset.ID,
		Url:         s.Media.SignedURL(asset.TenantID, asset.ID, safeFilename(filename), mediaReadTTL),
		ContentType: asset.ContentType,
		ByteSize:    int(asset.ByteSize),
		Width:       asset.Width,
		Height:      asset.Height,
		UploadedAt:  asset.CreatedAt,
	}
}

// mountMediaRoutes serves an asset back to whoever holds a valid signature.
//
// Mounted directly rather than generated: the contract declares the upload and
// documents the URL as opaque, deliberately, so that the read is ours to shape
// and a client cannot come to depend on its query string.
//
// NO SESSION IS REQUIRED, and that is the point of signing. A carrier fetching
// brand artwork has no Relay login; the signature is the authorisation, it
// carries the tenant inside it so it cannot be replayed against another
// prefix, and it expires.
func (s *Server) mountMediaRoutes(r chi.Router) {
	r.Get("/v1/media/{id}/{filename}", func(w http.ResponseWriter, req *http.Request) {
		assetID, err := uuid.Parse(chi.URLParam(req, "id"))
		if err != nil {
			writeError(w, http.StatusNotFound, codeNotFound, "No such asset.")
			return
		}
		expires, err := mediastore.ParseExpires(req.URL.Query().Get("expires"))
		if err != nil {
			writeError(w, http.StatusForbidden, "forbidden", "That link is not valid.")
			return
		}
		signature := req.URL.Query().Get("signature")

		// The tenant is read from the row and then CHECKED by the signature,
		// rather than taken from the URL. A URL that carried it would let a
		// caller ask for another tenant's prefix and disclose which customer an
		// asset belongs to even when the signature failed.
		asset, err := store.FindMediaAsset(ctx(req), s.AdminDB, assetID)
		if err != nil {
			writeError(w, http.StatusNotFound, codeNotFound, "No such asset.")
			return
		}
		key, err := s.Media.Verify(asset.TenantID, assetID, expires, signature)
		if err != nil {
			writeError(w, http.StatusForbidden, "forbidden",
				"That link has expired or is not valid. Read the asset again for a fresh one.")
			return
		}
		file, err := s.Media.Open(key)
		if err != nil {
			writeError(w, http.StatusNotFound, codeNotFound, "No such asset.")
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", asset.ContentType)
		w.Header().Set("Cache-Control", "private, max-age=300")
		_, _ = io.Copy(w, file)
	})
}

func ctx(req *http.Request) context.Context { return req.Context() }

// sniffContentType reads the type off the bytes.
//
// http.DetectContentType implements the WHATWG sniffing algorithm against the
// first 512 bytes, which is where every format this API accepts carries its
// magic number. It answers application/octet-stream when it recognises
// nothing, which the per-purpose rule then refuses by name.
func sniffContentType(body []byte) string {
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	detected := http.DetectContentType(head)
	// The sniffer returns parameters on some types ("text/plain; charset=..."),
	// and the rules are written against the bare type.
	if semicolon := strings.IndexByte(detected, ';'); semicolon >= 0 {
		detected = detected[:semicolon]
	}
	return detected
}

func bytesReaderOf(body []byte) *bytes.Reader { return bytes.NewReader(body) }

// safeFilename keeps the customer's name in the URL for a readable download
// while making sure it cannot climb out of the path or smuggle a query.
//
// The filename is decoration: the signature covers the id and the expiry, not
// this, so it is never trusted to locate anything.
func safeFilename(name string) string {
	name = filepath.Base(name)
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		}
		return '-'
	}, name)
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "asset"
	}
	if len(cleaned) > 80 {
		cleaned = cleaned[:80]
	}
	return cleaned
}
