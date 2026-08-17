// Package api holds the hand-written HTTP handlers satisfying the generated
// contract interface. It is the only package permitted to import internal/gen —
// internal/domain must stay free of generated types so the frontend can respec
// without the business logic caring.
package api

import (
	"errors"
	"fmt"
)

type notImplementedError struct{ operation string }

func (e notImplementedError) Error() string {
	return fmt.Sprintf("operation %s is not implemented yet", e.operation)
}

func errNotImplemented(operation string) error {
	return notImplementedError{operation: operation}
}

// errClickHouseUnavailable is returned when a message-log read is attempted
// without ClickHouse configured. It surfaces as a 500 rather than an empty
// page, because an empty page reads as "you have never sent anything" — a
// wrong answer is worse than an honest failure.
var errClickHouseUnavailable = errors.New("message logs require ClickHouse")


// errDependencyUnmet is returned when an action is legitimate but its
// preconditions are not yet satisfied — approving an email sender whose domain
// is not authenticated, or a voice sender whose caller-ID was never verified.
//
// It carries its own sentence because the operator needs to know WHICH
// precondition failed. "Cannot approve" tells them nothing they can act on.
//
// A sentinel rather than a typed 409 response because the contract declares
// only 200/401/404 on these operations; server.go maps this to a 409 with the
// standard envelope, the same way errForbidden and errInvalidFilter are handled.
type dependencyUnmetError struct{ reason string }

func (e dependencyUnmetError) Error() string { return e.reason }

func errDependencyUnmet(reason string) error {
	return dependencyUnmetError{reason: reason}
}
