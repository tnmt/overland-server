package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tnmt/overland-server/internal/timeline"
)

func TestInsertTimelineEarlierImportWins(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	h := func(hour int) time.Time { return time.Date(2026, 9, 30, hour, 0, 0, 0, time.UTC) }

	first := timeline.Export{
		Visits:     []timeline.Visit{{Start: h(0), End: h(8), PlaceID: "a"}, {Start: h(0), End: h(12), PlaceID: "parent", HierarchyLevel: 1}},
		Activities: []timeline.Activity{{Start: h(8), End: h(9), Mode: "WALKING"}},
	}
	if _, err := st.InsertTimeline(ctx, first, "x"); err != nil {
		t.Fatal(err)
	}

	second := timeline.Export{
		Visits: []timeline.Visit{
			{Start: h(0), End: h(8), PlaceID: "a"},                       // exact duplicate
			{Start: h(7), End: h(10), PlaceID: "b"},                      // overlaps
			{Start: h(10), End: h(11), PlaceID: "c"},                     // fills a gap
			{Start: h(10), End: h(11), PlaceID: "p2", HierarchyLevel: 1}, // overlaps parent
		},
		Activities: []timeline.Activity{
			{Start: h(8), End: h(9), Mode: "IN_TRAIN"}, // overlaps
			{Start: h(9), End: h(10), Mode: "CYCLING"}, // fills a gap
		},
	}
	res, err := st.InsertTimeline(ctx, second, "y")
	if err != nil {
		t.Fatal(err)
	}
	if res.Visits != 1 || res.Activities != 1 || res.Overlapping != 4 {
		t.Errorf("got %+v, want 1 visit, 1 activity, 4 overlapping", res)
	}
}

func TestIntervalSet(t *testing.T) {
	s := newIntervalSet([][2]int64{{10, 20}, {0, 100}, {200, 300}})
	cases := []struct {
		start, end int64
		want       bool
	}{
		{100, 200, false}, // touches both neighbours only at the boundaries
		{150, 160, false},
		{50, 60, true}, // inside the long interval
		{250, 400, true},
		{300, 300, false},
		{250, 250, true}, // zero-length inside an interval
	}
	for _, c := range cases {
		if got := s.overlaps(c.start, c.end); got != c.want {
			t.Errorf("overlaps(%d, %d) = %v, want %v", c.start, c.end, got, c.want)
		}
	}
}
