// Package stays detects periods spent at one place from a time-ordered series
// of location points.
package stays

import (
	"math"
	"time"
)

type Point struct {
	Time      time.Time
	Latitude  float64
	Longitude float64
}

type Stay struct {
	Start      time.Time
	End        time.Time
	Latitude   float64
	Longitude  float64
	PointCount int
}

type Params struct {
	// Radius is how far a point may be from the running centroid and still
	// belong to the stay.
	Radius float64
	// MinDuration is the shortest span reported as a stay.
	MinDuration time.Duration
	// MaxGap bridges missing data: a phone at rest often records nothing for
	// a while, and that silence should not split the stay.
	MaxGap time.Duration
}

var DefaultParams = Params{
	Radius:      100,
	MinDuration: 10 * time.Minute,
	MaxGap:      2 * time.Hour,
}

// Detect returns stays in chronological order. points must be sorted by Time.
func Detect(points []Point, p Params) []Stay {
	var out []Stay
	for i := 0; i < len(points); {
		lat, lon := points[i].Latitude, points[i].Longitude
		j := i + 1
		for ; j < len(points); j++ {
			if points[j].Time.Sub(points[j-1].Time) > p.MaxGap {
				break
			}
			if Distance(lat, lon, points[j].Latitude, points[j].Longitude) > p.Radius {
				break
			}
			n := float64(j - i)
			lat = (lat*n + points[j].Latitude) / (n + 1)
			lon = (lon*n + points[j].Longitude) / (n + 1)
		}

		if points[j-1].Time.Sub(points[i].Time) >= p.MinDuration {
			out = append(out, Stay{
				Start:      points[i].Time,
				End:        points[j-1].Time,
				Latitude:   lat,
				Longitude:  lon,
				PointCount: j - i,
			})
			i = j
			continue
		}
		i++
	}
	return mergeAdjacent(out, p)
}

// mergeAdjacent joins consecutive stays at the same place that a single noisy
// fix split in two.
func mergeAdjacent(in []Stay, p Params) []Stay {
	if len(in) == 0 {
		return in
	}
	out := []Stay{in[0]}
	for _, s := range in[1:] {
		last := &out[len(out)-1]
		if s.Start.Sub(last.End) <= p.MaxGap &&
			Distance(last.Latitude, last.Longitude, s.Latitude, s.Longitude) <= p.Radius {
			total := float64(last.PointCount + s.PointCount)
			last.Latitude = (last.Latitude*float64(last.PointCount) + s.Latitude*float64(s.PointCount)) / total
			last.Longitude = (last.Longitude*float64(last.PointCount) + s.Longitude*float64(s.PointCount)) / total
			last.PointCount += s.PointCount
			last.End = s.End
			continue
		}
		out = append(out, s)
	}
	return out
}

const earthRadius = 6371000.0

// Distance returns the great-circle distance in meters.
func Distance(lat1, lon1, lat2, lon2 float64) float64 {
	toRad := math.Pi / 180
	dLat := (lat2 - lat1) * toRad
	dLon := (lon2 - lon1) * toRad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*toRad)*math.Cos(lat2*toRad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadius * math.Asin(math.Sqrt(a))
}
