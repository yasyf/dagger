package daggercmd

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dagger/dagger/engine/slog"
	telemetry "github.com/dagger/otel-go"
	"github.com/stretchr/testify/require"
	apilog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

type diagnosticCallbackExporter struct {
	warn func()
	once sync.Once
	seen atomic.Bool
}

func (e *diagnosticCallbackExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.once.Do(e.warn)
	for _, record := range records {
		if record.Body().AsString() != "output" {
			e.seen.Store(true)
		}
	}
	return nil
}

func (*diagnosticCallbackExporter) ForceFlush(context.Context) error { return nil }
func (*diagnosticCallbackExporter) Shutdown(context.Context) error   { return nil }

func TestFrontendDiagnosticDoesNotBlockOutputExport(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var warn func()
	exporter := &diagnosticCallbackExporter{warn: func() {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return
		}
		warn()
	}}
	output := telemetry.NewBlockingLogProcessor(exporter)
	diagnostics := newDiagnosticLoggerProvider([]sdklog.Processor{output})
	outputProvider := sdklog.NewLoggerProvider(sdklog.WithProcessor(output))
	ctx = telemetry.WithLoggerProvider(ctx, outputProvider)
	ctx = slog.ContextWithLoggerProvider(ctx, diagnostics)
	logger := slog.SpanLogger(ctx, "test")
	require.Same(t, outputProvider, telemetry.LoggerProvider(ctx), "stdout keeps its original provider")
	warn = func() { logger.Warn("frontend warning") }
	t.Cleanup(func() {
		cancel()
		shutdownCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = diagnostics.Shutdown(shutdownCtx)
		_ = output.Shutdown(shutdownCtx)
	})
	var record sdklog.Record
	record.SetBody(apilog.StringValue("output"))
	// Reach the admission limit while an export is in progress. Its warning
	// must return even though another output record needs a synchronous flush.
	for range 2048 {
		require.NoError(t, output.OnEmit(ctx, &record))
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	done := make(chan error, 1)
	go func() { done <- output.OnEmit(ctx, &record) }()
	close(release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("frontend warning blocked log export")
	}
	require.NoError(t, diagnostics.Shutdown(ctx))
	// Draining diagnostics must leave the shared output processor running.
	require.NoError(t, output.OnEmit(ctx, &record))
	require.NoError(t, output.ForceFlush(ctx))
	require.True(t, exporter.seen.Load(), "diagnostic reached the original exporter")
}
