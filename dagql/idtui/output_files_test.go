package idtui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func outputFilesTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, dir)
	}
	return dir
}

func TestOutputFilesDeliverAfterFrontendStops(t *testing.T) {
	dir := outputFilesTempDir(t)
	// More than 2,048 writes, including multibyte text, must survive without
	// entering the frontend's telemetry writer or retaining the result in RAM.
	const rows = 4096
	line := "é猫😀 listing row\n"
	var output bytes.Buffer
	finished := false
	destination := outputFileWriterFunc(func(p []byte) (int, error) {
		require.True(t, finished, "the frontend must stop before stdout is written")
		return output.Write(p)
	})
	err := runWithOutputFiles(t.Context(), destination, func(ctx context.Context) error {
		for range 2 {
			err := WriteOutputFile(ctx, outputFileWriterFunc(func([]byte) (int, error) {
				t.Fatal("listing output entered the telemetry path")
				return 0, nil
			}), func(w io.Writer) error {
				for range rows {
					if _, err := io.WriteString(w, line); err != nil {
						return err
					}
				}
				return nil
			})
			require.NoError(t, err)
		}
		require.Empty(t, output.String())
		finished = true
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat(line, rows*2), output.String())
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, files, "the frontend removes its output files")
}

func TestOutputFilesDiscardIncompleteResult(t *testing.T) {
	dir := outputFilesTempDir(t)
	expected := errors.New("render failed")
	var output bytes.Buffer
	err := runWithOutputFiles(t.Context(), &output, func(ctx context.Context) error {
		return WriteOutputFile(ctx, io.Discard, func(w io.Writer) error {
			_, err := io.WriteString(w, "incomplete listing\n")
			return errors.Join(err, expected)
		})
	})
	require.ErrorIs(t, err, expected)
	require.Empty(t, output.String())
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestOutputFilesPropagateDeliveryFailureAndCleanUp(t *testing.T) {
	dir := outputFilesTempDir(t)
	writeErr, runErr := errors.New("stdout unavailable"), errors.New("command failed")
	err := runWithOutputFiles(t.Context(), outputFileWriterFunc(func([]byte) (int, error) {
		return 0, writeErr
	}), func(ctx context.Context) error {
		err := WriteOutputFile(ctx, io.Discard, func(w io.Writer) error {
			_, err := io.WriteString(w, "listing\n")
			return err
		})
		return errors.Join(runErr, err)
	})
	require.ErrorIs(t, err, writeErr)
	require.ErrorIs(t, err, runErr)
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestOutputFilesDeliverCompletedResultOnCommandFailure(t *testing.T) {
	expected := errors.New("command failed after rendering")
	var output bytes.Buffer
	err := runWithOutputFiles(t.Context(), &output, func(ctx context.Context) error {
		err := WriteOutputFile(ctx, io.Discard, func(w io.Writer) error {
			_, err := io.WriteString(w, "listing\n")
			return err
		})
		return errors.Join(expected, err)
	})
	require.ErrorIs(t, err, expected)
	require.Equal(t, "listing\n", output.String())
}

func TestOutputFilesPropagateReadFailureAndCleanUp(t *testing.T) {
	dir := outputFilesTempDir(t)
	err := runWithOutputFiles(t.Context(), io.Discard, func(ctx context.Context) error {
		err := WriteOutputFile(ctx, io.Discard, func(w io.Writer) error {
			_, err := io.WriteString(w, "listing\n")
			return err
		})
		if err != nil {
			return err
		}
		// Simulate losing the file handle between rendering and delivery.
		files := ctx.Value(outputFilesKey{}).(*outputFiles)
		return files.files[0].Close()
	})
	require.ErrorIs(t, err, os.ErrClosed)
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, files)
}

type outputFileWriterFunc func([]byte) (int, error)

func (f outputFileWriterFunc) Write(p []byte) (int, error) { return f(p) }
