package overland

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseTimestamp(t *testing.T) {
	want := time.Date(2026, 9, 29, 1, 2, 3, 0, time.UTC)
	for _, s := range []string{
		"2026-09-29T10:02:03+09:00",
		"2026-09-29T10:02:03+0900",
		"2026-09-29T01:02:03Z",
	} {
		got, err := parseTimestamp(s)
		if err != nil {
			t.Fatalf("parseTimestamp(%q): %v", s, err)
		}
		if !got.Equal(want) {
			t.Errorf("parseTimestamp(%q) = %v, want %v", s, got, want)
		}
	}
	if _, err := parseTimestamp("yesterday"); err == nil {
		t.Error("expected error for garbage timestamp")
	}
}

func TestToLocation(t *testing.T) {
	const body = `{
		"type": "Feature",
		"geometry": {"type": "Point", "coordinates": [139.7671, 35.6812]},
		"properties": {
			"timestamp": "2026-09-29T10:02:03+0900",
			"device_id": "phone",
			"altitude": 40,
			"horizontal_accuracy": 5,
			"motion": ["walking"],
			"battery_level": 0.8,
			"wifi": ""
		}
	}`
	var f Feature
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		t.Fatal(err)
	}
	loc, err := f.ToLocation()
	if err != nil {
		t.Fatal(err)
	}
	if loc.Latitude != 35.6812 || loc.Longitude != 139.7671 {
		t.Errorf("coordinates swapped: lat=%v lon=%v", loc.Latitude, loc.Longitude)
	}
	if loc.DeviceID != "phone" || loc.Altitude == nil || *loc.Altitude != 40 || loc.Speed != nil {
		t.Errorf("unexpected properties: %+v", loc)
	}
}

func TestToLocationRejects(t *testing.T) {
	cases := map[string]Feature{
		"linestring":   {Geometry: Geometry{Type: "LineString", Coordinates: []float64{1, 2}}, Properties: json.RawMessage(`{"timestamp":"2026-09-29T01:02:03Z"}`)},
		"short point":  {Geometry: Geometry{Type: "Point", Coordinates: []float64{1}}, Properties: json.RawMessage(`{"timestamp":"2026-09-29T01:02:03Z"}`)},
		"out of range": {Geometry: Geometry{Type: "Point", Coordinates: []float64{35, 139}}, Properties: json.RawMessage(`{"timestamp":"2026-09-29T01:02:03Z"}`)},
		"no timestamp": {Geometry: Geometry{Type: "Point", Coordinates: []float64{139, 35}}, Properties: json.RawMessage(`{}`)},
	}
	for name, f := range cases {
		if _, err := f.ToLocation(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
