package telemetry

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// gatedLogExporter records every exported body in order. Its first export
// waits for release, and it fails the first failures exports.
type gatedLogExporter struct {
	release  chan struct{}
	entered  chan struct{}
	once     sync.Once
	mu       sync.Mutex
	bodies   []string
	failures int
	calls    int
}

func (e *gatedLogExporter) Export(ctx context.Context, records []sdklog.Record) error {
	e.once.Do(func() {
		close(e.entered)
		select {
		case <-e.release:
		case <-ctx.Done():
		}
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.calls <= e.failures {
		return errors.New("injected export failure")
	}
	for _, rec := range records {
		e.bodies = append(e.bodies, rec.Body().AsString())
	}
	return nil
}
func (*gatedLogExporter) Shutdown(context.Context) error   { return nil }
func (*gatedLogExporter) ForceFlush(context.Context) error { return nil }

func blockingTestRecord(i int) *sdklog.Record {
	var rec sdklog.Record
	rec.SetBody(log.StringValue(strconv.Itoa(i)))
	return &rec
}

// A burst far larger than the queue, against an exporter that is stalled and
// then fails twice, arrives whole and in order: the producer waits instead of
// losing records.
func TestBlockingLogProcessorLosesNothing(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	exporter := &gatedLogExporter{release: make(chan struct{}), entered: make(chan struct{}), failures: 2}
	p := NewBlockingLogProcessor(exporter, 8, 4, time.Millisecond)

	const burst = 500
	produced := make(chan error, 1)
	go func() {
		for i := range burst {
			if err := p.OnEmit(ctx, blockingTestRecord(i)); err != nil {
				produced <- err
				return
			}
		}
		produced <- nil
	}()
	select {
	case <-exporter.entered:
	case <-ctx.Done():
		t.Fatal("no export started")
	}
	close(exporter.release)
	require.NoError(t, <-produced)
	require.NoError(t, p.ForceFlush(ctx))
	require.NoError(t, p.Shutdown(ctx))

	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	require.Len(t, exporter.bodies, burst)
	for i, body := range exporter.bodies {
		require.Equal(t, strconv.Itoa(i), body, "records arrive in order, once each")
	}
}

type failingLogExporter struct{}

func (failingLogExporter) Export(context.Context, []sdklog.Record) error {
	return errors.New("cloud unreachable")
}
func (failingLogExporter) Shutdown(context.Context) error   { return nil }
func (failingLogExporter) ForceFlush(context.Context) error { return nil }

// With an exporter that never succeeds, Shutdown still returns at its bound
// and releases a producer waiting for queue space.
func TestBlockingLogProcessorShutdownIsBounded(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	p := NewBlockingLogProcessor(failingLogExporter{}, 1, 1, 0)
	require.NoError(t, p.OnEmit(ctx, blockingTestRecord(0)))
	waiting := make(chan error, 1)
	go func() {
		// Fills the queue while the first record is being retried, then waits.
		err := p.OnEmit(ctx, blockingTestRecord(1))
		if err == nil {
			err = p.OnEmit(ctx, blockingTestRecord(2))
		}
		waiting <- err
	}()

	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer shutdownCancel()
	start := time.Now()
	require.ErrorIs(t, p.Shutdown(shutdownCtx), context.DeadlineExceeded)
	require.Less(t, time.Since(start), 10*time.Second)
	require.NoError(t, <-waiting, "a waiting producer is released by Shutdown")
}

// Exporter implementations can block in terminal I/O without observing ctx.
// Shutdown must return at cancellation and close the exporter only after the
// in-flight export ends, including when Shutdown itself ignores cancellation.
func TestBlockingLogProcessorShutdownWithUncooperativeExporter(t *testing.T) {
	for _, blockIn := range []string{"export", "shutdown"} {
		t.Run(blockIn, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			exporter := &uncooperativeLogExporter{entered: entered, release: release, blockIn: blockIn, shutdownCalled: make(chan struct{})}
			p := NewBlockingLogProcessor(exporter, 1, 1, 0)
			require.NoError(t, p.OnEmit(t.Context(), blockingTestRecord(0)))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- p.Shutdown(ctx) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("exporter did not reach blocking operation")
			}
			cancel()
			select {
			case err := <-result:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(5 * time.Second):
				t.Fatal("Shutdown waited for an uncooperative exporter")
			}
			if blockIn == "export" {
				select {
				case <-exporter.shutdownCalled:
					t.Fatal("exporter Shutdown raced its active Export")
				default:
				}
			}
		})
	}
}

type uncooperativeLogExporter struct {
	entered, release chan struct{}
	blockIn          string
	shutdownCalled   chan struct{}
	exportCalls      atomic.Int32
}

func (e *uncooperativeLogExporter) Export(context.Context, []sdklog.Record) error {
	if e.exportCalls.Add(1) == 1 && e.blockIn == "export" {
		close(e.entered)
		<-e.release
	}
	return nil
}

func (e *uncooperativeLogExporter) Shutdown(context.Context) error {
	close(e.shutdownCalled)
	if e.blockIn == "shutdown" {
		close(e.entered)
		<-e.release
	}
	return nil
}

func (*uncooperativeLogExporter) ForceFlush(context.Context) error { return nil }

// A context-ignoring export may complete successfully after Shutdown returns.
// Its success must not start another batch or retain the canceled queue.
func TestBlockingLogProcessorStopsAfterCanceledExport(t *testing.T) {
	exporter := &uncooperativeLogExporter{
		entered: make(chan struct{}), release: make(chan struct{}),
		blockIn: "export", shutdownCalled: make(chan struct{}),
	}
	release := sync.OnceFunc(func() { close(exporter.release) })
	defer release()
	p := NewBlockingLogProcessor(exporter, 1, 1, 0)
	require.NoError(t, p.OnEmit(t.Context(), blockingTestRecord(0)))
	select {
	case <-exporter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("export did not start")
	}
	require.NoError(t, p.OnEmit(t.Context(), blockingTestRecord(1)))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, p.Shutdown(ctx), context.Canceled)
	release()
	select {
	case <-p.shutdownDone:
	case <-time.After(5 * time.Second):
		t.Fatal("background cleanup did not finish")
	}
	require.Equal(t, int32(1), exporter.exportCalls.Load(), "canceled queue must not reach exporter")
	require.Empty(t, p.queue, "canceled records must be released")
}
