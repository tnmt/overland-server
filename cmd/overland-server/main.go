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
	_ "time/tzdata"

	"github.com/tnmt/overland-server/internal/days"
	"github.com/tnmt/overland-server/internal/server"
	"github.com/tnmt/overland-server/internal/store"
	"github.com/tnmt/overland-server/internal/timeline"
)

func main() {
	var err error
	if len(os.Args) > 1 && os.Args[1] == "import-google-timeline" {
		err = importTimeline(os.Args[2:])
	} else {
		err = serve(os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "overland-server:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("overland-server", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "HTTP listen address")
	dbPath := fs.String("db", "overland.db", "SQLite database path")
	ingestTokenFile := fs.String("ingest-token-file", "", "file containing the token Overland must present")
	readTokenFile := fs.String("read-token-file", "", "file containing the token for the read API; the API is disabled when unset")
	writeTokenFile := fs.String("write-token-file", "", "file containing the token for editing places; editing is disabled when unset")
	tz := fs.String("timezone", "Asia/Tokyo", "time zone that defines calendar days")
	fs.Parse(args)

	if *ingestTokenFile == "" {
		return errors.New("-ingest-token-file is required")
	}
	ingestToken, err := readToken(*ingestTokenFile)
	if err != nil {
		return fmt.Errorf("ingest token: %w", err)
	}
	var readTok string
	if *readTokenFile != "" {
		if readTok, err = readToken(*readTokenFile); err != nil {
			return fmt.Errorf("read token: %w", err)
		}
	}
	var writeTok string
	if *writeTokenFile != "" {
		if writeTok, err = readToken(*writeTokenFile); err != nil {
			return fmt.Errorf("write token: %w", err)
		}
	}
	if readTok != "" && readTok == writeTok {
		return errors.New("read and write tokens must differ")
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		return fmt.Errorf("timezone: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	st, err := store.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	cfg := server.Config{
		IngestToken: ingestToken, ReadToken: readTok, WriteToken: writeTok,
		Days: st, Places: st, Location: loc,
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           server.New(st, cfg, logger).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", *listen, "read_api", readTok != "", "write_api", writeTok != "")
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

func importTimeline(args []string) error {
	fs := flag.NewFlagSet("import-google-timeline", flag.ExitOnError)
	dbPath := fs.String("db", "overland.db", "SQLite database path")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: overland-server import-google-timeline -db PATH EXPORT.json...")
		fmt.Fprintln(fs.Output(), "Earlier files take precedence where exports overlap in time.")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
		return errors.New("no export files given")
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	for _, path := range fs.Args() {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		exp, err := timeline.Parse(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		res, err := st.InsertTimeline(context.Background(), exp, days.SourceTimeline)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		fmt.Printf("%s: visits %d/%d new, activities %d/%d new, %d skipped as covered by earlier imports\n",
			path, res.Visits, len(exp.Visits), res.Activities, len(exp.Activities), res.Overlapping)
	}
	return nil
}

func readToken(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errors.New("token file is empty")
	}
	return token, nil
}
