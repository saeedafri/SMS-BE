// Package messaging owns the message lifecycle: what states exist, which
// transitions are legal, and what each one means for money.
package messaging

// State is where a message is in its life. These are our internal states; the
// dashboard contract collapses them to a smaller set (see ContractStatus).
//
// The distinction that matters most: Accepted means a carrier took the message,
// Delivered means a handset confirmed it. Merging those two is the dishonesty
// this product exists to compete against, so they are separate states and the
// type system keeps them apart.
type State string

const (
	StateQueued      State = "queued"
	StateSubmitting  State = "submitting"
	StateSubmitted   State = "submitted"
	StateAccepted    State = "accepted"
	StateDelivered   State = "delivered"
	StateUndelivered State = "undelivered"
	StateRejected    State = "rejected"
	StateExpired     State = "expired"
)

// legalTransitions is the whole state machine in one place. A transition
// absent from this map is a bug, not an edge case — and a carrier replaying a
// receipt must not be able to walk a message backwards out of a terminal state.
var legalTransitions = map[State][]State{
	StateQueued:     {StateSubmitting, StateRejected},
	StateSubmitting: {StateSubmitted, StateRejected},
	StateSubmitted:  {StateAccepted, StateRejected, StateExpired},
	StateAccepted:   {StateDelivered, StateUndelivered, StateExpired},
	// Terminal states go nowhere.
	StateDelivered:   {},
	StateUndelivered: {},
	StateRejected:    {},
	StateExpired:     {},
}

func CanTransition(from, to State) bool {
	for _, allowed := range legalTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// IsTerminal reports a state a message can never leave. The reconciler uses
// this to decide what still needs chasing.
func IsTerminal(state State) bool {
	return len(legalTransitions[state]) == 0
}

// BillingEffect is what a transition does to the money held against a message.
type BillingEffect string

const (
	// EffectNone leaves the hold in place: the message is still in flight.
	EffectNone BillingEffect = "none"
	// EffectCharge converts a hold into a real charge. Reached only by a
	// handset-confirmed delivery — this is "no charge for undelivered",
	// implemented as a state machine rather than promised in a report.
	EffectCharge BillingEffect = "charge"
	// EffectRelease returns held money to the wallet.
	EffectRelease BillingEffect = "release"
)

func EffectOf(state State) BillingEffect {
	switch state {
	case StateDelivered:
		return EffectCharge
	case StateUndelivered, StateRejected, StateExpired:
		return EffectRelease
	default:
		return EffectNone
	}
}

// ContractStatus maps an internal state to the dashboard contract's
// MessageStatus, whose enum is only queued/sent/delivered/failed/read.
//
// Accepted maps to "sent", NOT "delivered". That single line is where the
// honesty claim is kept or broken: a carrier accepting a message is not a
// handset receiving it, and the UI must never show otherwise.
func ContractStatus(state State) string {
	switch state {
	case StateQueued, StateSubmitting:
		return "queued"
	case StateSubmitted, StateAccepted:
		return "sent"
	case StateDelivered:
		return "delivered"
	case StateUndelivered, StateRejected, StateExpired:
		return "failed"
	default:
		return "queued"
	}
}

// SubmitStatus maps a state to the SEND RESULT's vocabulary, which is not the
// log's.
//
// The two enums differ on purpose and the difference is the customer's:
// SendMessageResult.status carries `rejected`, MessageStatus does not. So a
// message we refuse at submit is `rejected` in the answer the caller gets, and
// `failed` in the log they read afterwards, because the log has no way to spell
// the distinction.
//
// Refusing at submit and failing in delivery are the two moments a customer
// actually tells apart — "you would not take it" versus "you took it and it did
// not arrive" — and they lead to different fixes. Reporting both as `failed`,
// which is what ContractStatus does, collapses them.
func SubmitStatus(state State) string {
	if state == StateRejected {
		return "rejected"
	}
	return ContractStatus(state)
}

// ErrorClass groups failure reasons for the logs explorer, so a user can tell
// "the number is unreachable" from "we blocked it ourselves".
type ErrorClass string

const (
	ErrorBlocked     ErrorClass = "blocked"
	ErrorUnreachable ErrorClass = "unreachable"
	ErrorRejected    ErrorClass = "rejected"
	ErrorExpired     ErrorClass = "expired"
)

// ClassifyCarrierError maps a carrier's code to a class and a plain-language
// explanation. The PRD calls carrier error dictionaries a differentiator:
// "DELIVRD/UNDELIV/107" tells a developer nothing on its own.
func ClassifyCarrierError(code string) (ErrorClass, string) {
	switch code {
	case "":
		return "", ""
	case "ABSENT_SUBSCRIBER", "UNKNOWN_SUBSCRIBER":
		return ErrorUnreachable, "That number is not in service on the carrier's network."
	case "HANDSET_BUSY", "PHONE_OFF":
		return ErrorUnreachable, "The handset was unreachable for the whole validity period."
	case "DND_BLOCKED":
		return ErrorBlocked, "The recipient is on the national do-not-disturb register for this message category."
	case "SPAM_BLOCKED":
		return ErrorBlocked, "The carrier's spam filter blocked this message. Check the template wording and sender registration."
	case "INVALID_SENDER":
		return ErrorRejected, "The sender ID is not registered for this route. Check its DLT/10DLC registration."
	case "TEMPLATE_MISMATCH":
		return ErrorRejected, "The message body does not match the registered template exactly."
	case "EXPIRED":
		return ErrorExpired, "The carrier could not deliver within the validity period."
	default:
		return ErrorRejected, "The carrier rejected this message (" + code + ")."
	}
}
