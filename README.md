# overland-server

A small self-hosted receiver for [Overland](https://overland.p3k.app/), the iOS
background location logger. It accepts Overland's batch uploads and stores every
point in SQLite so the history can later be queried per day (for example by an
Obsidian Daily Note side panel).

## Status

Ingest only. A read API (stays / places per local date) is planned once enough
real data has accumulated to tune stay detection.

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

### `GET /healthz`

Returns `{"status":"ok"}` when the database is reachable.

## Storage

One `locations` table. Frequently used properties get their own columns; the
full original `properties` object is kept as JSON so new columns can be
backfilled later.

## Running

```
overland-server -listen 127.0.0.1:8080 -db overland.db -ingest-token-file token.txt
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
