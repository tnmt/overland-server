package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrNoVisits is returned when a place is registered by Google place ID
// without coordinates and no imported visit tells where that place is.
var ErrNoVisits = errors.New("no imported visits for this Google place ID")

type PlaceSummary struct {
	Place
	// Visits counts top-level imported visits with the place's Google place ID.
	Visits int
}

type UnnamedPlace struct {
	GooglePlaceID string
	SemanticType  string
	Visits        int
	Hours         float64
	LastVisit     time.Time
	Latitude      float64
	Longitude     float64
}

func (s *Store) ListPlaces(ctx context.Context) ([]PlaceSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT p.id, p.name, p.latitude, p.longitude, p.radius_meters, coalesce(p.google_place_id, ''),
	(SELECT count(*) FROM visits v WHERE v.hierarchy_level = 0 AND v.google_place_id = p.google_place_id)
FROM places p ORDER BY p.name, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PlaceSummary
	for rows.Next() {
		var p PlaceSummary
		if err := rows.Scan(&p.ID, &p.Name, &p.Latitude, &p.Longitude, &p.RadiusMeters, &p.GooglePlaceID, &p.Visits); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetPlace(ctx context.Context, id int64) (Place, bool, error) {
	var p Place
	err := s.db.QueryRowContext(ctx, `
SELECT id, name, latitude, longitude, radius_meters, coalesce(google_place_id, '')
FROM places WHERE id = ?`, id).Scan(&p.ID, &p.Name, &p.Latitude, &p.Longitude, &p.RadiusMeters, &p.GooglePlaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
}

// VisitCentroid returns the mean position of top-level imported visits with
// the given Google place ID.
func (s *Store) VisitCentroid(ctx context.Context, googlePlaceID string) (lat, lon float64, ok bool, err error) {
	var la, lo sql.NullFloat64
	err = s.db.QueryRowContext(ctx, `
SELECT avg(latitude), avg(longitude) FROM visits
WHERE hierarchy_level = 0 AND google_place_id = ?`, googlePlaceID).Scan(&la, &lo)
	if err != nil || !la.Valid {
		return 0, 0, false, err
	}
	return la.Float64, lo.Float64, true, nil
}

// SavePlace registers p. A place with a Google place ID replaces the existing
// entry for that ID, so naming a place again corrects it; a place without one
// is always added. Coordinates of a Google place left at zero are filled in
// from its imported visits. created reports whether a new row was inserted.
func (s *Store) SavePlace(ctx context.Context, p Place) (saved Place, created bool, err error) {
	if p.GooglePlaceID != "" && p.Latitude == 0 && p.Longitude == 0 {
		lat, lon, ok, err := s.VisitCentroid(ctx, p.GooglePlaceID)
		if err != nil {
			return p, false, err
		}
		if !ok {
			return p, false, ErrNoVisits
		}
		p.Latitude, p.Longitude = lat, lon
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return p, false, err
	}
	defer tx.Rollback()

	if p.GooglePlaceID != "" {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM places WHERE google_place_id = ?`, p.GooglePlaceID).Scan(&id)
		switch {
		case err == nil:
			p.ID = id
			if _, err := tx.ExecContext(ctx, `
UPDATE places SET name = ?, latitude = ?, longitude = ?, radius_meters = ? WHERE id = ?`,
				p.Name, p.Latitude, p.Longitude, p.RadiusMeters, id); err != nil {
				return p, false, err
			}
			return p, false, tx.Commit()
		case !errors.Is(err, sql.ErrNoRows):
			return p, false, err
		}
	}

	var gid any
	if p.GooglePlaceID != "" {
		gid = p.GooglePlaceID
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO places (name, latitude, longitude, radius_meters, google_place_id) VALUES (?, ?, ?, ?, ?)`,
		p.Name, p.Latitude, p.Longitude, p.RadiusMeters, gid)
	if err != nil {
		return p, false, err
	}
	if p.ID, err = res.LastInsertId(); err != nil {
		return p, false, err
	}
	return p, true, tx.Commit()
}

// UpdatePlace changes the name and/or radius of a place. found is false when
// no place has that ID.
func (s *Store) UpdatePlace(ctx context.Context, id int64, name *string, radius *float64) (p Place, found bool, err error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE places SET name = coalesce(?, name), radius_meters = coalesce(?, radius_meters) WHERE id = ?`,
		name, radius, id)
	if err != nil {
		return p, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return p, false, nil
	}
	return s.GetPlace(ctx, id)
}

func (s *Store) DeletePlace(ctx context.Context, id int64) (found bool, err error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM places WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UnnamedPlaces lists Google place IDs of imported visits that no place
// carries yet, most visited first.
func (s *Store) UnnamedPlaces(ctx context.Context, limit int) ([]UnnamedPlace, error) {
	rows, err := s.db.QueryContext(ctx, `
WITH v AS (
	SELECT google_place_id, semantic_type, start_at, end_at, latitude, longitude
	FROM visits
	WHERE hierarchy_level = 0 AND google_place_id <> ''
	  AND google_place_id NOT IN (SELECT google_place_id FROM places WHERE google_place_id IS NOT NULL)
), types AS (
	SELECT google_place_id, semantic_type,
		row_number() OVER (PARTITION BY google_place_id ORDER BY count(*) DESC, semantic_type) AS rn
	FROM v GROUP BY google_place_id, semantic_type
)
SELECT v.google_place_id, t.semantic_type, count(*), sum(v.end_at - v.start_at) / 3.6e12,
	max(v.start_at), avg(v.latitude), avg(v.longitude)
FROM v JOIN types t ON t.google_place_id = v.google_place_id AND t.rn = 1
GROUP BY v.google_place_id
ORDER BY count(*) DESC, max(v.start_at) DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UnnamedPlace
	for rows.Next() {
		var u UnnamedPlace
		var last int64
		if err := rows.Scan(&u.GooglePlaceID, &u.SemanticType, &u.Visits, &u.Hours, &last, &u.Latitude, &u.Longitude); err != nil {
			return nil, err
		}
		u.LastVisit = time.Unix(0, last)
		out = append(out, u)
	}
	return out, rows.Err()
}
