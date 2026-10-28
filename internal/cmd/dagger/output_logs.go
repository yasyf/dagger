package daggercmd

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

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
