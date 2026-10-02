// Package days assembles what happened on one local calendar date: where the
// user stayed and how they moved between places.
package days

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/tnmt/overland-server/internal/stays"
	"github.com/tnmt/overland-server/internal/store"
	"github.com/tnmt/overland-server/internal/timeline"
)

type Source interface {
	Visits(ctx context.Context, from, to time.Time, level int) ([]timeline.Visit, error)
	Activities(ctx context.Context, from, to time.Time) ([]timeline.Activity, error)
	Points(ctx context.Context, from, to time.Time, maxAccuracy float64) ([]stays.Point, error)
	Places(ctx context.Context) ([]store.Place, error)
	NearestVisit(ctx context.Context, lat, lon, radius float64) (timeline.Visit, float64, bool, error)
}

const (
	SourceTimeline = "google-timeline"
	SourceRecorded = "recorded"

	// maxAccuracy drops fixes too vague to tell neighbouring places apart.
	maxAccuracy = 50.0
	// lookback lets a stay that began the previous evening (sleeping at
	// home, say) be detected with its real start time.
	lookback = 12 * time.Hour
	// visitMatchRadius is how close a detected stay must be to a previously
	// imported visit to inherit its Google place ID.
	visitMatchRadius = 50.0
	// unnamedVisitMargin is how much closer an inherited but unnamed visit
	// must be than the nearest registered place before the stay is left
	// unnamed. It is about the median accuracy of recorded points, so that
	// two place IDs inside one large building do not count as different
	// places.
	unnamedVisitMargin = 20.0
)

type Day struct {
	Date     string `json:"date"`
	Timezone string `json:"timezone"`
	Stays    []Stay `json:"stays"`
	Moves    []Move `json:"moves"`
}

type Stay struct {
	Start         time.Time `json:"start"`
	End           time.Time `json:"end"`
	Latitude      float64   `json:"latitude"`
	Longitude     float64   `json:"longitude"`
	Source        string    `json:"source"`
	GooglePlaceID string    `json:"google_place_id,omitempty"`
	SemanticType  string    `json:"semantic_type,omitempty"`
	Place         *PlaceRef `json:"place"`

	// visitDistance is how far the imported visit whose place ID a recorded
	// stay inherited lies from it; negative when nothing was inherited.
	visitDistance float64
}

type PlaceRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Move struct {
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
	Mode           string    `json:"mode"`
	DistanceMeters float64   `json:"distance_meters"`
	Source         string    `json:"source"`
}

// Build assembles the day containing date in loc. Wherever imported Google
// Timeline segments exist they are used as is; time they leave uncovered
// (before the first import, after the latest one, or gaps in between) is
// filled with stays detected from recorded points.
func Build(ctx context.Context, src Source, date time.Time, loc *time.Location) (Day, error) {
	y, m, d := date.Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, loc)
	dayEnd := dayStart.AddDate(0, 0, 1)

	day := Day{Date: dayStart.Format(time.DateOnly), Timezone: loc.String(), Stays: []Stay{}, Moves: []Move{}}

	places, err := src.Places(ctx)
	if err != nil {
		return day, err
	}

	// Recorded points reach back before midnight so that a stay begun the
	// previous evening is detected with its real start, so the timeline
	// segments that may cover that time are needed for the same window.
	windowStart := dayStart.Add(-lookback)
	visits, err := src.Visits(ctx, windowStart, dayEnd, 0)
	if err != nil {
		return day, err
	}
	activities, err := src.Activities(ctx, windowStart, dayEnd)
	if err != nil {
		return day, err
	}

	var covered []interval
	for _, v := range visits {
		covered = append(covered, interval{v.Start, v.End})
		if v.End.After(dayStart) {
			day.Stays = append(day.Stays, Stay{
				Start: v.Start, End: v.End, Latitude: v.Latitude, Longitude: v.Longitude,
				Source: SourceTimeline, GooglePlaceID: v.PlaceID, SemanticType: v.SemanticType,
			})
		}
	}
	for _, a := range activities {
		covered = append(covered, interval{a.Start, a.End})
		if a.End.After(dayStart) {
			day.Moves = append(day.Moves, Move{
				Start: a.Start, End: a.End, Mode: a.Mode, DistanceMeters: a.DistanceMeters, Source: SourceTimeline,
			})
		}
	}

	points, err := src.Points(ctx, windowStart, dayEnd, maxAccuracy)
	if err != nil {
		return day, err
	}
	for _, st := range stays.Detect(points, stays.DefaultParams) {
		gap, ok := longestUncovered(interval{st.Start, st.End}, covered)
		if !ok || gap.end.Sub(gap.start) < stays.DefaultParams.MinDuration || !gap.end.After(dayStart) {
			continue
		}
		s := Stay{
			Start: gap.start, End: gap.end, Latitude: st.Latitude, Longitude: st.Longitude,
			Source: SourceRecorded, visitDistance: -1,
		}
		v, dist, ok, err := src.NearestVisit(ctx, st.Latitude, st.Longitude, visitMatchRadius)
		if err != nil {
			return day, err
		}
		if ok {
			s.GooglePlaceID, s.SemanticType, s.visitDistance = v.PlaceID, v.SemanticType, dist
		}
		day.Stays = append(day.Stays, s)
	}

	for i := range day.Stays {
		day.Stays[i].Place = matchPlace(day.Stays[i], places)
		day.Stays[i].Start = day.Stays[i].Start.In(loc)
		day.Stays[i].End = day.Stays[i].End.In(loc)
	}
	for i := range day.Moves {
		day.Moves[i].Start = day.Moves[i].Start.In(loc)
		day.Moves[i].End = day.Moves[i].End.In(loc)
	}
	sort.SliceStable(day.Stays, func(i, j int) bool { return day.Stays[i].Start.Before(day.Stays[j].Start) })
	return day, nil
}

// matchPlace names a stay by Google place ID first. Only stays detected from
// recorded points fall back to distance: an imported visit already says which
// place it was, and a different place ID nearby (the shop next door, another
// tenant in the same building) must not inherit a registered neighbour's name.
func matchPlace(s Stay, places []store.Place) *PlaceRef {
	if s.GooglePlaceID != "" {
		for _, p := range places {
			if p.GooglePlaceID == s.GooglePlaceID {
				return &PlaceRef{ID: p.ID, Name: p.Name}
			}
		}
	}
	if s.Source == SourceTimeline && s.GooglePlaceID != "" {
		return nil
	}
	var best *PlaceRef
	bestDist := math.Inf(1)
	for _, p := range places {
		d := stays.Distance(s.Latitude, s.Longitude, p.Latitude, p.Longitude)
		if d <= p.RadiusMeters && d < bestDist {
			best, bestDist = &PlaceRef{ID: p.ID, Name: p.Name}, d
		}
	}
	// An unnamed place visited before is clearly closer than any registered
	// one: the stay was most likely there (a shop next to a registered one),
	// so leave it unnamed rather than borrow the farther neighbour's name.
	if s.visitDistance >= 0 && s.visitDistance+unnamedVisitMargin < bestDist {
		return nil
	}
	return best
}

type interval struct{ start, end time.Time }

// longestUncovered returns the longest part of iv not overlapped by any
// interval in covered, which need not be sorted.
func longestUncovered(iv interval, covered []interval) (interval, bool) {
	sorted := append([]interval(nil), covered...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start.Before(sorted[j].start) })

	var best interval
	found := false
	consider := func(c interval) {
		if c.end.After(c.start) && (!found || c.end.Sub(c.start) > best.end.Sub(best.start)) {
			best, found = c, true
		}
	}
	cursor := iv.start
	for _, c := range sorted {
		if !c.end.After(cursor) || !c.start.Before(iv.end) {
			continue
		}
		if c.start.After(cursor) {
			consider(interval{cursor, c.start})
		}
		cursor = c.end
		if !cursor.Before(iv.end) {
			break
		}
	}
	if cursor.Before(iv.end) {
		consider(interval{cursor, iv.end})
	}
	return best, found
}
