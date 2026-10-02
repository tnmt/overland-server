package timeline

import (
	"strings"
	"testing"
	"time"
)

const sample = `{
  "semanticSegments": [
    {
      "startTime": "2026-09-29T08:00:00.000+09:00",
      "endTime": "2026-09-29T10:00:00.000+09:00",
      "timelinePath": [{"point": "35.0000000°, 139.0000000°", "time": "2026-09-29T08:30:00.000+09:00"}]
    },
    {
      "startTime": "2026-09-29T08:10:00.000+09:00",
      "endTime": "2026-09-29T09:00:00.000+09:00",
      "startTimeTimezoneUtcOffsetMinutes": 540,
      "endTimeTimezoneUtcOffsetMinutes": 540,
      "visit": {
        "hierarchyLevel": 0,
        "probability": 0.9,
        "topCandidate": {
          "placeId": "place-a",
          "semanticType": "HOME",
          "probability": 0.8,
          "placeLocation": {"latLng": "35.6812000°, 139.7671000°"}
        }
      }
    },
    {
      "startTime": "2026-09-29T09:00:00.000+09:00",
      "endTime": "2026-09-29T09:20:00.000+09:00",
      "activity": {
        "start": {"latLng": "35.6812000°, 139.7671000°"},
        "end": {"latLng": "35.6900000°, 139.7000000°"},
        "distanceMeters": 6100.0,
        "probability": 0.9,
        "topCandidate": {"type": "IN_TRAIN", "probability": 0.7}
      }
    },
    {
      "startTime": "2026-09-29T09:20:00.000+09:00",
      "endTime": "2026-09-29T09:20:00.000+09:00",
      "timelineMemory": {"trip": {"distanceFromOriginKms": 10, "destinations": []}}
    }
  ],
  "rawSignals": [],
  "userLocationProfile": {}
}`

func TestParse(t *testing.T) {
	exp, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Visits) != 1 || len(exp.Activities) != 1 {
		t.Fatalf("got %d visits, %d activities; want 1, 1", len(exp.Visits), len(exp.Activities))
	}

	v := exp.Visits[0]
	wantStart := time.Date(2026, 9, 28, 23, 10, 0, 0, time.UTC)
	if !v.Start.Equal(wantStart) || v.PlaceID != "place-a" || v.SemanticType != "HOME" ||
		v.Latitude != 35.6812 || v.Longitude != 139.7671 || v.HierarchyLevel != 0 {
		t.Errorf("unexpected visit: %+v", v)
	}

	a := exp.Activities[0]
	if a.Mode != "IN_TRAIN" || a.DistanceMeters != 6100 || a.EndLongitude != 139.7 {
		t.Errorf("unexpected activity: %+v", a)
	}
}

func TestParseLatLng(t *testing.T) {
	lat, lon, err := ParseLatLng("-33.8688°, 151.2093°")
	if err != nil || lat != -33.8688 || lon != 151.2093 {
		t.Errorf("got %v %v %v", lat, lon, err)
	}
	for _, bad := range []string{"", "35.0°", "abc°, 139°", "95°, 139°"} {
		if _, _, err := ParseLatLng(bad); err == nil {
			t.Errorf("ParseLatLng(%q): expected error", bad)
		}
	}
}
