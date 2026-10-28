package slog

import (
	"context"
	"strings"
	"testing"

	telemetry "github.com/dagger/otel-go"
	"github.com/stretchr/testify/require"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

type captureLogProcessor struct {
	records []sdklog.Record
}

func (p *captureLogProcessor) OnEmit(_ context.Context, record *sdklog.Record) error {
	p.records = append(p.records, record.Clone())
	return nil
}

func (*captureLogProcessor) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }
func (*captureLogProcessor) ForceFlush(context.Context) error                       { return nil }
func (*captureLogProcessor) Shutdown(context.Context) error                         { return nil }

func TestDiagnosticProviderSurvivesSpanRebinding(t *testing.T) {
	output, diagnostics := new(captureLogProcessor), new(captureLogProcessor)
	outputProvider := sdklog.NewLoggerProvider(sdklog.WithProcessor(output))
	diagnosticProvider := sdklog.NewLoggerProvider(sdklog.WithProcessor(diagnostics))
	t.Cleanup(func() {
		require.NoError(t, diagnosticProvider.Shutdown(context.Background()))
		require.NoError(t, outputProvider.Shutdown(context.Background()))
	})
	rootID, childID := trace.SpanID{1}, trace.SpanID{2}
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: rootID})
	ctx := trace.ContextWithSpanContext(t.Context(), spanContext)
	ctx = telemetry.WithLoggerProvider(ctx, outputProvider)
	ctx = ContextWithLoggerProvider(ctx, diagnosticProvider)
	ctx = telemetry.ContextWithGlobalLogsSpan(ctx)
	child := trace.ContextWithSpanContext(ctx, spanContext.WithSpanID(childID))

	SpanLogger(ctx, "test").Warn("root warning")
	SpanLogger(child, "test").Warn("child warning")
	GlobalLogger(child, "test").Warn("global warning")
	_, err := telemetry.NewWriter(child, "test").Write([]byte("command output"))
	require.NoError(t, err)

	require.Len(t, diagnostics.records, 3)
	for i, want := range []struct {
		span trace.SpanID
		text string
	}{{rootID, "root warning"}, {childID, "child warning"}, {rootID, "global warning"}} {
		require.Equal(t, want.span, diagnostics.records[i].SpanID())
		require.True(t, strings.Contains(diagnostics.records[i].Body().AsString(), want.text))
	}
	require.Len(t, output.records, 1)
	require.Equal(t, childID, output.records[0].SpanID())
	require.Equal(t, "command output", output.records[0].Body().AsString())
}
