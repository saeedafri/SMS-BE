package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Request body ceilings.
//
// Nothing capped a request body: a 10 MB JSON document sent to POST
// /v1/messages was read in full and decoded before the handler found the
// field it wanted missing. A hostile caller could hold a worker, and the
// memory behind it, for as long as they cared to keep uploading.
//
// One megabyte is far more than any ordinary JSON body in the contract.
// Contact import carries every row in the body, so it gets its own, larger
// ceiling; media upload is multipart and enforces its own limit while it
// reads the file.
const (
	jsonBodyLimit   = 1 << 20
	importBodyLimit = 32 << 20
)

func bodyLimitFor(r *http.Request) int64 {
	if r.URL.Path == "/v1/contacts/import" {
		return importBodyLimit
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		return maxUploadBytes + 1<<20
	}
	return jsonBodyLimit
}

// limitBody refuses an oversized body twice over: up front from the declared
// Content-Length, so a client announcing 10 MB is turned away without reading a
// byte, and again while reading, for a client that sends chunked or lies.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := bodyLimitFor(r)
		if r.ContentLength > limit {
			writeTooLarge(w, limit)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

func writeTooLarge(w http.ResponseWriter, limit int64) {
	writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large",
		"That request body is larger than "+humanBytes(limit)+" allows.")
}

func humanBytes(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d KB", n>>10)
}

// isBodyTooLarge reports whether a decode failed because limitBody cut the
// stream, so the error handler can answer 413 instead of a vague decode error.
func isBodyTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}
