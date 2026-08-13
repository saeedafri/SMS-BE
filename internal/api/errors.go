// Package api holds the hand-written HTTP handlers satisfying the generated
// contract interface. It is the only package permitted to import internal/gen —
// internal/domain must stay free of generated types so the frontend can respec
// without the business logic caring.
package api

import "fmt"

type notImplementedError struct{ operation string }

func (e notImplementedError) Error() string {
	return fmt.Sprintf("operation %s is not implemented yet", e.operation)
}

func errNotImplemented(operation string) error {
	return notImplementedError{operation: operation}
}
