package idtui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/dagger/dagger/dagql/dagui"
	telemetry "github.com/dagger/otel-go"
	"github.com/stretchr/testify/require"
	"github.com/vito/tuist"
	"go.opentelemetry.io/otel/codes"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestFinalRenderTelemetryWarningWithoutTraceMessage(t *testing.T) {
	for _, reportOnly := range []bool{false, true} {
		for _, mode := range []string{"no report", "visible report", "quiet error"} {
			t.Run(fmt.Sprintf("reportOnly=%t/%s", reportOnly, mode), func(t *testing.T) {
				spanID := prettyTestSpanID(1)
				db := dagui.NewDB()
				db.ImportSnapshots([]dagui.SpanSnapshot{{
					ID: spanID, Name: "successful command", Final: true,
					StartTime: time.Unix(100, 0), EndTime: time.Unix(101, 0),
					Status: sdktrace.Status{Code: codes.Ok},
				}})
				db.SetPrimarySpan(spanID)
				db.PrimaryLogs[spanID] = []sdklog.Record{
					frontendTestLogRecord(spanID.SpanID, otellog.StringValue("command output\n"),
						otellog.Int(telemetry.StdioStreamAttr, 2)),
				}
				fe := newWithTerminal(io.Discard, db, tuist.NewHeadlessTerminal(80, 24))
				fe.reportOnly = reportOnly
				if mode == "visible report" {
					fe.Verbosity = dagui.ShowCompletedVerbosity
				}
				if mode == "quiet error" {
					fe.err = quietTestError{}
				}
				const message = "failed to shut down tracer provider: exporter failure"
				fe.SetTelemetryError(errors.New(message))
				require.Zero(t, fe.msgPreFinalRender.Len(), "no Cloud trace-link message is available")

				var output bytes.Buffer
				err := fe.FinalRender(&output)
				if mode == "quiet error" {
					var exit ExitError
					require.ErrorAs(t, err, &exit)
					require.Equal(t, 1, exit.Code())
				} else {
					require.NoError(t, err, "remote telemetry warnings stay nonfatal")
				}
				text := ansi.Strip(output.String())
				require.Equal(t, 1, strings.Count(text, message), "show the teardown failure exactly once")
				require.Contains(t, text, "WARN - failures detected while emitting telemetry")
				if mode == "quiet error" {
					require.Contains(t, text, "clean message")
				} else {
					require.Contains(t, text, "command output\n", "preserve the command's own output")
				}
			})
		}
	}
}
