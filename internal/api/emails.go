package api

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"time"
)

// Transactional email copy. It lives here rather than in internal/mailer so
// that package stays a dumb transport with no opinion about this product.
//
// Plain HTML with inline styles and a visible fallback URL, deliberately: mail
// clients strip <style> blocks, several refuse remote CSS outright, and a
// button that renders as nothing leaves the recipient with no way to continue.

// accountEmail renders the message for a token kind. The false return is for
// kinds with no email — the caller then only logs, which is what the previous
// behaviour was for every kind.
func accountEmail(kind, baseURL, token string) (subject, body string, ok bool) {
	link := fmt.Sprintf("%s/%s?token=%s", baseURL, pathForKind(kind), url.QueryEscape(token))
	switch kind {
	case "email_verification":
		return "Confirm your Relay email address",
			layout("Confirm your email",
				"Confirm this address to finish setting up your Relay account.",
				"Confirm email address", link,
				"This link expires in 24 hours. If you did not create a Relay account, ignore this email."),
			true
	case "password_reset":
		return "Reset your Relay password",
			layout("Reset your password",
				"Use the button below to choose a new password for your Relay account.",
				"Reset password", link,
				"This link expires in 1 hour and can be used once. If you did not ask for a reset, "+
					"ignore this email — your password will not change."),
			true
	default:
		return "", "", false
	}
}

func pathForKind(kind string) string {
	if kind == "password_reset" {
		return "reset-password"
	}
	return "verify-email"
}

// teamInviteEmail tells someone they have been added to a workspace.
//
// It carries no token. In this product an invited member already exists the
// moment they are invited, and they get in through the ordinary password-reset
// flow — so a link here would have to be a second, parallel credential path,
// and the whole point of having one is that there is only one.
func teamInviteEmail(baseURL, tenantName, role string) (subject, body string) {
	return fmt.Sprintf("You have been added to %s on Relay", tenantName),
		layout("You have been added to a workspace",
			fmt.Sprintf("You now have %s access to %s on Relay. "+
				"Use “Forgot password” on the sign-in page to set your password and get in.",
				role, tenantName),
			"Go to Relay", baseURL+"/login",
			"If you were not expecting this, you can ignore this email.")
}

// sendInviteEmail delivers the invitation without failing the request if the
// provider is down — the member row is already committed, so reporting an
// error would suggest the invitation did not happen when it did. Detached from
// the request context for the same reason deliverToken is.
func (s *Server) sendInviteEmail(tenantName, email, role string) {
	subject, body := teamInviteEmail(s.appBaseURL(), tenantName, role)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 15*time.Second)
	defer cancel()
	if err := s.Mail.Send(ctx, email, subject, body); err != nil && s.Logger != nil {
		s.Logger.Error("team invite email failed", "email", email, "error", err)
	}
}

func layout(heading, intro, buttonLabel, link, footer string) string {
	// Every interpolated value is escaped. tenantName and role reach this from
	// user input, and the link carries a token — an unescaped & or < would
	// both corrupt the markup and hand us an injection point in a message we
	// send to other people's inboxes.
	safeLink := html.EscapeString(link)
	return fmt.Sprintf(`<!doctype html>
<html><body style="margin:0;padding:24px;background:#f6f7f9;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#1a1d21">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0"><tr><td align="center">
    <table role="presentation" width="100%%" style="max-width:520px;background:#ffffff;border:1px solid #e4e7eb;border-radius:8px;padding:32px">
      <tr><td>
        <p style="margin:0 0 4px;font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:#6b7280">Relay</p>
        <h1 style="margin:0 0 16px;font-size:20px;line-height:1.3">%s</h1>
        <p style="margin:0 0 24px;font-size:15px;line-height:1.6;color:#374151">%s</p>
        <p style="margin:0 0 24px">
          <a href="%s" style="display:inline-block;background:#1a1d21;color:#ffffff;text-decoration:none;padding:12px 20px;border-radius:6px;font-size:15px;font-weight:600">%s</a>
        </p>
        <p style="margin:0 0 8px;font-size:13px;color:#6b7280">Or paste this link into your browser:</p>
        <p style="margin:0 0 24px;font-size:12px;word-break:break-all;color:#374151">%s</p>
        <p style="margin:0;font-size:13px;line-height:1.6;color:#6b7280;border-top:1px solid #e4e7eb;padding-top:16px">%s</p>
      </td></tr>
    </table>
  </td></tr></table>
</body></html>`,
		html.EscapeString(heading), html.EscapeString(intro),
		safeLink, html.EscapeString(buttonLabel), safeLink, html.EscapeString(footer))
}
