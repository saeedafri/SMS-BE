package api

import (
	"context"

	"github.com/saeedafri/sms-be/internal/store"
)

// The daily volume ceiling on the single-send path.
//
// A ceiling that only covered campaigns would be lifted by a loop around
// POST /v1/messages, which is the first thing anybody hitting it would try.
//
// Verification codes are deliberately NOT checked: an OTP withheld is a
// customer's own user locked out of their account, which is an incident rather
// than a margin. The ceiling exists to bound bulk traffic, and verify.go does
// not call this.
//
// Two reads rather than one atomic reserve, on purpose. A reserve would take a
// row lock per message and serialise one tenant's whole API throughput behind a
// single row — the exact cost the batched campaign path was built to escape.
// What that leaves is an overshoot bounded by the number of requests in flight,
// which for a margin control is noise.
func (s *Server) admitUnderSendCap(ctx context.Context,
	identity store.Identity) (store.SendAllowance, bool) {
	allowance, err := store.ReadSendAllowance(ctx, s.DB, identity, s.now())
	if err != nil {
		// Fails OPEN, and silently. A ceiling that takes a customer's sending
		// down with the database has stopped protecting a margin and started
		// causing an outage, and the side to err on is the one that costs us
		// money rather than them.
		if s.Logger != nil {
			s.Logger.Warn("send ceiling unreadable; allowing the send",
				"tenant_id", identity.TenantID.String(), "error", err)
		}
		return store.SendAllowance{}, true
	}
	if !allowance.Capped() || allowance.Room(1) > 0 {
		return allowance, true
	}
	if err := store.RecordSendUsage(ctx, s.DB, identity, allowance.Day, 0, 1); err != nil &&
		s.Logger != nil {
		s.Logger.Warn("send ceiling refusal not recorded",
			"tenant_id", identity.TenantID.String(), "error", err)
	}
	return allowance, false
}

// recordSendUnderCap counts a message that actually went out.
//
// What LEFT, not what was asked for — the campaign path counts the same thing,
// and a ceiling that meant "messages attempted" on one route and "messages
// sent" on the other would be impossible to explain to the operator reading
// both numbers on one screen.
func (s *Server) recordSendUnderCap(ctx context.Context, identity store.Identity,
	allowance store.SendAllowance, status string) {

	if !allowance.Capped() || status == "rejected" || status == "failed" {
		return
	}
	if err := store.RecordSendUsage(ctx, s.DB, identity, allowance.Day, 1, 0); err != nil &&
		s.Logger != nil {
		s.Logger.Warn("send not counted against the ceiling",
			"tenant_id", identity.TenantID.String(), "error", err)
	}
}
