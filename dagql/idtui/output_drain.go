package idtui

import (
	"errors"
	"fmt"

	"github.com/dagger/dagger/engine/slog"
)

// OutputDrainError means local command output could not be fully delivered.
// The frontend may still have an exporter blocked in I/O. Callers must skip
// final rendering and DB reads instead of waiting on that exporter's lock.
type OutputDrainError struct{ Err error }

func (e *OutputDrainError) Error() string { return fmt.Sprintf("drain command output: %v", e.Err) }
func (e *OutputDrainError) Unwrap() error { return e.Err }

func isOutputDrainError(err error) bool {
	var drainErr *OutputDrainError
	return errors.As(err, &drainErr)
}

func outputDrainFailure(err error) error {
	if !isOutputDrainError(err) {
		return nil
	}
	// stderr itself may be blocked. Report through the bounded diagnostic
	// sink and suppress synchronous error printing at the CLI entry point.
	slog.Diagnostic().Error("command output was not fully delivered", "error", err)
	return ExitError{OriginalCode: 1, Original: err}
}
