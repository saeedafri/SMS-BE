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
