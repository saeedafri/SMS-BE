package sending

import (
	"encoding/json"

	"github.com/saeedafri/sms-be/internal/store"
)

// whatsAppIncomplete says a WhatsApp sender cannot send yet: its setup is not
// finished, or the number was disconnected.
func whatsAppIncomplete(sender store.SenderID) bool {
	return sender.Channel == "WHATSAPP" && (sender.WhatsApp == nil || !sender.WhatsApp.Ready())
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
