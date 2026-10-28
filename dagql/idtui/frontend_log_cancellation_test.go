package idtui

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/dagger/dagger/dagql/dagui"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// An export that waits behind final rendering must check cancellation after
// acquiring the frontend lock, before changing the DB or primary output.
func TestFrontendRejectsCanceledLogExport(t *testing.T) {
	for name, newFrontend := range map[string]func(io.Writer) Frontend{
		"logs": NewLogs, "dots": NewDots, "plain": NewPlain,
	} {
		t.Run(name, func(t *testing.T) {
			frontend := newFrontend(io.Discard)
			var mu *sync.Mutex
			var db *dagui.DB
			switch fe := frontend.(type) {
			case *frontendLogs:
				mu, db = &fe.mu, fe.db
			case *frontendDots:
				mu, db = &fe.mu, fe.db
			case *frontendPlain:
				mu, db = &fe.mu, fe.db
				t.Cleanup(fe.ticker.Stop)
			}
			primary := prettyTestSpanID(1)
			frontend.SetPrimary(primary)
			var record sdklog.Record
			record.SetSpanID(primary.SpanID)
			record.SetBody(log.StringValue("late output"))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			mu.Lock()
			result := make(chan error, 1)
			go func() { result <- frontend.LogExporter().Export(ctx, []sdklog.Record{record}) }()
			cancel()
			mu.Unlock()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Export error = %v, want cancellation", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled export did not return")
			}
			if len(db.PrimaryLogs[primary]) != 0 {
				t.Fatal("canceled export modified primary output")
			}
		})
	}
}
