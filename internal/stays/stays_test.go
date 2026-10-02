package stays

import (
	"testing"
	"time"
)

var base = time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)

func at(minute int, lat, lon float64) Point {
	return Point{Time: base.Add(time.Duration(minute) * time.Minute), Latitude: lat, Longitude: lon}
}

const (
	homeLat, homeLon     = 35.0000, 139.0000
	officeLat, officeLon = 35.0500, 139.0500 // ~7 km away
	deg10m               = 0.00009           // ~10 m of latitude
)

func TestDetectTwoStaysWithTravel(t *testing.T) {
	var pts []Point
	for m := 0; m <= 30; m++ {
		pts = append(pts, at(m, homeLat+float64(m%3)*deg10m, homeLon))
	}
	for m := 31; m < 50; m++ {
		f := float64(m-30) / 20
		pts = append(pts, at(m, homeLat+(officeLat-homeLat)*f, homeLon+(officeLon-homeLon)*f))
	}
	for m := 50; m <= 120; m++ {
		pts = append(pts, at(m, officeLat, officeLon-float64(m%2)*deg10m))
	}

	got := Detect(pts, DefaultParams)
	if len(got) != 2 {
		t.Fatalf("got %d stays, want 2: %+v", len(got), got)
	}
	if got[0].Start != base || got[0].End != base.Add(30*time.Minute) {
		t.Errorf("home stay: %v - %v", got[0].Start, got[0].End)
	}
	if got[1].End != base.Add(120*time.Minute) {
		t.Errorf("office stay end: %v", got[1].End)
	}
	if d := Distance(got[1].Latitude, got[1].Longitude, officeLat, officeLon); d > 20 {
		t.Errorf("office centroid off by %.0f m", d)
	}
}

func TestDetectBridgesSilenceButNotLongGaps(t *testing.T) {
	pts := []Point{
		at(0, homeLat, homeLon),
		at(5, homeLat, homeLon),
		at(95, homeLat, homeLon), // 90 min of silence: same stay
		at(100, homeLat, homeLon),
		at(300, homeLat, homeLon), // 200 min gap: new stay
		at(320, homeLat, homeLon),
	}
	got := Detect(pts, DefaultParams)
	if len(got) != 2 {
		t.Fatalf("got %d stays, want 2: %+v", len(got), got)
	}
	if got[0].End != base.Add(100*time.Minute) || got[1].Start != base.Add(300*time.Minute) {
		t.Errorf("unexpected boundaries: %+v", got)
	}
}

func TestDetectIgnoresShortStopsAndOutliers(t *testing.T) {
	var pts []Point
	for m := 0; m < 5; m++ { // 4-minute stop: too short
		pts = append(pts, at(m, officeLat, officeLon))
	}
	for m := 10; m <= 60; m++ {
		lat := homeLat
		if m == 30 {
			lat = homeLat + 30*deg10m // single 300 m jump
		}
		pts = append(pts, at(m, lat, homeLon))
	}
	got := Detect(pts, DefaultParams)
	if len(got) != 1 {
		t.Fatalf("got %d stays, want 1 (outlier merged): %+v", len(got), got)
	}
	if got[0].Start != base.Add(10*time.Minute) || got[0].End != base.Add(60*time.Minute) {
		t.Errorf("unexpected stay: %+v", got[0])
	}
}

func TestDistance(t *testing.T) {
	// One degree of latitude is ~111.2 km.
	if d := Distance(35, 139, 36, 139); d < 111000 || d > 111400 {
		t.Errorf("Distance = %.0f", d)
	}
}
