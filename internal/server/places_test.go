package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tnmt/overland-server/internal/store"
	"github.com/tnmt/overland-server/internal/timeline"
)

const (
	readTok  = "read-token"
	writeTok = "write-token"
)

func newPlacesServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	at := func(day, hour int) time.Time { return time.Date(2026, 9, day, hour, 0, 0, 0, time.UTC) }
	exp := timeline.Export{Visits: []timeline.Visit{
		{Start: at(1, 0), End: at(1, 2), Latitude: 35.0, Longitude: 139.0, PlaceID: "g-cafe", SemanticType: "UNKNOWN"},
		{Start: at(2, 0), End: at(2, 1), Latitude: 35.0002, Longitude: 139.0, PlaceID: "g-cafe", SemanticType: "UNKNOWN"},
		{Start: at(3, 0), End: at(3, 1), Latitude: 35.1, Longitude: 139.1, PlaceID: "g-shop", SemanticType: "UNKNOWN"},
	}}
	if _, err := st.InsertTimeline(context.Background(), exp, "google-timeline"); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{
		IngestToken: testToken, ReadToken: readTok, WriteToken: writeTok,
		Days: st, Places: st, Location: time.UTC,
	}
	ts := httptest.NewServer(New(st, cfg, logger).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func call(t *testing.T, ts *httptest.Server, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if resp.StatusCode != http.StatusNoContent {
		_ = json.NewDecoder(resp.Body).Decode(&out)
	}
	return resp.StatusCode, out
}

func TestPlacesTokensAreSeparate(t *testing.T) {
	ts := newPlacesServer(t)
	cases := []struct {
		method, path, token, body string
		want                      int
	}{
		{"GET", "/api/places", readTok, "", http.StatusOK},
		{"GET", "/api/places", writeTok, "", http.StatusUnauthorized},
		{"GET", "/api/places/unnamed", writeTok, "", http.StatusUnauthorized},
		{"GET", "/api/days/2026-09-01", writeTok, "", http.StatusUnauthorized},
		{"POST", "/api/places", readTok, `{"name":"Cafe","google_place_id":"g-cafe"}`, http.StatusUnauthorized},
		{"PATCH", "/api/places/1", readTok, `{"name":"x"}`, http.StatusUnauthorized},
		{"DELETE", "/api/places/1", readTok, "", http.StatusUnauthorized},
		{"POST", "/api/places", testToken, `{"name":"Cafe","google_place_id":"g-cafe"}`, http.StatusUnauthorized},
	}
	for _, c := range cases {
		if got, _ := call(t, ts, c.method, c.path, c.token, c.body); got != c.want {
			t.Errorf("%s %s with %s: status %d, want %d", c.method, c.path, c.token, got, c.want)
		}
	}
}

func TestPlacesLifecycle(t *testing.T) {
	ts := newPlacesServer(t)

	status, unnamed := call(t, ts, "GET", "/api/places/unnamed", readTok, "")
	list := unnamed["places"].([]any)
	if status != http.StatusOK || len(list) != 2 || list[0].(map[string]any)["google_place_id"] != "g-cafe" {
		t.Fatalf("unnamed before naming: %d %+v", status, unnamed)
	}

	// Naming by Google place ID fills coordinates from its visits.
	status, cafe := call(t, ts, "POST", "/api/places", writeTok, `{"name":"Cafe","google_place_id":"g-cafe"}`)
	if status != http.StatusCreated || cafe["radius_meters"] != 100.0 || cafe["latitude"].(float64) < 35.0 {
		t.Fatalf("create by place ID: %d %+v", status, cafe)
	}
	id := int64(cafe["id"].(float64))

	// Naming the same place ID again corrects the existing entry.
	status, renamed := call(t, ts, "POST", "/api/places", writeTok, `{"name":"Corner Cafe","google_place_id":"g-cafe"}`)
	if status != http.StatusOK || int64(renamed["id"].(float64)) != id || renamed["name"] != "Corner Cafe" {
		t.Fatalf("re-name by place ID: %d %+v", status, renamed)
	}

	// A place known only by coordinates gets the tighter default radius.
	status, spot := call(t, ts, "POST", "/api/places", writeTok, `{"name":"Spot","latitude":35.5,"longitude":139.5}`)
	if status != http.StatusCreated || spot["radius_meters"] != 50.0 || spot["google_place_id"] != nil {
		t.Fatalf("create by coordinates: %d %+v", status, spot)
	}

	_, unnamed = call(t, ts, "GET", "/api/places/unnamed", readTok, "")
	if list := unnamed["places"].([]any); len(list) != 1 || list[0].(map[string]any)["google_place_id"] != "g-shop" {
		t.Errorf("unnamed after naming: %+v", unnamed)
	}

	path := "/api/places/" + jsonNumber(id)
	if status, p := call(t, ts, "PATCH", path, writeTok, `{"radius_meters":30}`); status != http.StatusOK || p["radius_meters"] != 30.0 || p["name"] != "Corner Cafe" {
		t.Errorf("patch radius: %d %+v", status, p)
	}
	if status, _ := call(t, ts, "DELETE", path, writeTok, ""); status != http.StatusNoContent {
		t.Errorf("delete: %d", status)
	}
	if status, _ := call(t, ts, "DELETE", path, writeTok, ""); status != http.StatusNotFound {
		t.Errorf("delete again: %d", status)
	}

	_, all := call(t, ts, "GET", "/api/places", readTok, "")
	if list := all["places"].([]any); len(list) != 1 || list[0].(map[string]any)["name"] != "Spot" {
		t.Errorf("list after delete: %+v", all)
	}
}

func TestCreatePlaceValidation(t *testing.T) {
	ts := newPlacesServer(t)
	cases := []struct {
		body string
		want int
	}{
		{`{"name":" ","google_place_id":"g-cafe"}`, http.StatusBadRequest},
		{`{"name":"x"}`, http.StatusBadRequest},
		{`{"name":"x","latitude":35}`, http.StatusBadRequest},
		{`{"name":"x","latitude":95,"longitude":139}`, http.StatusBadRequest},
		{`{"name":"x","google_place_id":"g-cafe","radius_meters":0}`, http.StatusBadRequest},
		{`{"name":"x","google_place_id":"g-cafe","colour":"red"}`, http.StatusBadRequest},
		{`{"name":"x","google_place_id":"g-unknown"}`, http.StatusUnprocessableEntity},
		{`{"name":"x","google_place_id":"g-unknown","latitude":35,"longitude":139}`, http.StatusCreated},
	}
	for _, c := range cases {
		if got, body := call(t, ts, "POST", "/api/places", writeTok, c.body); got != c.want {
			t.Errorf("%s: status %d, want %d (%v)", c.body, got, c.want, body)
		}
	}
	if status, _ := call(t, ts, "PATCH", "/api/places/999", writeTok, `{"name":"x"}`); status != http.StatusNotFound {
		t.Errorf("patch missing: %d", status)
	}
	if status, _ := call(t, ts, "PATCH", "/api/places/1", writeTok, `{}`); status != http.StatusBadRequest {
		t.Errorf("empty patch: %d", status)
	}
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
