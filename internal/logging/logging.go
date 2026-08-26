// Package logging provides a daily rotating file writer for slog output.
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mars-base/pgcli/internal/platform"
)

// DailyWriter implements io.Writer, writing to ~/.pgcli/logs/pg-YYYY-MM-DD.log.
// It automatically rotates to a new file when the date changes.
type DailyWriter struct {
	mu    sync.Mutex
	dir   string
	today string
	file  *os.File
}

// NewDailyWriter creates a DailyWriter that writes log files under the pg log directory.
func NewDailyWriter() (*DailyWriter, error) {
	dir := filepath.Join(platform.DefaultConfigDir(), "logs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating log directory %s: %w", dir, err)
	}

	w := &DailyWriter{dir: dir}

	// Open today's log file
	if err := w.rotate(); err != nil {
		return nil, err
	}

	return w, nil
}

// Write implements io.Writer. If the date has changed, it rotates to a new file first.
func (w *DailyWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if today != w.today {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	return w.file.Write(p)
}

// rotate opens today's log file, closing any previously open file.
func (w *DailyWriter) rotate() error {
	if w.file != nil {
		w.file.Close()
	}

	w.today = time.Now().Format("2006-01-02")
	filename := filepath.Join(w.dir, fmt.Sprintf("pg-%s.log", w.today))

	f, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening log file %s: %w", filename, err)
	}
	w.file = f
	return nil
}

// Close closes the underlying file.
func (w *DailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}
