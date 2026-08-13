package auth

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"image"
	"net/url"
	"strings"
	"time"

	"github.com/boombuler/barcode/qr"
	"github.com/pquerna/otp/totp"
)

const (
	totpIssuer        = "Relay"
	recoveryCodeCount = 10
	recoveryCodeBytes = 5
)

// NewTOTPSecret mints a base32 secret and the otpauth:// URI an authenticator
// app scans.
func NewTOTPSecret(accountName string) (secret, otpauthURI string, err error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("auth: read totp secret: %w", err)
	}
	secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)

	uri := url.URL{
		Scheme: "otpauth",
		Host:   "totp",
		Path:   "/" + totpIssuer + ":" + accountName,
	}
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", totpIssuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", "6")
	query.Set("period", "30")
	uri.RawQuery = query.Encode()

	return secret, uri.String(), nil
}

// VerifyTOTP checks a code against a secret, allowing one period of clock skew
// in each direction. Without that tolerance, a phone whose clock is a few
// seconds out locks the user out of their own account.
func VerifyTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if secret == "" || code == "" {
		return false
	}
	valid, err := totp.ValidateCustom(code, secret, time.Now().UTC(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: 6,
	})
	return err == nil && valid
}

// NewRecoveryCodes returns codes to show the user once, and their hashes to
// store. Like session tokens they are high-entropy, so SHA-256 is the right
// digest — there is no weak secret to stretch.
func NewRecoveryCodes() (codes []string, hashes [][]byte, err error) {
	codes = make([]string, 0, recoveryCodeCount)
	hashes = make([][]byte, 0, recoveryCodeCount)
	for range recoveryCodeCount {
		buf := make([]byte, recoveryCodeBytes)
		if _, err := rand.Read(buf); err != nil {
			return nil, nil, fmt.Errorf("auth: read recovery code: %w", err)
		}
		code := strings.ToLower(
			base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
		codes = append(codes, code[:4]+"-"+code[4:])
		hashes = append(hashes, HashToken(NormaliseRecoveryCode(code[:4]+"-"+code[4:])))
	}
	return codes, hashes, nil
}

// NormaliseRecoveryCode makes lookup insensitive to how the user retyped it —
// case, spaces and the display hyphen all vary when someone copies a code off
// a printout.
func NormaliseRecoveryCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "")
	return strings.ReplaceAll(code, " ", "")
}

// QRCodeSVG renders the otpauth URI as a scannable SVG QR code.
//
// It is generated here rather than by a third-party image service on purpose:
// the URI embeds the TOTP secret, so sending it to an external renderer would
// hand that secret to someone else. SVG rather than PNG because the dashboard
// inlines it and it stays crisp at any size.
func QRCodeSVG(otpauthURI string) (string, error) {
	code, err := qr.Encode(otpauthURI, qr.M, qr.Auto)
	if err != nil {
		return "", fmt.Errorf("auth: encode qr: %w", err)
	}
	size := code.Bounds().Dx()
	const quiet = 4 // modules of mandatory quiet zone around the symbol
	total := size + quiet*2

	var svg strings.Builder
	fmt.Fprintf(&svg,
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" `+
			`shape-rendering="crispEdges" role="img" `+
			`aria-label="Scan this code in your authenticator app">`, total, total)
	fmt.Fprintf(&svg, `<rect width="%d" height="%d" fill="#fff"/>`, total, total)

	// Emit each dark module as a 1x1 rect. Runs are merged horizontally so a
	// typical code is a few hundred rects rather than a few thousand.
	for y := range size {
		for x := 0; x < size; {
			if _, _, _, alpha := code.At(x, y).RGBA(); alpha == 0 || !isDark(code, x, y) {
				x++
				continue
			}
			run := 1
			for x+run < size && isDark(code, x+run, y) {
				run++
			}
			fmt.Fprintf(&svg, `<rect x="%d" y="%d" width="%d" height="1" fill="#000"/>`,
				x+quiet, y+quiet, run)
			x += run
		}
	}
	svg.WriteString(`</svg>`)
	return svg.String(), nil
}

func isDark(code image.Image, x, y int) bool {
	r, g, b, _ := code.At(x, y).RGBA()
	return r == 0 && g == 0 && b == 0
}
