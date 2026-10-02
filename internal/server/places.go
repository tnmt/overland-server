package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tnmt/overland-server/internal/store"
)

type PlaceStore interface {
	ListPlaces(ctx context.Context) ([]store.PlaceSummary, error)
	SavePlace(ctx context.Context, p store.Place) (store.Place, bool, error)
	UpdatePlace(ctx context.Context, id int64, name *string, radius *float64) (store.Place, bool, error)
	DeletePlace(ctx context.Context, id int64) (bool, error)
	UnnamedPlaces(ctx context.Context, limit int) ([]store.UnnamedPlace, error)
}

const (
	// Registered by Google place ID, a place is matched by ID; the radius
	// only matters for stays detected from recorded points nearby.
	defaultPlaceRadius = 100.0
	// Places registered by coordinates alone are matched purely by distance,
	// and registered places can be as little as ~100 m apart in town.
	defaultCoordinateRadius = 50.0
	maxPlaceRadius          = 5000.0
	maxPlaceNameLength      = 200
	defaultUnnamedLimit     = 50
	maxUnnamedLimit         = 500
	maxPlaceBodyBytes       = 64 << 10
)

type placeJSON struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Latitude      float64 `json:"latitude"`
	Longitude     float64 `json:"longitude"`
	RadiusMeters  float64 `json:"radius_meters"`
	GooglePlaceID string  `json:"google_place_id,omitempty"`
	Visits        *int    `json:"visits,omitempty"`
	MapsURL       string  `json:"maps_url"`
}

type unnamedPlaceJSON struct {
	GooglePlaceID string  `json:"google_place_id"`
	SemanticType  string  `json:"semantic_type"`
	Visits        int     `json:"visits"`
	Hours         float64 `json:"hours"`
	LastVisit     string  `json:"last_visit"`
	Latitude      float64 `json:"latitude"`
	Longitude     float64 `json:"longitude"`
	MapsURL       string  `json:"maps_url"`
}

func mapsURL(googlePlaceID string, lat, lon float64) string {
	if googlePlaceID != "" {
		return "https://www.google.com/maps/place/?q=place_id:" + url.QueryEscape(googlePlaceID)
	}
	return fmt.Sprintf("https://www.google.com/maps/search/?api=1&query=%.6f,%.6f", lat, lon)
}

func toPlaceJSON(p store.Place) placeJSON {
	return placeJSON{
		ID: p.ID, Name: p.Name, Latitude: p.Latitude, Longitude: p.Longitude,
		RadiusMeters: p.RadiusMeters, GooglePlaceID: p.GooglePlaceID,
		MapsURL: mapsURL(p.GooglePlaceID, p.Latitude, p.Longitude),
	}
}

func (s *Server) handleListPlaces(w http.ResponseWriter, r *http.Request) {
	places, err := s.cfg.Places.ListPlaces(r.Context())
	if err != nil {
		s.storageFailure(w, "list places", err)
		return
	}
	out := make([]placeJSON, 0, len(places))
	for _, p := range places {
		j := toPlaceJSON(p.Place)
		visits := p.Visits
		j.Visits = &visits
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, map[string]any{"places": out})
}

func (s *Server) handleUnnamedPlaces(w http.ResponseWriter, r *http.Request) {
	limit := defaultUnnamedLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxUnnamedLimit {
			badRequest(w, fmt.Sprintf("limit must be 1-%d", maxUnnamedLimit))
			return
		}
		limit = n
	}
	unnamed, err := s.cfg.Places.UnnamedPlaces(r.Context(), limit)
	if err != nil {
		s.storageFailure(w, "list unnamed places", err)
		return
	}
	out := make([]unnamedPlaceJSON, 0, len(unnamed))
	for _, u := range unnamed {
		out = append(out, unnamedPlaceJSON{
			GooglePlaceID: u.GooglePlaceID, SemanticType: u.SemanticType, Visits: u.Visits,
			Hours: math.Round(u.Hours*10) / 10, LastVisit: u.LastVisit.In(s.cfg.Location).Format(time.DateOnly),
			Latitude: u.Latitude, Longitude: u.Longitude, MapsURL: mapsURL(u.GooglePlaceID, 0, 0),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"places": out})
}

type createPlaceRequest struct {
	Name          string   `json:"name"`
	GooglePlaceID string   `json:"google_place_id"`
	Latitude      *float64 `json:"latitude"`
	Longitude     *float64 `json:"longitude"`
	RadiusMeters  *float64 `json:"radius_meters"`
}

func (s *Server) handleCreatePlace(w http.ResponseWriter, r *http.Request) {
	var req createPlaceRequest
	if err := decodeBody(w, r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	name, err := validName(req.Name)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	p := store.Place{Name: name, GooglePlaceID: strings.TrimSpace(req.GooglePlaceID), RadiusMeters: defaultPlaceRadius}
	switch {
	case req.Latitude != nil && req.Longitude != nil:
		if *req.Latitude < -90 || *req.Latitude > 90 || *req.Longitude < -180 || *req.Longitude > 180 {
			badRequest(w, "latitude/longitude out of range")
			return
		}
		p.Latitude, p.Longitude = *req.Latitude, *req.Longitude
	case req.Latitude != nil || req.Longitude != nil:
		badRequest(w, "latitude and longitude must be given together")
		return
	case p.GooglePlaceID == "":
		badRequest(w, "give google_place_id, or latitude and longitude")
		return
	}
	if p.GooglePlaceID == "" {
		p.RadiusMeters = defaultCoordinateRadius
	}
	if req.RadiusMeters != nil {
		if p.RadiusMeters, err = validRadius(*req.RadiusMeters); err != nil {
			badRequest(w, err.Error())
			return
		}
	}

	saved, created, err := s.cfg.Places.SavePlace(r.Context(), p)
	if errors.Is(err, store.ErrNoVisits) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "no imported visits for this google_place_id; give latitude and longitude",
		})
		return
	}
	if err != nil {
		s.storageFailure(w, "save place", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.logger.Info("saved place", "id", saved.ID, "created", created)
	writeJSON(w, status, toPlaceJSON(saved))
}

type updatePlaceRequest struct {
	Name         *string  `json:"name"`
	RadiusMeters *float64 `json:"radius_meters"`
}

func (s *Server) handleUpdatePlace(w http.ResponseWriter, r *http.Request) {
	id, ok := placeID(w, r)
	if !ok {
		return
	}
	var req updatePlaceRequest
	if err := decodeBody(w, r, &req); err != nil {
		badRequest(w, err.Error())
		return
	}
	if req.Name == nil && req.RadiusMeters == nil {
		badRequest(w, "give name and/or radius_meters")
		return
	}
	if req.Name != nil {
		name, err := validName(*req.Name)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		req.Name = &name
	}
	if req.RadiusMeters != nil {
		radius, err := validRadius(*req.RadiusMeters)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		req.RadiusMeters = &radius
	}

	p, found, err := s.cfg.Places.UpdatePlace(r.Context(), id, req.Name, req.RadiusMeters)
	if err != nil {
		s.storageFailure(w, "update place", err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such place"})
		return
	}
	s.logger.Info("updated place", "id", id)
	writeJSON(w, http.StatusOK, toPlaceJSON(p))
}

func (s *Server) handleDeletePlace(w http.ResponseWriter, r *http.Request) {
	id, ok := placeID(w, r)
	if !ok {
		return
	}
	found, err := s.cfg.Places.DeletePlace(r.Context(), id)
	if err != nil {
		s.storageFailure(w, "delete place", err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such place"})
		return
	}
	s.logger.Info("deleted place", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

func placeID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		badRequest(w, "invalid place id")
		return 0, false
	}
	return id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPlaceBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid json: %v", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("invalid json: trailing data")
	}
	return nil
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("name is required")
	}
	if len([]rune(name)) > maxPlaceNameLength {
		return "", fmt.Errorf("name must be at most %d characters", maxPlaceNameLength)
	}
	return name, nil
}

func validRadius(r float64) (float64, error) {
	if r <= 0 || r > maxPlaceRadius {
		return 0, fmt.Errorf("radius_meters must be in (0, %.0f]", maxPlaceRadius)
	}
	return r, nil
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

func (s *Server) storageFailure(w http.ResponseWriter, op string, err error) {
	s.logger.Error(op, "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "storage failure"})
}
