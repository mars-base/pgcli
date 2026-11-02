package daemon

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/mars-base/pgcli/internal/config"
)

const (
	dayLayout = "2006-01-02"
	megabyte  = int64(1 << 20)
	// lumberjack treats MaxSize 0 as its 100 MB default, so daily-only mode
	// passes a value it will never reach (1 TiB).
	unlimitedMB = 1 << 20
)

// rotatingWriter feeds a lumberjack logger and adds date-based rotation on
// top: lumberjack only rotates by size. A nil/disabled rotation config never
// reaches this type.
type rotatingWriter struct {
	log     *lumberjack.Logger
	path    string
	daily   bool
	lastDay string
	now     func() time.Time
}

func newRotatingWriter(path string, rotate *config.DaemonLogRotate, now func() time.Time) *rotatingWriter {
	maxMB := unlimitedMB
	if rotate.MaxSize > 0 {
		maxMB = int((rotate.MaxSize + megabyte - 1) / megabyte)
	}
	w := &rotatingWriter{
		log: &lumberjack.Logger{
			Filename:   path,
			MaxSize:    maxMB,
			MaxBackups: rotate.Keep,
			LocalTime:  true,
		},
		path:  path,
		daily: rotate.Daily,
		now:   now,
	}
	w.lastDay = now().Format(dayLayout)
	w.rotateStaleDay()
	return w
}

// rotateStaleDay moves a log written on a previous day out of the way before
// the first new write, so a daemon restarted after midnight does not keep
// appending to yesterday's file.
func (w *rotatingWriter) rotateStaleDay() {
	if !w.daily {
		return
	}
	info, err := os.Stat(w.path)
	if err != nil || info.Size() == 0 {
		return
	}
	if info.ModTime().Format(dayLayout) != w.lastDay {
		w.rotate()
	}
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	if w.daily {
		day := w.now().Format(dayLayout)
		if day != w.lastDay {
			if info, err := os.Stat(w.path); err == nil && info.Size() > 0 {
				w.rotate()
			}
			w.lastDay = day
		}
	}
	return w.log.Write(p)
}

// rotate never fails the daemon's output path: losing rotation is preferable
// to breaking the child's stdout/stderr.
func (w *rotatingWriter) rotate() {
	if err := w.log.Rotate(); err != nil {
		fmt.Fprintf(os.Stderr, "daemon: rotating %s: %v\n", w.path, err)
	}
}

func (w *rotatingWriter) Close() error {
	return w.log.Close()
}
