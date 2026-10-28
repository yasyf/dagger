package slog

import (
	"log/slog"
	"os"
)

// Diagnostics must not re-enter telemetry: they can be emitted by its consumer
// or error handler while producers are waiting for queue space. A separate,
// bounded stderr writer also prevents a stopped stderr reader from blocking
// those consumers. Diagnostics are best-effort and may be dropped at exit.
var diagnosticLogger = New(slog.NewTextHandler(diagnosticWriter{
	NewWriter(os.Stderr, 64*1024),
}, nil))

// Diagnostic returns the process logger for frontend and telemetry diagnostics.
// It bypasses telemetry and drops messages when its stderr queue is full.
func Diagnostic() *Logger { return diagnosticLogger }

type diagnosticWriter struct{ *Writer }

func (w diagnosticWriter) Write(p []byte) (int, error) {
	w.TryWrite(p)
	return len(p), nil
}
