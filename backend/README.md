# Backend

One Go module contains API/controller, mTLS Node Agent and GStreamer worker.
The API opens `SQLITE_PATH` and transactionally applies checksummed migrations from
`SQLITE_MIGRATIONS_DIR` (default `sqlite-migrations`). One control process owns the
private database; WAL readers and IMMEDIATE transactions serialize state mutations.
`cmd/migrate` is an offline maintenance command; stop the API before using it.

`backend/migrations` preserves PostgreSQL history, without a PostgreSQL runtime.
`backend/sqlite-migrations` defines the active schema. Do not alter applied migrations.

Run `make check race` from the repository root, or `go -C backend test ./...`.
Docker build context is this directory:

```sh
docker build --target control-api -t streamtool-relay-api:local backend
```

See [self-host deployment](../docs/SELFHOST.md) and [API contract](../contracts/user-api.md).
