package sending

import (
	"context"

	"github.com/google/uuid"
)

// Disposition is what a send on a channel does for one tenant.
type Disposition int

const (
	// SendLive goes to the channel's carrier and is billed.
	SendLive Disposition = iota
	// SendSimulated is recorded and settled by the sandbox: never handed to a
	// carrier, never billed, labelled simulated.
	SendSimulated
	// SendRefused is refused channel_unavailable before anything is held.
	SendRefused
)

// ChannelCheck answers for a tenant and channel. testMode is a send made with
// a developer test key: the one way a send on a sandbox channel is accepted.
type ChannelCheck func(ctx context.Context, tenantID uuid.UUID, channel string,
	testMode bool) (Disposition, error)

// disposition asks the server's ChannelCheck. A Service built without one —
// the workers that only settle or reconcile, and tests that drive the pipeline
// directly — treats every channel as live, which is how it behaved before
// channels had modes.
func (s *Service) disposition(ctx context.Context, tenantID uuid.UUID, channel string,
	testMode bool) (Disposition, error) {

	if s.Channels == nil {
		return SendLive, nil
	}
	return s.Channels(ctx, tenantID, channel, testMode)
}
