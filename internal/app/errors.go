package app

import (
	"context"
	"errors"

	"github.com/marcus/comms/internal/domain"
)

// PublicErrorMessage is the final output boundary. Provider-coded errors and
// unclassified failures never expose implementation/driver text to users.
func PublicErrorMessage(err error) string {
	var opaque interface{ Code() int }
	if errors.As(err, &opaque) {
		return "storage operation failed"
	}
	for _, kind := range []error{domain.ErrInvalid, ErrNotFound, ErrConflict, ErrUnavailable, ErrOverloaded, ErrClosed, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, kind) {
			return err.Error()
		}
	}
	return "internal error"
}
