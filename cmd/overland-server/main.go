package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tnmt/overland-server/internal/server"
	"github.com/tnmt/overland-server/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "overland-server:", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	dbPath := flag.String("db", "overland.db", "SQLite database path")
	tokenFile := flag.String("ingest-token-file", "", "file containing the token Overland must present")
	flag.Parse()

	if *tokenFile == "" {
		return errors.New("-ingest-token-file is required")
	}
	raw, err := os.ReadFile(*tokenFile)
	if err != nil {
		return fmt.Errorf("read ingest token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return errors.New("ingest token file is empty")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	st, err := store.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	srv := &http.Server{
		Addr:              *listen,
		Handler:           server.New(st, token, logger).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", *listen)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
