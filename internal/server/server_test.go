package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tnmt/overland-server/internal/store"
)

const testToken = "secret-token"

const batchBody = `{
	"locations": [
		{"type": "Feature", "geometry": {"type": "Point", "coordinates": [139.7671, 35.6812]},
		 "properties": {"timestamp": "2026-09-29T10:00:00+0900", "device_id": "phone"}},
		{"type": "Feature", "geometry": {"type": "Point", "coordinates": [139.7672, 35.6813]},
		 "properties": {"timestamp": "2026-09-29T10:00:30+0900", "device_id": "phone"}},
		{"type": "Feature", "geometry": {"type": "Point", "coordinates": [139.7673, 35.6814]},
		 "properties": {"timestamp": "not a time", "device_id": "phone"}}
	],
	"current": {"type": "Feature"}
}`

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(New(st, testToken, logger).Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func post(t *testing.T, url, auth, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func TestIngestAuthentication(t *testing.T) {
	ts, _ := newTestServer(t)

	cases := []struct {
		name, path, auth string
		want             int
	}{
		{"no token", "/api/overland", "", http.StatusUnauthorized},
		{"wrong query token", "/api/overland?access_token=nope", "", http.StatusUnauthorized},
		{"wrong bearer", "/api/overland", "Bearer nope", http.StatusUnauthorized},
		{"query token", "/api/overland?access_token=" + testToken, "", http.StatusOK},
		{"bearer token", "/api/overland", "Bearer " + testToken, http.StatusOK},
	}
	for _, c := range cases {
		if status, _ := post(t, ts.URL+c.path, c.auth, batchBody); status != c.want {
			t.Errorf("%s: status %d, want %d", c.name, status, c.want)
		}
	}
}

func TestIngestStoresAndDeduplicates(t *testing.T) {
	ts, st := newTestServer(t)
	url := ts.URL + "/api/overland?access_token=" + testToken

	for i := range 2 {
		status, body := post(t, url, "", batchBody)
		if status != http.StatusOK || body != `{"result":"ok"}` {
			t.Fatalf("attempt %d: got %d %s", i, status, body)
		}
	}

	n, err := st.CountLocations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("stored %d rows, want 2 (invalid point skipped, resend deduplicated)", n)
	}
}

func TestIngestRejectsNonOverlandBodies(t *testing.T) {
	ts, _ := newTestServer(t)
	url := ts.URL + "/api/overland?access_token=" + testToken

	for name, body := range map[string]string{
		"malformed json":    "{",
		"missing locations": `{"_type":"location","lat":35.68,"lon":139.76,"tst":1790000000}`,
		"null locations":    `{"locations":null}`,
	} {
		if status, _ := post(t, url, "", body); status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, status)
		}
	}

	if status, body := post(t, url, "", `{"locations":[]}`); status != http.StatusOK || body != `{"result":"ok"}` {
		t.Errorf("empty batch: got %d %s, want 200 ok", status, body)
	}
}
