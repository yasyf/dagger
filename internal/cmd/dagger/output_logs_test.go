package daggercmd

import (
	"context"
	"errors"
	"testing"
	"time"

	telemetry "github.com/dagger/otel-go"
	"github.com/stretchr/testify/require"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

func TestOutputAdmissionFailureIsReturnedToCommandCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	exporter := &diagnosticCallbackExporter{warn: func() {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}}
	p := &outputLogProcessor{Processor: telemetry.NewBlockingLogProcessor(exporter)}
	t.Cleanup(func() { close(release); _ = p.Shutdown(context.Background()) })
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(p))
	canceled, stop := context.WithCancel(telemetry.WithLoggerProvider(ctx, provider))
	stop()
	writer := telemetry.NewWriter(canceled, "test")
	// Writes with canceled contexts still succeed while capacity is available.
	for range 2048 {
		_, err := writer.Write([]byte("output"))
		require.NoError(t, err)
	}
	require.NoError(t, outputLogError([]sdklog.Processor{p}))
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The log API swallows the next admission error, but command cleanup must
	// retain it so an incomplete table cannot result in exit status zero.
	_, err := writer.Write([]byte("not admitted"))
	require.NoError(t, err)
	require.ErrorIs(t, outputLogError([]sdklog.Processor{p}), context.Canceled)
}

type shutdownErrorLogExporter struct {
	countingLogExporter
	err error
}

func (e *shutdownErrorLogExporter) Shutdown(context.Context) error { return e.err }

func TestOutputShutdownFailureIsReturnedToCommandCleanup(t *testing.T) {
	expected := errors.New("frontend shutdown failed")
	p := &outputLogProcessor{Processor: telemetry.NewBlockingLogProcessor(&shutdownErrorLogExporter{err: expected})}
	require.ErrorIs(t, p.Shutdown(t.Context()), expected)
	require.ErrorIs(t, outputLogError([]sdklog.Processor{p}), expected)
	require.NoError(t, outputLogError(nil), "remote-only telemetry does not fail the command")
}

type admissionErrorLogProcessor struct {
	sdklog.Processor
	err error
}

func (p admissionErrorLogProcessor) OnEmit(context.Context, *sdklog.Record) error { return p.err }

func TestDiagnosticAdmissionFailureRemainsBestEffort(t *testing.T) {
	expected := errors.New("diagnostic admission failed")
	p := &outputLogProcessor{Processor: admissionErrorLogProcessor{err: expected}}
	ctx := context.WithValue(t.Context(), diagnosticLogContextKey{}, true)
	var record sdklog.Record
	require.ErrorIs(t, p.OnEmit(ctx, &record), expected)
	require.NoError(t, outputLogError([]sdklog.Processor{p}), "diagnostics do not fail command output")
	require.ErrorIs(t, p.OnEmit(t.Context(), &record), expected)
	require.ErrorIs(t, outputLogError([]sdklog.Processor{p}), expected, "ordinary delivery failure remains fatal")
}
