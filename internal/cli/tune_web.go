package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mars-base/pgcli/internal/tune"
)

// runWebTuneServer starts an HTTP server that serves the interactive tuning
// site directly. The server listens on the specified address (default
// 127.0.0.1:10864) and serves the self-contained HTML page. Ctrl+C / SIGTERM
// gracefully shuts down the server.
func runWebTuneServer(listen string, doOpen bool, out io.Writer) error {
	prefill := tune.Inputs{
		RAMBytes: hostRAM(),
		CPUs:     hostCPUs(),
		OS:       probeOS(),
	}
	html, err := tune.WebHTML(prefill)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	})

	server := &http.Server{
		Addr:    listen,
		Handler: mux,
	}

	// Signal handling: SIGINT (Ctrl+C) or SIGTERM → graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Fprintln(out, "\nShutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	fmt.Fprintf(out, "Starting web-tune server on http://%s (Ctrl+C to stop)\n", listen)
	if doOpen {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := openBrowser("http://" + listen); err != nil {
				fmt.Fprintf(out, "server running (open http://%s manually — browser launch failed: %v)\n", listen, err)
			}
		}()
	}

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}
	fmt.Fprintf(out, "Server stopped.\n")
	return nil
}

