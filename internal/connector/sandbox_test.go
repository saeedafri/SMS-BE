package connector

import (
	"context"
	"testing"
)

// Taking one message's report must not consume anybody else's.
//
// The conversation reply handler wanted a single report and called
// DrainReports, which empties the queue. Every other in-flight message's report
// went with it and was discarded, so those messages never settled: they sat at
// "sent" until the reconciler expired them as carrier silence. It surfaced as
// an unrelated spec failing whenever an inbox reply happened to run alongside
// it, which is the hardest kind of failure to place.
func TestTakingOneReportLeavesTheOthersQueued(t *testing.T) {
	sandbox := NewSandbox(0)
	mine, theirs := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	if _, err := sandbox.Submit(context.Background(), []Submission{
		{MessageID: mine, Msisdn: "+919820000002", Body: "mine", Channel: "SMS", Country: "IN"},
		{MessageID: theirs, Msisdn: "+919820000004", Body: "theirs", Channel: "SMS", Country: "IN"},
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	taken := sandbox.TakeReportsFor(mine)
	if len(taken) != 1 || taken[0].MessageID != mine {
		t.Fatalf("took %d reports, want exactly the one for %s: %+v", len(taken), mine, taken)
	}

	// The other message's report must still be there for the background drainer.
	left := sandbox.DrainReports()
	if len(left) != 1 || left[0].MessageID != theirs {
		t.Fatalf("the other message's report was consumed: %+v", left)
	}
}
