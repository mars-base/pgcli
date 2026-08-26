// pg - PostgreSQL database instance manager.
//
// // Single Go binary CLI tool, cross-platform (Linux/macOS/Windows).
// Uses Podman to manage PostgreSQL + pgBackRest container, leveraging PITR for filesystem time travel.
package main

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/mars-base/pgcli/internal/cli"
	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/logging"
	"github.com/mars-base/pgcli/internal/platform"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	setupLogging()

	cli.Version = version
	cli.BuildTime = buildTime
	cli.Execute()
}

// setupLogging reads the config file and configures the default slog logger level.
// It respects the --config/-c flag if present in os.Args. If the config file does
// not exist or cannot be parsed, defaults to info level.
func setupLogging() {
	path := platform.DefaultConfigPath()

	// Parse --config/-c from os.Args (Cobra hasn't run yet)
	for i, arg := range os.Args {
		if (arg == "--config" || arg == "-c") && i+1 < len(os.Args) {
			path = os.Args[i+1]
			break
		}
		if strings.HasPrefix(arg, "--config=") {
			path = strings.TrimPrefix(arg, "--config=")
			break
		}
	}

	cfg, err := config.Load(path)
	if err != nil {
		// Config not available, slog defaults to info
		return
	}

	var level slog.Level
	switch cfg.Logging.Level {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	dw, err := logging.NewDailyWriter()
	if err != nil {
		// If daily log file can't be created, fall back to stderr only
		handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
		slog.SetDefault(slog.New(handler))
		return
	}

	// Write to both stderr and daily log file
	w := io.MultiWriter(os.Stderr, dw)
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}
