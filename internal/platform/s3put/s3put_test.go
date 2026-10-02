package s3put

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// AWS's own worked example, "Example: PUT Object", from the Signature V4
// documentation. The signature below is theirs, not ours: this is the check
// that the signer is correct rather than merely self-consistent.
func TestTheSignerMatchesAWSsPublishedPutObjectExample(t *testing.T) {
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	payload := sha256Hex([]byte("Welcome to Amazon S3."))
	headers := map[string]string{
		"date":                 "Fri, 24 May 2013 00:00:00 GMT",
		"host":                 "examplebucket.s3.amazonaws.com",
		"x-amz-content-sha256": payload,
		"x-amz-date":           "20130524T000000Z",
		"x-amz-storage-class":  "REDUCED_REDUNDANCY",
	}
	got := Authorization("AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"us-east-1", "PUT", "/test%24file.text", "", headers, payload, at)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
		" SignedHeaders=date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class," +
		" Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"
	if got != want {
		t.Errorf("authorization =\n%s\nwant\n%s", got, want)
	}
	if escapePath("test$file.text") != "test%24file.text" {
		t.Errorf("escapePath = %q", escapePath("test$file.text"))
	}
}

var good = Destination{Bucket: "acme-exports", Region: "ap-south-1", Prefix: "relay/reports",
	AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}

func TestPutSendsASignedRequestToTheRightObject(t *testing.T) {
	var path, auth, hash string
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth, hash = r.URL.EscapedPath(), r.Header.Get("Authorization"), r.Header.Get("x-amz-content-sha256")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer server.Close()
	c := Client{Endpoint: server.URL, Now: func() time.Time { return time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC) }}
	if err := c.Put(context.Background(), good, "daily/2026-10-02 report.csv", "text/csv", []byte("a,b\n1,2\n")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if path != "/acme-exports/relay/reports/daily/2026-10-02%20report.csv" {
		t.Errorf("path = %q", path)
	}
	if string(body) != "a,b\n1,2\n" || hash != sha256Hex(body) {
		t.Errorf("body/hash wrong: %q %s", body, hash)
	}
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20261002/ap-south-1/s3/aws4_request") {
		t.Errorf("authorization = %s", auth)
	}
}

func TestAnS3RefusalSurfacesItsCodeNotItsMarkup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>AccessDenied</Code><Message>nope</Message></Error>`))
	}))
	defer server.Close()
	err := Client{Endpoint: server.URL}.Put(context.Background(), good, "x.csv", "text/csv", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "AccessDenied") ||
		strings.Contains(err.Error(), "<Error>") {
		t.Errorf("err = %v", err)
	}
}

func TestADestinationThatCouldNotBeS3IsRefused(t *testing.T) {
	for name, mutate := range map[string]func(*Destination){
		"upper-case bucket": func(d *Destination) { d.Bucket = "Acme" },
		"bucket with slash": func(d *Destination) { d.Bucket = "a/b" },
		"bucket dots":       func(d *Destination) { d.Bucket = "a..b" },
		"short bucket":      func(d *Destination) { d.Bucket = "ab" },
		"host as region":    func(d *Destination) { d.Region = "evil.example.com" },
		"empty region":      func(d *Destination) { d.Region = "" },
		"bad key id":        func(d *Destination) { d.AccessKeyID = "short" },
		"bad secret":        func(d *Destination) { d.SecretAccessKey = "tiny" },
		"prefix traversal":  func(d *Destination) { d.Prefix = "a/../b" },
		"prefix slash":      func(d *Destination) { d.Prefix = "/abs" },
		"prefix newline":    func(d *Destination) { d.Prefix = "a\nb" },
	} {
		d := good
		mutate(&d)
		if d.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
		if err := (Client{}).Put(context.Background(), d, "x", "text/plain", nil); err == nil {
			t.Errorf("%s was uploaded", name)
		}
	}
	if err := good.Validate(); err != nil {
		t.Errorf("a good destination was refused: %v", err)
	}
}
