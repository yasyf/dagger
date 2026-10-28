package daggercmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// Each write to the CLI's output produces one telemetry log record. A large
// table must not flood the frontend's 2048-record queue with cells and padding.
func TestArtifactTableLargeOutput(t *testing.T) {
	const count = 1000
	items := make([]listedArtifact, count)
	for i := range items {
		items[i] = listedArtifact{
			URI: fmt.Sprintf("dag+check://go/packages/test?go-package=package-%04d", i),
			DimensionKeys: []struct{ Dimension, Key string }{
				{"go/packages", fmt.Sprintf("package-%04d", i)},
				{"type:Check", "test"},
			},
		}
	}
	var output bytes.Buffer
	writes := 0
	err := writeArtifactTable(artifactTableWriterFunc(func(p []byte) (int, error) {
		writes++
		return output.Write(p)
	}), items, map[string]string{"go/packages": "go-package", "type:Check": "check"})
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	require.Len(t, lines, count+1)
	require.Equal(t, []string{"GO-PACKAGE", "CHECK"}, strings.Fields(lines[0]))
	for i, line := range lines[1:] {
		require.Equal(t, []string{fmt.Sprintf("package-%04d", i), "test"}, strings.Fields(line))
	}
	require.Less(t, writes, 2048, "table must fit within the default telemetry log queue")
	t.Logf("%d table rows produced %d output records (%d bytes)", count, writes, output.Len())
}

func TestArtifactTableOutputError(t *testing.T) {
	expected := errors.New("output unavailable")
	out := artifactTableWriterFunc(func([]byte) (int, error) { return 0, expected })
	require.ErrorIs(t, writeArtifactTable(out, nil, nil), expected)
}

func TestArtifactTableOutputRecordsRemainUTF8(t *testing.T) {
	for _, cellSize := range []int{1, 3, 127, 4095, 4096, 5000} {
		t.Run(fmt.Sprint(cellSize), func(t *testing.T) {
			const count = 100
			description := strings.Repeat("é猫😀", cellSize)
			items := make([]listedArtifact, count)
			for i := range items {
				items[i] = listedArtifact{
					URI:         "dag+check://test",
					Description: description,
					DimensionKeys: []struct{ Dimension, Key string }{
						{"type:Check", fmt.Sprintf("test-%03d", i)},
					},
				}
			}
			var output bytes.Buffer
			err := writeArtifactTable(artifactTableWriterFunc(func(p []byte) (int, error) {
				// Each write becomes a separate telemetry StringValue and must
				// be valid on its own for OTLP protobuf serialization.
				require.True(t, utf8.Valid(p), "output record contains a split character")
				return output.Write(p)
			}), items, map[string]string{"type:Check": "check"})
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
			require.Len(t, lines, count+1)
			require.Equal(t, []string{"CHECK", "DESCRIPTION"}, strings.Fields(lines[0]))
			for i, line := range lines[1:] {
				require.Equal(t, []string{fmt.Sprintf("test-%03d", i), description}, strings.Fields(line))
			}
		})
	}
}

func TestArtifactTableShortWrite(t *testing.T) {
	for _, size := range []int{1, 5000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			items := []listedArtifact{{URI: "dag+check://test", Description: strings.Repeat("x", size)}}
			out := artifactTableWriterFunc(func(p []byte) (int, error) {
				if size == 1 || len(p) >= artifactTableBufferSize {
					return len(p) - 1, nil
				}
				return len(p), nil
			})
			require.ErrorIs(t, writeArtifactTable(out, items, nil), io.ErrShortWrite)
		})
	}
}

type artifactTableWriterFunc func([]byte) (int, error)

func (f artifactTableWriterFunc) Write(p []byte) (int, error) { return f(p) }
