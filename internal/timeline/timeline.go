// Package timeline reads the Google Maps Timeline export produced on-device
// (Settings > Location > Timeline > Export), i.e. a JSON document with a
// top-level "semanticSegments" array.
package timeline

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

type Visit struct {
	Start          time.Time
	End            time.Time
	Latitude       float64
	Longitude      float64
	PlaceID        string
	SemanticType   string
	HierarchyLevel int
	Probability    float64
}

type Activity struct {
	Start          time.Time
	End            time.Time
	StartLatitude  float64
	StartLongitude float64
	EndLatitude    float64
	EndLongitude   float64
	Mode           string
	DistanceMeters float64
}

type Export struct {
	Visits     []Visit
	Activities []Activity
}

type rawExport struct {
	SemanticSegments []rawSegment `json:"semanticSegments"`
}

type rawSegment struct {
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
	Visit     *struct {
		HierarchyLevel int     `json:"hierarchyLevel"`
		Probability    float64 `json:"probability"`
		TopCandidate   struct {
			PlaceID       string `json:"placeId"`
			SemanticType  string `json:"semanticType"`
			PlaceLocation struct {
				LatLng string `json:"latLng"`
			} `json:"placeLocation"`
		} `json:"topCandidate"`
	} `json:"visit"`
	Activity *struct {
		Start struct {
			LatLng string `json:"latLng"`
		} `json:"start"`
		End struct {
			LatLng string `json:"latLng"`
		} `json:"end"`
		DistanceMeters float64 `json:"distanceMeters"`
		TopCandidate   struct {
			Type string `json:"type"`
		} `json:"topCandidate"`
	} `json:"activity"`
}

// Parse decodes an export. Segments other than visits and activities
// (timelinePath, timelineMemory) are ignored: raw paths are already covered by
// point data, and memories carry no time range of their own.
func Parse(r io.Reader) (Export, error) {
	var raw rawExport
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return Export{}, fmt.Errorf("decode export: %w", err)
	}

	var out Export
	for i, seg := range raw.SemanticSegments {
		if seg.Visit == nil && seg.Activity == nil {
			continue
		}
		start, err := time.Parse(time.RFC3339Nano, seg.StartTime)
		if err != nil {
			return Export{}, fmt.Errorf("segment %d: start time: %w", i, err)
		}
		end, err := time.Parse(time.RFC3339Nano, seg.EndTime)
		if err != nil {
			return Export{}, fmt.Errorf("segment %d: end time: %w", i, err)
		}

		switch {
		case seg.Visit != nil:
			c := seg.Visit.TopCandidate
			lat, lon, err := ParseLatLng(c.PlaceLocation.LatLng)
			if err != nil {
				return Export{}, fmt.Errorf("segment %d: %w", i, err)
			}
			out.Visits = append(out.Visits, Visit{
				Start:          start,
				End:            end,
				Latitude:       lat,
				Longitude:      lon,
				PlaceID:        c.PlaceID,
				SemanticType:   c.SemanticType,
				HierarchyLevel: seg.Visit.HierarchyLevel,
				Probability:    seg.Visit.Probability,
			})
		case seg.Activity != nil:
			a := seg.Activity
			slat, slon, err := ParseLatLng(a.Start.LatLng)
			if err != nil {
				return Export{}, fmt.Errorf("segment %d: activity start: %w", i, err)
			}
			elat, elon, err := ParseLatLng(a.End.LatLng)
			if err != nil {
				return Export{}, fmt.Errorf("segment %d: activity end: %w", i, err)
			}
			out.Activities = append(out.Activities, Activity{
				Start:          start,
				End:            end,
				StartLatitude:  slat,
				StartLongitude: slon,
				EndLatitude:    elat,
				EndLongitude:   elon,
				Mode:           a.TopCandidate.Type,
				DistanceMeters: a.DistanceMeters,
			})
		}
	}
	return out, nil
}

// ParseLatLng parses the export's coordinate notation, e.g.
// "35.6812°, 139.7671°".
func ParseLatLng(s string) (lat, lon float64, err error) {
	latStr, lonStr, ok := strings.Cut(s, ",")
	if !ok {
		return 0, 0, fmt.Errorf("invalid latLng %q", s)
	}
	parse := func(v string) (float64, error) {
		return strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "°"), 64)
	}
	if lat, err = parse(latStr); err != nil {
		return 0, 0, fmt.Errorf("invalid latitude in %q", s)
	}
	if lon, err = parse(lonStr); err != nil {
		return 0, 0, fmt.Errorf("invalid longitude in %q", s)
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, fmt.Errorf("latLng out of range: %q", s)
	}
	return lat, lon, nil
}
