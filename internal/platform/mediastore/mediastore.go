// Package mediastore holds uploaded files and hands out signed, expiring URLs
// for them.
//
// Filesystem-backed rather than S3, deliberately and for now. Every requirement
// the frontend stated — signed, expiring, tenant-prefixed, never a public read,
// an https URL a carrier can fetch — is satisfied without adding an
// object-storage dependency or provisioning a bucket that does not exist. The
// trade is that the bytes live on one machine's disk; moving to S3 later is a
// swap behind Put/Open, not a change to the signing or the API.
//
// ponytail: single-node storage, swap Store for an S3 driver if a second API
// node ever exists.
package mediastore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrBadSignature covers every reason a URL is not honoured — wrong signature,
// expired, malformed. One error on purpose: telling a caller WHICH is wrong
// helps an attacker more than it helps a client, and a client's remedy is the
// same in every case, which is to re-read the asset.
var ErrBadSignature = errors.New("mediastore: signature invalid or expired")

// Store writes files under a root directory and signs URLs into them.
type Store struct {
	root   string
	secret []byte
	// baseURL is the public origin a carrier will fetch from. It has to be the
	// externally reachable one: a URL only resolvable inside the VPC is not a
	// URL a carrier can fetch, and the failure lands days later at the gateway.
	baseURL string
}

func New(root string, secret []byte, baseURL string) *Store {
	return &Store{root: root, secret: secret, baseURL: strings.TrimRight(baseURL, "/")}
}

// Configured reports whether uploads can be served at all, which is a
// deployment choice rather than an incident and is worth telling apart.
func (s *Store) Configured() bool {
	return s != nil && s.root != "" && len(s.secret) > 0
}

// Key is where an asset's bytes live, relative to the root.
//
// TENANT-PREFIXED so isolation is enforceable at the path and not only in the
// query that produced the URL. A bug in one handler cannot then read across
// tenants by id alone.
func Key(tenantID, assetID uuid.UUID) string {
	return filepath.Join(tenantID.String(), assetID.String())
}

// Put writes the bytes and returns how many were written.
func (s *Store) Put(key string, body io.Reader) (int64, error) {
	if !s.Configured() {
		return 0, errors.New("mediastore: no root configured")
	}
	path := filepath.Join(s.root, filepath.Clean("/"+key))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, fmt.Errorf("mediastore: create directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("mediastore: create file: %w", err)
	}
	defer file.Close()
	written, err := io.Copy(file, body)
	if err != nil {
		return 0, fmt.Errorf("mediastore: write file: %w", err)
	}
	return written, nil
}

// Open reads an asset back.
func (s *Store) Open(key string) (io.ReadCloser, error) {
	path := filepath.Join(s.root, filepath.Clean("/"+key))
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("mediastore: open file: %w", err)
	}
	return file, nil
}

// Remove deletes an asset's bytes. Used by retention, which is why it tolerates
// a file that is already gone: a retention sweep that fails on its second run
// over the same row is a sweep that stops running.
func (s *Store) Remove(key string) error {
	err := os.Remove(filepath.Join(s.root, filepath.Clean("/"+key)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("mediastore: remove file: %w", err)
	}
	return nil
}

// SignedURL is where an asset is served from, for a bounded window.
//
// Signed and expiring rather than a public path, because these are brand assets
// and business identity documents: a readable bucket makes them enumerable by
// anyone who guesses a path, and a verification document is a company's
// incorporation paperwork.
//
// The URL is opaque and must be re-read rather than stored — the expiry is the
// whole point, and a client that caches it holds a URL that stops working.
func (s *Store) SignedURL(tenantID, assetID uuid.UUID, filename string, ttl time.Duration) string {
	expires := time.Now().Add(ttl).Unix()
	return fmt.Sprintf("%s/v1/media/%s/%s?expires=%d&signature=%s",
		s.baseURL, assetID, filename, expires, s.sign(tenantID, assetID, expires))
}

// Verify checks a presented signature and returns the storage key it authorises.
//
// The tenant id is INSIDE the signature rather than in the URL. It is what the
// key is built from, so a signature cannot be replayed against another tenant's
// prefix, and the URL does not have to disclose which customer an asset belongs
// to.
func (s *Store) Verify(tenantID, assetID uuid.UUID, expires int64, signature string) (string, error) {
	if time.Now().Unix() > expires {
		return "", ErrBadSignature
	}
	// Constant time, so a caller cannot learn the signature a byte at a time by
	// measuring how long the comparison takes.
	if !hmac.Equal([]byte(signature), []byte(s.sign(tenantID, assetID, expires))) {
		return "", ErrBadSignature
	}
	return Key(tenantID, assetID), nil
}

func (s *Store) sign(tenantID, assetID uuid.UUID, expires int64) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(tenantID[:])
	mac.Write(assetID[:])
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], uint64(expires))
	mac.Write(stamp[:])
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ParseExpires reads the expiry off a query string.
func ParseExpires(raw string) (int64, error) {
	return strconv.ParseInt(raw, 10, 64)
}
