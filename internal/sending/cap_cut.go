package sending

import (
	"sort"

	"github.com/saeedafri/sms-be/internal/store"
)

// chooseUnderCap decides who a capped send carries out of one page of
// recipients, and who it withholds.
//
// Two rules, in this order:
//
//  1. A contact marked always_send is carried, whatever the cap says. That is
//     the whole point of the exemption — the customer's own staff, the numbers
//     a regulator samples, the accounts under contract. If the exempt alone
//     exceed the cap, they still all go: a guarantee that the cap can override
//     is not a guarantee.
//
//  2. The rest fill the remaining room, least-recently-carried first. The cap
//     has to cut somebody; cutting the SAME somebody on every send is how a
//     contact at the bottom of a list receives nothing, ever, while the numbers
//     on the report look fine. Ordering by last_capped_send_at with nulls first
//     spreads coverage across repeated sends without any state beyond the one
//     column.
//
// Applied per page rather than to the whole audience. A page is a random-enough
// slice of the list that taking the same share of each one yields the same share
// overall, and it keeps the fan-out's keyset cursor exactly as it was — the
// alternative was reordering the audience query itself, which would have meant
// a second cursor encoding for capped sends alone.
func chooseUnderCap(page []store.Contact, percent int) (send, withheld []store.Contact) {
	if percent >= 100 || len(page) == 0 {
		return page, nil
	}

	exempt := make([]store.Contact, 0, len(page))
	rest := make([]store.Contact, 0, len(page))
	for _, contact := range page {
		if contact.AlwaysSend {
			exempt = append(exempt, contact)
		} else {
			rest = append(rest, contact)
		}
	}

	// Rounded UP. At 70% of three recipients the choice is between two and
	// three; sending two is the cap doing its job, but rounding down at every
	// page turns a 70% cap into something materially lower on a long list, and
	// the operator set 70 rather than 65.
	allowed := (len(page)*percent + 99) / 100
	room := allowed - len(exempt)
	if room <= 0 {
		return exempt, rest
	}
	if room > len(rest) {
		room = len(rest)
	}

	// Stable, so two contacts never carried before keep the audience's own
	// order between them rather than an arbitrary one — a send repeated with no
	// change in between picks the same people, which is what makes a support
	// question answerable.
	sort.SliceStable(rest, func(i, j int) bool {
		left, right := rest[i].LastCappedSendAt, rest[j].LastCappedSendAt
		switch {
		case left == nil && right == nil:
			return false
		case left == nil:
			return true
		case right == nil:
			return false
		default:
			return left.Before(*right)
		}
	})

	return append(exempt, rest[:room]...), rest[room:]
}
