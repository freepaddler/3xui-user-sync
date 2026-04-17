# 3xui-user-sync

Simple Go service over multiple `3x-ui` panels:

- aggregate real subscription contents from many servers
- local SQLite for users and configured servers
- one admin login for the web UI
- user matrix with per-inbound enable/disable
- UI on `HTMX + templ + Pico.css`

## Current shape

- `cmd/main.go`
- `internal/...`
- `net/http`
- SQLite auto-create on startup
- runtime remote errors do not crash the process
- graceful HTTP shutdown
- Docker `from scratch`

## Environment

- `HTTP_ADDR` default `:8080`
- `DB_PATH` default `./data/app.db`
- `LOG_LEVEL` default `info`
- `LOG_FORMAT` default `pretty`
- `BASE_PATH` optional path prefix for all routes
- `PUBLIC_SUBSCRIPTION_PATH` default `/sub/`
- `PROFILE_TITLE` default `3xui-user-sync`
- `SECURE_COOKIE` default `false`
- `REQUEST_TIMEOUT` default `15s`
- `SESSION_TTL` default `24h`
- `SESSION_IDLE_TIMEOUT` default `8h`
- `REMEMBER_ME_TTL` default `720h`
- `ADMIN_USERNAME` required
- `ADMIN_PASSWORD` required

## Notes

- Admin credentials are read from `ENV` only and are never stored in SQLite.
- Server panel passwords are stored encrypted in SQLite.
- Session cookie `Secure` attribute is controlled by `SECURE_COOKIE`.
- Each server stores a full `subscription_url`, for example `https://server.example/3x/sub/{subscription_id}`.
- Servers can be marked inactive without deleting them.
- Public subscription endpoint fetches active remote subscriptions, merges them, base64-encodes the merged content, and returns `text/plain`.
- Aggregated `subscription-userinfo`: `upload/download/total` are summed, `expire` is the maximum.
- Aggregated `profile-title` comes from `PROFILE_TITLE`.

## Run

```bash
ADMIN_USERNAME=admin \
ADMIN_PASSWORD=secret \
HTTP_ADDR=:8080 \
DB_PATH=./data/app.db \
go run ./cmd/main.go
```

## Container

```bash
docker build -t 3xui-user-sync .
docker run --rm -p 8080:8080 \
  -e ADMIN_USERNAME=admin \
  -e ADMIN_PASSWORD=secret \
  -v "$PWD/testdata/data:/app/data" \
  3xui-user-sync
```

## Compose

```bash
docker compose up --build
```

SQLite file will be stored in `./testdata/data/app.db`.
