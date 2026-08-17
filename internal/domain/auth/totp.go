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

// DevTOTPSecret is the fixed secret used ONLY when dev endpoints are enabled.
//
// It is the well-known RFC 4226 example secret, and the frontend's browser specs
// assert on it by value because their mock returns it from every enrolment. A
// real enrolment mints 20 random bytes; this exists so an automated test can
// know the secret in advance, which is otherwise impossible without scanning a
// QR code.
//
// It is safe only because it is unreachable unless ENABLE_DEV_ENDPOINTS is set,
// the same switch that already exposes hooks to change your own role and empty
// your own wallet. That flag defaults to off and the process now refuses to
// start on an unrecognised value, so it cannot be enabled by accident.
const DevTOTPSecret = "JBSWY3DPEHPK3PXP"

// DevTOTPCode is the code accepted for DevTOTPSecret, and for nothing else.
//
// It is not a valid TOTP code for that secret at any point in time — no fixed
// code is. Accepting it is a deliberate test-only bypass, scoped as narrowly as
// it can be: see VerifyTOTPWithDevBypass.
const DevTOTPCode = "123456"

// NewTOTPSecret mints a base32 secret and the otpauth:// URI an authenticator
// app scans.
//
// dev makes the secret the fixed DevTOTPSecret instead of a random one. The
// caller passes the server's ENABLE_DEV_ENDPOINTS flag; nothing here reads the
// environment, so a unit test cannot turn it on by accident and production
// cannot reach it at all.
func NewTOTPSecret(accountName string, dev bool) (secret, otpauthURI string, err error) {
	if dev {
		secret = DevTOTPSecret
	} else {
		buf := make([]byte, 20)
		if _, err := rand.Read(buf); err != nil {
			return "", "", fmt.Errorf("auth: read totp secret: %w", err)
		}
		secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	}

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
		// Shown uppercase. base32's alphabet is A-Z and 2-7 — no 0, 1, 8 or 9 —
		// so uppercase is both its natural form and the easier one to copy off a
		// printout without misreading a character. Case is display-only:
		// NormaliseRecoveryCode lowercases before hashing, so a user who types
		// it back in either case still matches.
		code := strings.ToUpper(
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
		// width and height as well as viewBox.
		//
		// An SVG with only a viewBox has no intrinsic size. Inside a flex or
		// grid container it can compute to 0x0 and render as nothing at all —
		// present in the DOM, styled, and completely invisible. The enrolment
		// screen showed an empty space where the QR code should be, and the
		// only clue was a browser reporting the element as "hidden".
		//
		// Sized in CSS pixels at 4x the module count so the code is large
		// enough for a phone camera to read from a screen, and scalable from
		// there by the page's own CSS.
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %[1]d %[1]d" `+
			`width="%[2]d" height="%[2]d" `+
			`shape-rendering="crispEdges" role="img" `+
			// The label names the thing before saying what to do with it. A
			// screen-reader user hearing only "scan this code in your
			// authenticator app" has to infer what kind of code is on screen;
			// naming it first is how every other image on the page reads.
			`aria-label="QR code — scan this in your authenticator app">`, total, total*4)
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

// VerifyTOTPWithDevBypass is VerifyTOTP plus one narrow exception for automated
// tests: when dev endpoints are enabled AND the account's secret is exactly the
// fixed DevTOTPSecret, the fixed DevTOTPCode is also accepted.
//
// Both conditions are required, deliberately. Gating on the dev flag alone would
// make every account on a dev instance accept 123456, including one a developer
// had enrolled with a real authenticator app. Tying it to the dev secret as well
// means the bypass can only ever unlock an account that was enrolled by the same
// dev machinery — a real secret still demands a real code, even here.
func VerifyTOTPWithDevBypass(secret, code string, dev bool) bool {
	if dev && secret == DevTOTPSecret && code == DevTOTPCode {
		return true
	}
	return VerifyTOTP(secret, code)
}
