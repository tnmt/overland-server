package store

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"time"

	"github.com/tnmt/overland-server/internal/stays"
	"github.com/tnmt/overland-server/internal/timeline"
)

type Place struct {
	ID            int64
	Name          string
	Latitude      float64
	Longitude     float64
	RadiusMeters  float64
	GooglePlaceID string
}

type ImportResult struct {
	Visits     int
	Activities int
	// Overlapping counts segments dropped because an earlier import already
	// covers that time.
	Overlapping int
}

// InsertTimeline stores an export's visits and activities. When several
// exports (e.g. from different Google accounts on the same phone) describe the
// same time, the one imported first wins: a segment overlapping already stored
// segments is skipped as a whole, so each moment has a single story while gaps
// in one export are still filled by another.
func (s *Store) InsertTimeline(ctx context.Context, exp timeline.Export, source string) (ImportResult, error) {
	var res ImportResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	// Top-level visits and activities partition time, so they are checked
	// against each other; parent visits only against other parents.
	segments, err := loadIntervals(ctx, tx, `
SELECT start_at, end_at FROM visits WHERE hierarchy_level = 0
UNION ALL
SELECT start_at, end_at FROM activities`)
	if err != nil {
		return res, err
	}
	parents, err := loadIntervals(ctx, tx, `SELECT start_at, end_at FROM visits WHERE hierarchy_level > 0`)
	if err != nil {
		return res, err
	}

	vstmt, err := tx.PrepareContext(ctx, `
INSERT OR IGNORE INTO visits (
	source, start_at, end_at, latitude, longitude, google_place_id,
	semantic_type, hierarchy_level, probability
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return res, err
	}
	defer vstmt.Close()
	for _, v := range exp.Visits {
		existing := segments
		if v.HierarchyLevel > 0 {
			existing = parents
		}
		if existing.overlaps(v.Start.UnixNano(), v.End.UnixNano()) {
			res.Overlapping++
			continue
		}
		r, err := vstmt.ExecContext(ctx, source, v.Start.UnixNano(), v.End.UnixNano(),
			v.Latitude, v.Longitude, v.PlaceID, v.SemanticType, v.HierarchyLevel, v.Probability)
		if err != nil {
			return res, err
		}
		n, _ := r.RowsAffected()
		res.Visits += int(n)
	}

	astmt, err := tx.PrepareContext(ctx, `
INSERT OR IGNORE INTO activities (
	source, start_at, end_at, start_latitude, start_longitude,
	end_latitude, end_longitude, mode, distance_meters
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return res, err
	}
	defer astmt.Close()
	for _, a := range exp.Activities {
		if segments.overlaps(a.Start.UnixNano(), a.End.UnixNano()) {
			res.Overlapping++
			continue
		}
		r, err := astmt.ExecContext(ctx, source, a.Start.UnixNano(), a.End.UnixNano(),
			a.StartLatitude, a.StartLongitude, a.EndLatitude, a.EndLongitude, a.Mode, a.DistanceMeters)
		if err != nil {
			return res, err
		}
		n, _ := r.RowsAffected()
		res.Activities += int(n)
	}

	return res, tx.Commit()
}

func loadIntervals(ctx context.Context, tx *sql.Tx, query string) (intervalSet, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return intervalSet{}, err
	}
	defer rows.Close()
	var iv [][2]int64
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return intervalSet{}, err
		}
		iv = append(iv, [2]int64{a, b})
	}
	if err := rows.Err(); err != nil {
		return intervalSet{}, err
	}
	return newIntervalSet(iv), nil
}

// intervalSet answers "does [start, end) overlap any stored interval" in
// O(log n) using start-sorted intervals and a running maximum of their ends.
type intervalSet struct {
	starts       []int64
	prefixMaxEnd []int64
}

func newIntervalSet(iv [][2]int64) intervalSet {
	sort.Slice(iv, func(i, j int) bool { return iv[i][0] < iv[j][0] })
	s := intervalSet{starts: make([]int64, len(iv)), prefixMaxEnd: make([]int64, len(iv))}
	for i, v := range iv {
		s.starts[i] = v[0]
		s.prefixMaxEnd[i] = v[1]
		if i > 0 && s.prefixMaxEnd[i-1] > v[1] {
			s.prefixMaxEnd[i] = s.prefixMaxEnd[i-1]
		}
	}
	return s
}

func (s intervalSet) overlaps(start, end int64) bool {
	k := sort.Search(len(s.starts), func(i int) bool { return s.starts[i] >= end })
	return k > 0 && s.prefixMaxEnd[k-1] > start
}

// Visits returns visits at the given hierarchy level that overlap [from, to).
func (s *Store) Visits(ctx context.Context, from, to time.Time, level int) ([]timeline.Visit, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT start_at, end_at, latitude, longitude, google_place_id, semantic_type, hierarchy_level, probability
FROM visits
WHERE end_at > ? AND start_at < ? AND hierarchy_level = ?
ORDER BY start_at`, from.UnixNano(), to.UnixNano(), level)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []timeline.Visit
	for rows.Next() {
		var v timeline.Visit
		var start, end int64
		if err := rows.Scan(&start, &end, &v.Latitude, &v.Longitude, &v.PlaceID,
			&v.SemanticType, &v.HierarchyLevel, &v.Probability); err != nil {
			return nil, err
		}
		v.Start, v.End = time.Unix(0, start), time.Unix(0, end)
		out = append(out, v)
	}
	return out, rows.Err()
}

// Activities returns journeys that overlap [from, to).
func (s *Store) Activities(ctx context.Context, from, to time.Time) ([]timeline.Activity, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT start_at, end_at, start_latitude, start_longitude, end_latitude, end_longitude, mode, distance_meters
FROM activities
WHERE end_at > ? AND start_at < ?
ORDER BY start_at`, from.UnixNano(), to.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []timeline.Activity
	for rows.Next() {
		var a timeline.Activity
		var start, end int64
		if err := rows.Scan(&start, &end, &a.StartLatitude, &a.StartLongitude,
			&a.EndLatitude, &a.EndLongitude, &a.Mode, &a.DistanceMeters); err != nil {
			return nil, err
		}
		a.Start, a.End = time.Unix(0, start), time.Unix(0, end)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Points returns recorded locations in [from, to) from all devices, oldest
// first. Points whose reported accuracy is zero (not a real estimate) or
// worse than maxAccuracy are left out; points without any accuracy are kept.
func (s *Store) Points(ctx context.Context, from, to time.Time, maxAccuracy float64) ([]stays.Point, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT recorded_at, latitude, longitude
FROM locations
WHERE recorded_at >= ? AND recorded_at < ?
  AND (horizontal_accuracy IS NULL OR (horizontal_accuracy > 0 AND horizontal_accuracy <= ?))
ORDER BY recorded_at`, from.UnixNano(), to.UnixNano(), maxAccuracy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []stays.Point
	for rows.Next() {
		var p stays.Point
		var ns int64
		if err := rows.Scan(&ns, &p.Latitude, &p.Longitude); err != nil {
			return nil, err
		}
		p.Time = time.Unix(0, ns)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Places(ctx context.Context) ([]Place, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, latitude, longitude, radius_meters, coalesce(google_place_id, '')
FROM places ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Place
	for rows.Next() {
		var p Place
		if err := rows.Scan(&p.ID, &p.Name, &p.Latitude, &p.Longitude, &p.RadiusMeters, &p.GooglePlaceID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// NearestVisit finds the closest top-level imported visit within radius
// meters, so that stays detected from recorded points can reuse the Google
// place ID of a place visited before. ok is false when there is none.
func (s *Store) NearestVisit(ctx context.Context, lat, lon, radius float64) (v timeline.Visit, ok bool, err error) {
	dLat := radius / 111320
	dLon := radius / (111320 * math.Max(math.Cos(lat*math.Pi/180), 0.01))
	rows, err := s.db.QueryContext(ctx, `
SELECT latitude, longitude, google_place_id, semantic_type
FROM visits
WHERE hierarchy_level = 0
  AND latitude BETWEEN ? AND ? AND longitude BETWEEN ? AND ?`,
		lat-dLat, lat+dLat, lon-dLon, lon+dLon)
	if err != nil {
		return v, false, err
	}
	defer rows.Close()

	best := math.Inf(1)
	for rows.Next() {
		var c timeline.Visit
		if err := rows.Scan(&c.Latitude, &c.Longitude, &c.PlaceID, &c.SemanticType); err != nil {
			return v, false, err
		}
		if d := stays.Distance(lat, lon, c.Latitude, c.Longitude); d <= radius && d < best {
			best, v, ok = d, c, true
		}
	}
	if err := rows.Err(); err != nil {
		return v, false, err
	}
	return v, ok, nil
}
