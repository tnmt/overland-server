package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tnmt/overland-server/internal/days"
	"github.com/tnmt/overland-server/internal/overland"
)

const maxBodyBytes = 16 << 20

type LocationStore interface {
	InsertLocations(ctx context.Context, locs []overland.Location, receivedAt time.Time) (int, error)
	Ping(ctx context.Context) error
}

type Config struct {
	IngestToken string
	// ReadToken enables GET /api/days/{date}; empty disables it.
	ReadToken string
	Days      days.Source
	Location  *time.Location
}

type Server struct {
	store  LocationStore
	cfg    Config
	logger *slog.Logger
	now    func() time.Time
}

func New(store LocationStore, cfg Config, logger *slog.Logger) *Server {
	return &Server{store: store, cfg: cfg, logger: logger, now: time.Now}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/overland", s.handleIngest)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	if s.cfg.ReadToken != "" {
		mux.HandleFunc("GET /api/days/{date}", s.handleDay)
	}
	return mux
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if !ingestAuthorized(r, s.cfg.IngestToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var batch overland.Batch
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := decoder.Decode(&batch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	// Decode once more so that valid JSON followed by another value is not
	// silently accepted. This also catches accidental request concatenation.
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	// Answering "ok" to a payload in another client's format would make that
	// client drop its queue silently, so a misconfigured client must see an error.
	if batch.Locations == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing locations"})
		return
	}

	// Malformed points are dropped rather than failing the batch: Overland
	// keeps retrying a rejected batch forever, so one bad point would block
	// every later upload from the device.
	locs := make([]overland.Location, 0, len(batch.Locations))
	for i, f := range batch.Locations {
		loc, err := f.ToLocation(batch.DeviceID)
		if err != nil {
			s.logger.Warn("skipping invalid location", "index", i, "err", err)
			continue
		}
		locs = append(locs, loc)
	}

	inserted, err := s.store.InsertLocations(r.Context(), locs, s.now())
	if err != nil {
		s.logger.Error("store locations", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage failure"})
		return
	}
	s.logger.Info("ingested batch",
		"received", len(batch.Locations), "valid", len(locs), "inserted", inserted)

	// Overland only discards its local queue when it sees exactly this body.
	writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	if !tokenMatches(bearerToken(r), s.cfg.ReadToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	date, err := time.ParseInLocation(time.DateOnly, r.PathValue("date"), s.cfg.Location)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date must be YYYY-MM-DD"})
		return
	}
	day, err := days.Build(r.Context(), s.cfg.Days, date, s.cfg.Location)
	if err != nil {
		s.logger.Error("build day", "date", r.PathValue("date"), "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage failure"})
		return
	}
	writeJSON(w, http.StatusOK, day)
}

// Overland can only be configured with a receiver URL, so the ingest token is
// accepted as an access_token query parameter as well as a bearer header.
// The read API takes the header only, keeping its token out of access logs.
func ingestAuthorized(r *http.Request, want string) bool {
	token := r.URL.Query().Get("access_token")
	if bearer := bearerToken(r); bearer != "" {
		token = bearer
	}
	return tokenMatches(token, want)
}

func bearerToken(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return token
}

func tokenMatches(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
