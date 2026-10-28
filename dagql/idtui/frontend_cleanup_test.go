package idtui

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/dagger/dagger/dagql/dagui"
	"github.com/dagger/dagger/util/cleanups"
	"github.com/stretchr/testify/require"
	"github.com/vito/tuist"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestPrettyCleanupFailureSurvivesSuccessfulSpan(t *testing.T) {
	for _, tc := range []struct {
		name      string
		noExit    bool
		shellExit bool
		runError  bool
		exitCode  int
	}{
		{name: "immediate cleanup"},
		{name: "deferred cleanup", noExit: true},
		{name: "shell exit during cleanup", shellExit: true},
		{name: "run and cleanup fail", runError: true},
		{name: "explicit failure survives shell exit", shellExit: true, exitCode: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spanID := prettyTestSpanID(1)
			db := dagui.NewDB()
			db.ImportSnapshots([]dagui.SpanSnapshot{{
				ID: spanID, Name: "successful command", Final: true,
				StartTime: time.Unix(100, 0), EndTime: time.Unix(101, 0),
				Status: sdktrace.Status{Code: codes.Ok},
			}})
			db.SetPrimarySpan(spanID)
			fe := newWithTerminal(io.Discard, db, tuist.NewHeadlessTerminal(80, 24))
			// Exercise the live lifecycle methods with synchronous dispatch,
			// without starting a terminal event loop or a real command.
			fe.reportOnly = true
			fe.NoExit = tc.noExit
			fe.quit = make(chan struct{})
			fe.runCtx, fe.interrupt = context.WithCancelCause(t.Context())
			defer fe.interrupt(context.Canceled)
			cleanupErr := errors.New("frontend output could not be delivered")
			var runErr error
			if tc.runError {
				runErr = errors.New("command failed")
			}
			if tc.exitCode != 0 {
				runErr = ExitError{OriginalCode: tc.exitCode}
			}
			fe.run = func(context.Context) (cleanups.CleanupF, error) {
				return func() error {
					if tc.shellExit {
						fe.interrupt(ErrShellExited)
					}
					_ = fe.Close()
					return cleanupErr
				}, runErr
			}
			fe.spawnRun()
			if tc.noExit {
				require.True(t, fe.done)
				require.NotNil(t, fe.cleanup)
				fe.quitAction(ErrShellExited)
			}
			select {
			case <-fe.quit:
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not complete")
			}
			err := normalizeFrontendExit(fe.runResult(), db)
			require.ErrorIs(t, err, cleanupErr)
			if runErr != nil {
				require.ErrorIs(t, err, runErr)
			}
			if tc.shellExit {
				var exit ExitError
				require.ErrorAs(t, err, &exit)
				require.NotZero(t, exit.Code(), "ordinary shell exit cannot hide cleanup failure")
				if tc.exitCode != 0 {
					require.Equal(t, tc.exitCode, exit.Code(), "preserve the command's explicit failure code")
				}
			}
		})
	}
}

func TestPrettyRunResultKeepsOrdinaryShellExit(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(ErrShellExited)
	fe := &frontendPretty{runCtx: ctx}
	require.ErrorIs(t, fe.runResult(), ErrShellExited)
}
