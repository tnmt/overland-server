// Package overland decodes batches sent by the Overland iOS app.
//
// Protocol reference: https://github.com/aaronpk/Overland-iOS#api
package overland

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Batch struct {
	Locations []Feature `json:"locations"`
}

type Feature struct {
	Type       string          `json:"type"`
	Geometry   Geometry        `json:"geometry"`
	Properties json.RawMessage `json:"properties"`
}

type Geometry struct {
	Type        string    `json:"type"`
	Coordinates []float64 `json:"coordinates"`
}

type Properties struct {
	Timestamp          string   `json:"timestamp"`
	DeviceID           string   `json:"device_id"`
	Altitude           *float64 `json:"altitude"`
	Speed              *float64 `json:"speed"`
	HorizontalAccuracy *float64 `json:"horizontal_accuracy"`
	VerticalAccuracy   *float64 `json:"vertical_accuracy"`
	Motion             []string `json:"motion"`
	BatteryLevel       *float64 `json:"battery_level"`
	BatteryState       string   `json:"battery_state"`
	Wifi               string   `json:"wifi"`
}

type Location struct {
	DeviceID           string
	RecordedAt         time.Time
	Latitude           float64
	Longitude          float64
	Altitude           *float64
	Speed              *float64
	HorizontalAccuracy *float64
	VerticalAccuracy   *float64
	Motion             []string
	BatteryLevel       *float64
	BatteryState       string
	Wifi               string
	RawProperties      json.RawMessage
}

// Overland emits ISO 8601 with a colon-less zone offset ("-0700"), which
// time.RFC3339 rejects, so both spellings are accepted.
var timestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999Z0700",
}

func parseTimestamp(s string) (time.Time, error) {
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp %q", s)
}

func (f Feature) ToLocation() (Location, error) {
	if f.Geometry.Type != "Point" {
		return Location{}, fmt.Errorf("unsupported geometry type %q", f.Geometry.Type)
	}
	if len(f.Geometry.Coordinates) < 2 {
		return Location{}, errors.New("point has fewer than two coordinates")
	}
	lon, lat := f.Geometry.Coordinates[0], f.Geometry.Coordinates[1]
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return Location{}, fmt.Errorf("coordinates out of range: [%v, %v]", lon, lat)
	}

	var p Properties
	if err := json.Unmarshal(f.Properties, &p); err != nil {
		return Location{}, fmt.Errorf("decode properties: %w", err)
	}
	ts, err := parseTimestamp(p.Timestamp)
	if err != nil {
		return Location{}, err
	}

	return Location{
		DeviceID:           p.DeviceID,
		RecordedAt:         ts,
		Latitude:           lat,
		Longitude:          lon,
		Altitude:           p.Altitude,
		Speed:              p.Speed,
		HorizontalAccuracy: p.HorizontalAccuracy,
		VerticalAccuracy:   p.VerticalAccuracy,
		Motion:             p.Motion,
		BatteryLevel:       p.BatteryLevel,
		BatteryState:       p.BatteryState,
		Wifi:               p.Wifi,
		RawProperties:      f.Properties,
	}, nil
}
