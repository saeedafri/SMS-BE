// Package s3put uploads one object to S3 with a hand-written Signature V4
// request. The module carries no AWS SDK and this needs only a single PUT, so
// pulling in the SDK's dependency tree for it would be the larger risk. The
// signer is checked against AWS's own published example in the test.
package s3put

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionName = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
	accessKey  = regexp.MustCompile(`^[A-Z0-9]{16,128}$`)
)

// Destination is where a customer's exports go.
type Destination struct {
	Bucket          string
	Region          string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
}

// Validate refuses a destination that could not be a real S3 bucket, and
// anything that would send a signed request somewhere else.
func (d Destination) Validate() error {
	switch {
	case !bucketName.MatchString(d.Bucket) || strings.Contains(d.Bucket, ".."):
		return fmt.Errorf("bucket must be a valid S3 bucket name (3-63 lowercase letters, digits, dots or hyphens)")
	case !regionName.MatchString(d.Region):
		return fmt.Errorf("region must look like ap-south-1")
	case !accessKey.MatchString(d.AccessKeyID):
		return fmt.Errorf("access key id must be 16-128 capital letters and digits")
	case len(d.SecretAccessKey) < 20 || len(d.SecretAccessKey) > 128 || strings.ContainsAny(d.SecretAccessKey, " \r\n\t"):
		return fmt.Errorf("secret access key looks wrong")
	case len(d.Prefix) > 200 || strings.Contains(d.Prefix, "..") || strings.HasPrefix(d.Prefix, "/") ||
		strings.ContainsAny(d.Prefix, "\r\n\x00\\"):
		return fmt.Errorf("prefix must be a folder path without '..', a leading slash or control characters")
	}
	return nil
}

// Client uploads objects. Endpoint is empty in production, which means the
// bucket's own virtual-hosted address; a test points it at a local server.
type Client struct {
	HTTP     *http.Client
	Endpoint string
	Now      func() time.Time
}

func (c Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// Put writes body at prefix/key.
func (c Client) Put(ctx context.Context, dest Destination, key, contentType string, body []byte) error {
	if err := dest.Validate(); err != nil {
		return err
	}
	objectKey := strings.TrimSuffix(dest.Prefix, "/")
	if objectKey != "" {
		objectKey += "/"
	}
	objectKey += strings.TrimLeft(key, "/")

	host := dest.Bucket + ".s3." + dest.Region + ".amazonaws.com"
	base := "https://" + host
	if c.Endpoint != "" {
		base = strings.TrimRight(c.Endpoint, "/") + "/" + dest.Bucket
		parsed, err := url.Parse(base)
		if err != nil {
			return err
		}
		host = parsed.Host
	}
	path := "/" + escapePath(objectKey)
	if c.Endpoint != "" {
		path = "/" + dest.Bucket + path
		base = strings.TrimRight(c.Endpoint, "/")
	}
	payloadHash := sha256Hex(body)
	at := c.now()
	headers := map[string]string{
		"host":                 host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           at.Format("20060102T150405Z"),
	}
	authorization := Authorization(dest.AccessKeyID, dest.SecretAccessKey, dest.Region, "PUT",
		path, "", headers, payloadHash, at)

	request, err := http.NewRequestWithContext(ctx, http.MethodPut, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("x-amz-content-sha256", payloadHash)
	request.Header.Set("x-amz-date", headers["x-amz-date"])
	request.Header.Set("Authorization", authorization)

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("s3: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 == 2 {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(response.Body, 600))
	return fmt.Errorf("s3 answered %d: %s", response.StatusCode, summarise(string(detail)))
}

// summarise pulls the error code out of S3's XML so the message a customer sees
// is "AccessDenied", not a wall of markup.
func summarise(xml string) string {
	if start := strings.Index(xml, "<Code>"); start >= 0 {
		if end := strings.Index(xml[start:], "</Code>"); end > 0 {
			return xml[start+6 : start+end]
		}
	}
	if len(xml) > 120 {
		xml = xml[:120]
	}
	return strings.TrimSpace(xml)
}

// Authorization computes the Signature V4 header value. headers are the ones to
// sign, keyed lower-case.
func Authorization(accessKeyID, secret, region, method, path, query string,
	headers map[string]string, payloadHash string, at time.Time) string {

	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + strings.TrimSpace(headers[name]) + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{method, path, query, canonicalHeaders.String(), signed, payloadHash}, "\n")

	scope := at.Format("20060102") + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + at.Format("20060102T150405Z") + "\n" + scope + "\n" + sha256Hex([]byte(canonical))

	key := hmacSHA256([]byte("AWS4"+secret), at.Format("20060102"))
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))
	return "AWS4-HMAC-SHA256 Credential=" + accessKeyID + "/" + scope +
		", SignedHeaders=" + signed + ", Signature=" + signature
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// escapePath percent-encodes each path segment the way S3 canonicalises it:
// everything except unreserved characters, with '/' kept as the separator.
func escapePath(key string) string {
	var out strings.Builder
	for _, b := range []byte(key) {
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9',
			b == '-', b == '_', b == '.', b == '~', b == '/':
			out.WriteByte(b)
		default:
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}
