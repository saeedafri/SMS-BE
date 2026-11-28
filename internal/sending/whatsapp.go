package sending

import (
	"encoding/json"

	"github.com/saeedafri/sms-be/internal/store"
)

// whatsAppIncomplete says a WhatsApp sender cannot send yet: its setup is not
// finished, or the number was disconnected. A simulated send is exempt. It is
// a test-key send on a sandbox channel, the only kind channelDisposition
// simulates: nothing reaches Meta and nothing is billed, so a developer can
// build an integration before the business finishes onboarding. A live key, a
// live channel, a campaign and a reply are never simulated, so never exempt.
func whatsAppIncomplete(sender store.SenderID, disposition Disposition) bool {
	return disposition != SendSimulated && sender.Channel == "WHATSAPP" &&
		(sender.WhatsApp == nil || !sender.WhatsApp.Ready())
}

// SendableStatus is the approval a send judges a template by. A WhatsApp
// template Meta has reviewed is sendable only when Meta approved it, whatever
// our own status says. One that never went to Meta (waContent, no waState)
// keeps our own status, as the frontend's mock does.
func SendableStatus(template store.Template) string {
	if template.Channel != "WHATSAPP" || len(template.WAState) == 0 {
		return template.Status
	}
	var state struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(template.WAState, &state) != nil || state.Status == "" {
		return "not reviewed by WhatsApp"
	}
	if state.Status != "approved" {
		return "WhatsApp " + state.Status
	}
	return "approved"
}
