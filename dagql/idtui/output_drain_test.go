package idtui

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/dagger/dagger/dagql/dagui"
	"github.com/dagger/dagger/util/cleanups"
)

// A failed local drain must not wait for an exporter holding the frontend
// lock, inspect its DB, or turn incomplete command output into success.
func TestOutputDrainFailureSkipsBusyFrontend(t *testing.T) {
	for name, newFrontend := range map[string]func(io.Writer) Frontend{
		"logs": NewLogs, "dots": NewDots, "plain": NewPlain, "report": NewReporter,
	} {
		t.Run(name, func(t *testing.T) {
			frontend := newFrontend(io.Discard)
			var mu *sync.Mutex
			switch fe := frontend.(type) {
			case *frontendLogs:
				mu = &fe.mu
			case *frontendDots:
				mu = &fe.mu
			case *frontendPlain:
				mu = &fe.mu
				t.Cleanup(fe.ticker.Stop)
			case *frontendPretty:
				mu = &fe.reportMu
			}
			mu.Lock()
			defer mu.Unlock()
			result := make(chan error, 1)
			go func() {
				result <- frontend.Run(t.Context(), dagui.FrontendOpts{Silent: true}, func(context.Context) (cleanups.CleanupF, error) {
					return func() error { return &OutputDrainError{Err: context.DeadlineExceeded} }, ExitError{OriginalCode: 0}
				})
			}()
			select {
			case err := <-result:
				var exit ExitError
				if !errors.As(err, &exit) || exit.Code() != 1 {
					t.Fatalf("drain failure returned %v; want exit code 1", err)
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lost original drain error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("failed drain waited on the busy frontend")
			}
		})
	}
}
