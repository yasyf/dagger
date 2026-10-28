package idtui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/dagger/dagger/dagql/dagui"
	"github.com/dagger/dagger/util/cleanups"
)

type outputFilesKey struct{}

type outputFiles struct {
	mu     sync.Mutex
	files  []*os.File
	closed bool
}

// RunWithOutputFiles delivers file-backed command results after the frontend
// has stopped. These results bypass telemetry, whose log queues are best effort.
func RunWithOutputFiles(ctx context.Context, frontend Frontend, opts dagui.FrontendOpts, run func(context.Context) (cleanups.CleanupF, error)) error {
	return runWithOutputFiles(ctx, os.Stdout, func(ctx context.Context) error {
		return frontend.Run(ctx, opts, run)
	})
}

func runWithOutputFiles(ctx context.Context, out io.Writer, run func(context.Context) error) (rerr error) {
	files := new(outputFiles)
	defer func() { rerr = errors.Join(rerr, files.close()) }()
	ctx = context.WithValue(ctx, outputFilesKey{}, files)
	rerr = run(ctx)
	return errors.Join(rerr, files.writeTo(out))
}

// WriteOutputFile renders a complete result to a private temporary file. The
// frontend owns the file once rendering succeeds. Outside a frontend run, write
// to fallback so callers with their own output writer retain that behavior.
func WriteOutputFile(ctx context.Context, fallback io.Writer, render func(io.Writer) error) error {
	files, ok := ctx.Value(outputFilesKey{}).(*outputFiles)
	if !ok {
		return render(fallback)
	}
	file, err := os.CreateTemp("", "dagger-list-output-*")
	if err != nil {
		return fmt.Errorf("create listing output: %w", err)
	}
	owned := false
	defer func() {
		if !owned {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	buffer := bufio.NewWriter(file)
	if err := render(buffer); err != nil {
		return err
	}
	if err := buffer.Flush(); err != nil {
		return fmt.Errorf("write listing output: %w", err)
	}
	files.mu.Lock()
	defer files.mu.Unlock()
	if files.closed {
		return errors.New("listing output is closed")
	}
	files.files = append(files.files, file)
	owned = true
	return nil
}

func (files *outputFiles) writeTo(out io.Writer) error {
	files.mu.Lock()
	defer files.mu.Unlock()
	files.closed = true
	for _, file := range files.files {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("read listing output: %w", err)
		}
		if _, err := io.Copy(out, file); err != nil {
			return fmt.Errorf("copy listing output: %w", err)
		}
	}
	return nil
}

func (files *outputFiles) close() error {
	files.mu.Lock()
	defer files.mu.Unlock()
	files.closed = true
	var result error
	for _, file := range files.files {
		result = errors.Join(result, file.Close(), os.Remove(file.Name()))
	}
	files.files = nil
	return result
}
