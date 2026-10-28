package daggercmd

import (
	"context"
	"slices"

	telemetry "github.com/dagger/otel-go"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// Diagnostics are best effort: emitting a warning must not block frontend
// ingestion, including when ingestion is running in a log export. Command
// stdout/stderr continue to use the session's blocking processor directly.
func newDiagnosticLoggerProvider(processors []sdklog.Processor) *sdklog.LoggerProvider {
	return sdklog.NewLoggerProvider(
		sdklog.WithResource(telemetry.Resource),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(diagnosticLogExporter{
			LogForwarder: telemetry.LogForwarder{Processors: slices.Clone(processors)},
		}, sdklog.WithExportInterval(telemetry.NearlyImmediate))),
	)
}

// The session owns the downstream processors and flushes them after draining
// diagnostics. Do not flush or shut down the frontend through this extra hop.
type diagnosticLogExporter struct {
	telemetry.LogForwarder
}

type diagnosticLogContextKey struct{}

func (e diagnosticLogExporter) Export(ctx context.Context, records []sdklog.Record) error {
	// A diagnostic that cannot reach the frontend stays best effort. The
	// output processor must not turn loss in this queue into a command error.
	return e.LogForwarder.Export(context.WithValue(ctx, diagnosticLogContextKey{}, true), records)
}

func (diagnosticLogExporter) ForceFlush(context.Context) error { return nil }
func (diagnosticLogExporter) Shutdown(context.Context) error   { return nil }
