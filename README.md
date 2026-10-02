# overland-server

A small self-hosted receiver for [Overland](https://overland.p3k.app/)-format
location uploads (the Overland iOS app, Colota or GPSLogger on Android). It
stores every point in SQLite, imports past Google Maps Timeline exports, and
answers "where was I on this date" for tools such as an Obsidian Daily Note
side panel.

## API

### `POST /api/overland`

The endpoint to configure as Overland's *Receiver Endpoint*:

```
https://example.com/api/overland?access_token=<token>
```

`Authorization: Bearer <token>` is accepted as well.

- The body is Overland's `{"locations": [GeoJSON Point Feature, ...]}` batch.
  `current` and `trip` are ignored.
- On success the server responds `{"result":"ok"}`, which is what makes Overland
  drop the batch from its on-device queue. Any other response makes it retry.
- `device_id` is read from each feature's `properties`, falling back to a
  top-level `device_id` next to `locations` (as sent by Colota).
- Points are deduplicated on `(device_id, timestamp)`, so resent batches are
  harmless.
- A body without a `locations` array is rejected with 400, so a client sending
  another format sees an error instead of silently discarding its queue.
- Individual points that cannot be parsed are logged and skipped instead of
  failing the batch, because a rejected batch would be retried forever and block
  all later uploads.

### `GET /api/days/{date}`

Enabled only when a read token is configured, and accepts that token only as
`Authorization: Bearer <token>` (never as a query parameter). `{date}` is
`YYYY-MM-DD` in the configured time zone.

```json
{
  "date": "2026-09-30",
  "timezone": "Asia/Tokyo",
  "stays": [
    {
      "start": "2026-09-29T22:00:00+09:00",
      "end": "2026-09-30T08:30:00+09:00",
      "latitude": 35.0,
      "longitude": 139.0,
      "source": "google-timeline",
      "google_place_id": "ChIJ...",
      "semantic_type": "HOME",
      "place": {"id": 1, "name": "Home"}
    }
  ],
  "moves": [
    {
      "start": "2026-09-30T08:30:00+09:00",
      "end": "2026-09-30T09:10:00+09:00",
      "mode": "IN_TRAIN",
      "distance_meters": 7000,
      "source": "google-timeline"
    }
  ]
}
```

- Stays overlapping the date are returned with their full time range, so the
  first one usually starts the previous evening.
- Up to the end of the latest Google Timeline import, stays and moves come
  from the import (`source: "google-timeline"`). After that, stays are
  detected from recorded points (`source: "recorded"`): points within 100 m
  of each other for at least 10 minutes, bridging gaps of up to 2 hours.
  Points with a reported accuracy of 0 or worse than 50 m are ignored.
  Detected stays inherit the Google place ID of a previously imported visit
  within 50 m. Moves are only available from imports.
- `place` is a user-named place (see below), or `null`.

### `GET /healthz`

Returns `{"status":"ok"}` when the database is reachable.

## Storage

- `locations`: recorded points. Frequently used properties get their own
  columns; the full original `properties` object is kept as JSON so new columns
  can be backfilled later.
- `visits`, `activities`: stays and journeys imported from Google Timeline.
- `places`: user-named places. A stay matches a place by `google_place_id`
  first, then by being within `radius_meters` of it. There is no API for
  editing them yet; insert rows with `sqlite3`.

## Importing Google Timeline

Export the timeline on the phone (Settings > Location > Timeline > Export
timeline data), which produces a JSON file with `semanticSegments`, then:

```
overland-server import-google-timeline -db overland.db first.json second.json
```

Re-importing is safe. When exports overlap in time (e.g. several Google
accounts on one phone), earlier files win: a segment that overlaps anything
already stored is skipped as a whole.

## Running

```
overland-server -listen 127.0.0.1:8080 -db overland.db \
  -ingest-token-file ingest-token.txt -read-token-file read-token.txt \
  -timezone Asia/Tokyo
```

## NixOS

```nix
{
  inputs.overland-server.url = "github:tnmt/overland-server";

  # in a NixOS configuration
  imports = [ inputs.overland-server.nixosModules.default ];
  services.overland-server = {
    enable = true;
    listenAddress = "127.0.0.1:8095";
    ingestTokenFile = "/run/secrets/overland_ingest_token";
    readTokenFile = "/run/secrets/overland_read_token"; # optional
    timezone = "Asia/Tokyo";
  };
}
```

The service runs with `DynamicUser`; the database lives in
`/var/lib/overland-server/overland.db` (`/var/lib/private/overland-server` on
disk). TLS termination is left to a reverse proxy.

## Development

```
nix develop
go test ./...
```

The SQLite driver is the pure-Go `modernc.org/sqlite`, so the build needs no C
toolchain (`CGO_ENABLED=0`).

## License

MIT
