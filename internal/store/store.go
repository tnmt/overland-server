package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tnmt/overland-server/internal/overland"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS locations (
	id                  INTEGER PRIMARY KEY,
	device_id           TEXT    NOT NULL,
	recorded_at         INTEGER NOT NULL, -- Unix nanoseconds
	latitude            REAL    NOT NULL,
	longitude           REAL    NOT NULL,
	altitude            REAL,
	speed               REAL,
	horizontal_accuracy REAL,
	vertical_accuracy   REAL,
	motion              TEXT,
	battery_level       REAL,
	battery_state       TEXT,
	wifi                TEXT,
	properties          TEXT    NOT NULL,
	received_at         INTEGER NOT NULL,
	UNIQUE (device_id, recorded_at)
);
CREATE INDEX IF NOT EXISTS locations_recorded_at ON locations (recorded_at);

-- Stays and journeys from Google Timeline exports. Times are Unix nanoseconds.
CREATE TABLE IF NOT EXISTS visits (
	id              INTEGER PRIMARY KEY,
	source          TEXT    NOT NULL,
	start_at        INTEGER NOT NULL,
	end_at          INTEGER NOT NULL,
	latitude        REAL    NOT NULL,
	longitude       REAL    NOT NULL,
	google_place_id TEXT    NOT NULL,
	semantic_type   TEXT    NOT NULL,
	hierarchy_level INTEGER NOT NULL,
	probability     REAL    NOT NULL,
	UNIQUE (start_at, end_at, google_place_id, hierarchy_level)
);
CREATE INDEX IF NOT EXISTS visits_end_at ON visits (end_at);

CREATE TABLE IF NOT EXISTS activities (
	id              INTEGER PRIMARY KEY,
	source          TEXT    NOT NULL,
	start_at        INTEGER NOT NULL,
	end_at          INTEGER NOT NULL,
	start_latitude  REAL    NOT NULL,
	start_longitude REAL    NOT NULL,
	end_latitude    REAL    NOT NULL,
	end_longitude   REAL    NOT NULL,
	mode            TEXT    NOT NULL,
	distance_meters REAL    NOT NULL,
	UNIQUE (start_at, end_at, mode)
);
CREATE INDEX IF NOT EXISTS activities_end_at ON activities (end_at);

-- User-named places. A stay matches a place by Google place ID first, then by
-- distance within radius_meters.
CREATE TABLE IF NOT EXISTS places (
	id              INTEGER PRIMARY KEY,
	name            TEXT    NOT NULL,
	latitude        REAL    NOT NULL,
	longitude       REAL    NOT NULL,
	radius_meters   REAL    NOT NULL DEFAULT 100,
	google_place_id TEXT    UNIQUE
);
`

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single writer connection avoids SQLITE_BUSY between concurrent
	// ingest requests; throughput is bounded by one phone anyway.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrateRecordedAt(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &Store{db: db}, nil
}

// migrateRecordedAt upgrades databases created before recorded_at used Unix
// seconds. The range check prevents already-nanosecond values from being
// multiplied if a database was created by a newer binary but has no version
// marker yet.
func migrateRecordedAt(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 1 {
		return nil
	}
	if _, err := db.Exec(`
UPDATE locations
SET recorded_at = recorded_at * 1000000000
WHERE recorded_at BETWEEN -4102444800 AND 4102444800`); err != nil {
		return err
	}
	_, err := db.Exec(`PRAGMA user_version = 1`)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

// InsertLocations stores the batch atomically and returns how many rows were
// new. Overland resends a whole batch when a response is lost, so points
// already stored for the same device and timestamp are ignored.
func (s *Store) InsertLocations(ctx context.Context, locs []overland.Location, receivedAt time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT OR IGNORE INTO locations (
	device_id, recorded_at, latitude, longitude, altitude, speed,
	horizontal_accuracy, vertical_accuracy, motion, battery_level,
	battery_state, wifi, properties, received_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, l := range locs {
		var motion any
		if len(l.Motion) > 0 {
			b, err := json.Marshal(l.Motion)
			if err != nil {
				return 0, err
			}
			motion = string(b)
		}
		res, err := stmt.ExecContext(ctx,
			l.DeviceID, l.RecordedAt.UnixNano(), l.Latitude, l.Longitude, l.Altitude, l.Speed,
			l.HorizontalAccuracy, l.VerticalAccuracy, motion, l.BatteryLevel,
			nullIfEmpty(l.BatteryState), nullIfEmpty(l.Wifi), string(l.RawProperties), receivedAt.Unix(),
		)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		inserted += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (s *Store) CountLocations(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM locations`).Scan(&n)
	return n, err
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
