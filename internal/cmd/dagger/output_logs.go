package daggercmd

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	telemetry "github.com/dagger/otel-go"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// The log API cannot return emission errors to telemetry.Writer.Write. Remember
// failures in the frontend's processor so cleanup can fail the command instead
// of reporting successful delivery of incomplete stdout. Remote exporters keep
// their existing best-effort policy.
type outputLogProcessor struct {
	sdklog.Processor
	err atomic.Pointer[error]
}

// Capture exporter failures at the point where they occur. A batch processor
// reports asynchronous errors to the OTel error handler, not to ForceFlush.
func newOutputLogProcessor(exporter sdklog.Exporter) *outputLogProcessor {
	p := &outputLogProcessor{}
	p.Processor = telemetry.NewBlockingLogProcessor(&outputLogExporter{Exporter: exporter, processor: p})
	return p
}

type outputLogExporter struct {
	sdklog.Exporter
	processor *outputLogProcessor
}

func (e *outputLogExporter) Export(ctx context.Context, records []sdklog.Record) error {
	return e.processor.recordError(e.Exporter.Export(ctx, records))
}

func (p *outputLogProcessor) OnEmit(ctx context.Context, record *sdklog.Record) error {
	err := p.Processor.OnEmit(ctx, record)
	if ctx.Value(diagnosticLogContextKey{}) == true {
		return err
	}
	return p.recordError(err)
}

func (p *outputLogProcessor) Shutdown(ctx context.Context) error {
	return p.recordError(p.Processor.Shutdown(ctx))
}

func (p *outputLogProcessor) recordError(err error) error {
	if err == nil {
		return nil
	}
	err = fmt.Errorf("frontend output could not be delivered: %w", err)
	p.err.CompareAndSwap(nil, &err)
	return err
}

func outputLogError(processors []sdklog.Processor) error {
	var result error
	for _, processor := range processors {
		if p, ok := processor.(*outputLogProcessor); ok {
			if err := p.err.Load(); err != nil {
				result = errors.Join(result, *err)
			}
		}
	}
	return result
}
