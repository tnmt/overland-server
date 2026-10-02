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
	TimelineCoverageEnd(ctx context.Context) (time.Time, bool, error)
	Visits(ctx context.Context, from, to time.Time, level int) ([]timeline.Visit, error)
	Activities(ctx context.Context, from, to time.Time) ([]timeline.Activity, error)
	Points(ctx context.Context, from, to time.Time, maxAccuracy float64) ([]stays.Point, error)
	Places(ctx context.Context) ([]store.Place, error)
	NearestVisit(ctx context.Context, lat, lon, radius float64) (timeline.Visit, bool, error)
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

// Build assembles the day containing date in loc. Imported Google Timeline
// segments are authoritative up to the end of the last import; after that,
// stays are detected from recorded points.
func Build(ctx context.Context, src Source, date time.Time, loc *time.Location) (Day, error) {
	y, m, d := date.Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, loc)
	dayEnd := dayStart.AddDate(0, 0, 1)

	day := Day{Date: dayStart.Format(time.DateOnly), Timezone: loc.String(), Stays: []Stay{}, Moves: []Move{}}

	places, err := src.Places(ctx)
	if err != nil {
		return day, err
	}
	coverageEnd, imported, err := src.TimelineCoverageEnd(ctx)
	if err != nil {
		return day, err
	}

	if imported && coverageEnd.After(dayStart) {
		visits, err := src.Visits(ctx, dayStart, minTime(dayEnd, coverageEnd), 0)
		if err != nil {
			return day, err
		}
		for _, v := range visits {
			day.Stays = append(day.Stays, Stay{
				Start: v.Start, End: v.End, Latitude: v.Latitude, Longitude: v.Longitude,
				Source: SourceTimeline, GooglePlaceID: v.PlaceID, SemanticType: v.SemanticType,
			})
		}
		activities, err := src.Activities(ctx, dayStart, minTime(dayEnd, coverageEnd))
		if err != nil {
			return day, err
		}
		for _, a := range activities {
			day.Moves = append(day.Moves, Move{
				Start: a.Start, End: a.End, Mode: a.Mode, DistanceMeters: a.DistanceMeters, Source: SourceTimeline,
			})
		}
	}

	if !imported || coverageEnd.Before(dayEnd) {
		from := dayStart
		if imported && coverageEnd.After(from) {
			from = coverageEnd
		}
		pointsFrom := from.Add(-lookback)
		if imported && coverageEnd.After(pointsFrom) {
			pointsFrom = coverageEnd
		}
		points, err := src.Points(ctx, pointsFrom, dayEnd, maxAccuracy)
		if err != nil {
			return day, err
		}
		for _, st := range stays.Detect(points, stays.DefaultParams) {
			if !st.End.After(from) {
				continue
			}
			s := Stay{
				Start: st.Start, End: st.End, Latitude: st.Latitude, Longitude: st.Longitude,
				Source: SourceRecorded,
			}
			v, ok, err := src.NearestVisit(ctx, st.Latitude, st.Longitude, visitMatchRadius)
			if err != nil {
				return day, err
			}
			if ok {
				s.GooglePlaceID, s.SemanticType = v.PlaceID, v.SemanticType
			}
			day.Stays = append(day.Stays, s)
		}
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

func matchPlace(s Stay, places []store.Place) *PlaceRef {
	if s.GooglePlaceID != "" {
		for _, p := range places {
			if p.GooglePlaceID == s.GooglePlaceID {
				return &PlaceRef{ID: p.ID, Name: p.Name}
			}
		}
	}
	var best *PlaceRef
	bestDist := math.Inf(1)
	for _, p := range places {
		d := stays.Distance(s.Latitude, s.Longitude, p.Latitude, p.Longitude)
		if d <= p.RadiusMeters && d < bestDist {
			best, bestDist = &PlaceRef{ID: p.ID, Name: p.Name}, d
		}
	}
	return best
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
