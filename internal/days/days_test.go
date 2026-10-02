package days

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/tnmt/overland-server/internal/overland"
	"github.com/tnmt/overland-server/internal/store"
	"github.com/tnmt/overland-server/internal/timeline"
)

var tokyo = time.FixedZone("Asia/Tokyo", 9*3600)

const (
	homeLat, homeLon     = 35.0000, 139.0000
	officeLat, officeLon = 35.0500, 139.0500
)

func jst(day, hour, minute int) time.Time {
	return time.Date(2026, 9, day, hour, minute, 0, 0, tokyo)
}

func newStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

func addPlace(t *testing.T, path, name string, lat, lon float64, googleID string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var gid any
	if googleID != "" {
		gid = googleID
	}
	if _, err := db.Exec(`INSERT INTO places (name, latitude, longitude, google_place_id) VALUES (?, ?, ?, ?)`,
		name, lat, lon, gid); err != nil {
		t.Fatal(err)
	}
}

func recordMinutes(t *testing.T, st *store.Store, from, to time.Time, lat, lon, acc float64) {
	t.Helper()
	var locs []overland.Location
	for ts := from; !ts.After(to); ts = ts.Add(time.Minute) {
		a := acc
		locs = append(locs, overland.Location{
			DeviceID: "phone", RecordedAt: ts, Latitude: lat, Longitude: lon,
			HorizontalAccuracy: &a, RawProperties: json.RawMessage(`{}`),
		})
	}
	if _, err := st.InsertLocations(context.Background(), locs, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestBuildFillsTimelineGapsWithRecordedPoints(t *testing.T) {
	st, path := newStore(t)
	ctx := context.Background()

	exp := timeline.Export{
		Visits: []timeline.Visit{
			{Start: jst(29, 22, 0), End: jst(30, 8, 30), Latitude: homeLat, Longitude: homeLon, PlaceID: "g-home", SemanticType: "HOME"},
			{Start: jst(30, 9, 10), End: jst(30, 12, 0), Latitude: officeLat, Longitude: officeLon, PlaceID: "g-office", SemanticType: "WORK"},
			{Start: jst(30, 9, 0), End: jst(30, 18, 0), Latitude: officeLat, Longitude: officeLon, PlaceID: "g-building", HierarchyLevel: 1},
		},
		Activities: []timeline.Activity{
			{Start: jst(30, 8, 30), End: jst(30, 9, 10), Mode: "IN_TRAIN", DistanceMeters: 7000},
		},
	}
	if _, err := st.InsertTimeline(ctx, exp, SourceTimeline); err != nil {
		t.Fatal(err)
	}
	// The timeline has nothing after 12:00: still at the office, then home.
	recordMinutes(t, st, jst(30, 12, 1), jst(30, 18, 0), officeLat, officeLon, 15)
	recordMinutes(t, st, jst(30, 19, 0), jst(30, 23, 0), homeLat, homeLon, 15)
	// Imprecise fixes elsewhere must not create a stay.
	recordMinutes(t, st, jst(30, 18, 10), jst(30, 18, 50), 35.2, 139.2, 500)

	addPlace(t, path, "Home", homeLat, homeLon, "")
	addPlace(t, path, "Office", 0, 0, "g-office")

	day, err := Build(ctx, st, jst(30, 0, 0), tokyo)
	if err != nil {
		t.Fatal(err)
	}

	type want struct {
		start, end time.Time
		source     string
		place      string
		googleID   string
	}
	wants := []want{
		{jst(29, 22, 0), jst(30, 8, 30), SourceTimeline, "Home", "g-home"},
		{jst(30, 9, 10), jst(30, 12, 0), SourceTimeline, "Office", "g-office"},
		{jst(30, 12, 1), jst(30, 18, 0), SourceRecorded, "Office", "g-office"},
		{jst(30, 19, 0), jst(30, 23, 0), SourceRecorded, "Home", "g-home"},
	}
	if len(day.Stays) != len(wants) {
		t.Fatalf("got %d stays, want %d: %+v", len(day.Stays), len(wants), day.Stays)
	}
	for i, w := range wants {
		s := day.Stays[i]
		name := ""
		if s.Place != nil {
			name = s.Place.Name
		}
		if !s.Start.Equal(w.start) || !s.End.Equal(w.end) || s.Source != w.source || name != w.place || s.GooglePlaceID != w.googleID {
			t.Errorf("stay %d = %v-%v %s %q %q, want %v-%v %s %q %q",
				i, s.Start, s.End, s.Source, name, s.GooglePlaceID, w.start, w.end, w.source, w.place, w.googleID)
		}
	}
	if len(day.Moves) != 1 || day.Moves[0].Mode != "IN_TRAIN" {
		t.Errorf("unexpected moves: %+v", day.Moves)
	}
	if day.Date != "2026-09-30" {
		t.Errorf("date = %q", day.Date)
	}
}

func TestBuildPrefersTimelineWhereBothExist(t *testing.T) {
	st, _ := newStore(t)
	ctx := context.Background()
	// The import covers the morning, misses 12:00-15:00, and covers the
	// evening; points were recorded all day.
	exp := timeline.Export{Visits: []timeline.Visit{
		{Start: jst(30, 8, 0), End: jst(30, 12, 0), Latitude: officeLat, Longitude: officeLon, PlaceID: "g-office"},
		{Start: jst(30, 15, 0), End: jst(30, 18, 0), Latitude: officeLat, Longitude: officeLon, PlaceID: "g-office"},
	}}
	if _, err := st.InsertTimeline(ctx, exp, SourceTimeline); err != nil {
		t.Fatal(err)
	}
	recordMinutes(t, st, jst(30, 8, 0), jst(30, 18, 0), officeLat, officeLon, 10)

	day, err := Build(ctx, st, jst(30, 0, 0), tokyo)
	if err != nil {
		t.Fatal(err)
	}
	if len(day.Stays) != 3 {
		t.Fatalf("got %d stays, want 3: %+v", len(day.Stays), day.Stays)
	}
	mid := day.Stays[1]
	if mid.Source != SourceRecorded || !mid.Start.Equal(jst(30, 12, 0)) || !mid.End.Equal(jst(30, 15, 0)) {
		t.Errorf("gap stay = %v-%v %s, want 12:00-15:00 recorded", mid.Start, mid.End, mid.Source)
	}
}

func TestLongestUncovered(t *testing.T) {
	h := func(hour int) time.Time { return jst(30, hour, 0) }
	covered := []interval{{h(13), h(14)}, {h(9), h(10)}, {h(10), h(11)}}
	got, ok := longestUncovered(interval{h(8), h(18)}, covered)
	if !ok || !got.start.Equal(h(14)) || !got.end.Equal(h(18)) {
		t.Errorf("got %v-%v %v, want 14:00-18:00", got.start, got.end, ok)
	}
	if _, ok := longestUncovered(interval{h(9), h(11)}, covered); ok {
		t.Error("fully covered interval should have no gap")
	}
}

func TestBuildWithoutImportOrPlaces(t *testing.T) {
	st, _ := newStore(t)
	recordMinutes(t, st, jst(30, 10, 0), jst(30, 11, 0), homeLat, homeLon, 10)

	day, err := Build(context.Background(), st, jst(30, 0, 0), tokyo)
	if err != nil {
		t.Fatal(err)
	}
	if len(day.Stays) != 1 || day.Stays[0].Place != nil || day.Stays[0].GooglePlaceID != "" {
		t.Fatalf("unexpected stays: %+v", day.Stays)
	}

	empty, err := Build(context.Background(), st, jst(28, 0, 0), tokyo)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Stays == nil || empty.Moves == nil || len(empty.Stays) != 0 {
		t.Errorf("empty day should have empty, non-nil slices: %+v", empty)
	}
}
