package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tnmt/overland-server/internal/overland"
)

const maxBodyBytes = 16 << 20

type LocationStore interface {
	InsertLocations(ctx context.Context, locs []overland.Location, receivedAt time.Time) (int, error)
	Ping(ctx context.Context) error
}

type Server struct {
	store       LocationStore
	ingestToken string
	logger      *slog.Logger
	now         func() time.Time
}

func New(store LocationStore, ingestToken string, logger *slog.Logger) *Server {
	return &Server{store: store, ingestToken: ingestToken, logger: logger, now: time.Now}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/overland", s.handleIngest)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return mux
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var batch overland.Batch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&batch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	// Malformed points are dropped rather than failing the batch: Overland
	// keeps retrying a rejected batch forever, so one bad point would block
	// every later upload from the device.
	locs := make([]overland.Location, 0, len(batch.Locations))
	for i, f := range batch.Locations {
		loc, err := f.ToLocation()
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

// Overland can only be configured with a receiver URL, so the token is
// accepted as an access_token query parameter as well as a bearer header.
func (s *Server) authorized(r *http.Request) bool {
	token := r.URL.Query().Get("access_token")
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		token = bearer
	}
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.ingestToken)) == 1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
