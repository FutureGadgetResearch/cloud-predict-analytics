# cloud-predict-analytics (Backend)

## Multi-Repo Project: cloud-predict-analytics

This repo is **one of three** repositories that together form the cloud-predict-analytics system. All three repos should be cloned as siblings under the same parent directory.

### Repository Layout

```
FutureGadgetLabs/
├── cloud-predict-analytics-frontend-admin/   (admin UI — Hugo + Firebase Auth + GitHub Pages)
├── cloud-predict-analytics/                  ← THIS REPO (backend — Cloud Run API + job)
└── cloud-predict-analytics-data/             (reference data JSONL → GCS → BigQuery)
```

### Repository Roles

| Repo | GitHub | Role |
|------|--------|------|
| `cloud-predict-analytics-frontend-admin` | https://github.com/FG-PolyLabs/cloud-predict-analytics-frontend-admin | Admin UI; calls this repo's API for CRUD |
| `cloud-predict-analytics` | https://github.com/FG-PolyLabs/cloud-predict-analytics | **This repo** — Cloud Run API (`weather-api`) + Cloud Run Job (`weather-polymarket`) |
| `cloud-predict-analytics-data` | https://github.com/FG-PolyLabs/cloud-predict-analytics-data | Reference city data; seeded via JSONL → GCS → BQ |

---

## This Repo: Backend

### What it does

Two Go entry points under `cmd/`:

- **`cmd/polymarket`** — Cloud Run Job (batch). Fetches Polymarket weather prediction market data and loads snapshots into BigQuery (`polymarket_snapshots`). Triggered daily by Cloud Scheduler, iterates over all `active = TRUE` cities from `tracked_cities`.
- **`cmd/api`** — Cloud Run Service. REST API consumed by the admin frontend. All write endpoints require a Firebase ID token (`Authorization: Bearer <token>`).
- **`cmd/setup`** — One-time BQ table creation and seeding.

### GCP Infrastructure

| Resource | Details |
|----------|---------|
| GCP Project | `fg-polylabs` |
| Cloud Run Service | `weather-api` — `https://weather-api-846376753241.us-central1.run.app` |
| Cloud Run Job | `weather-polymarket` — args: `--all-cities --yesterday` |
| Cloud Scheduler | `weather-daily` — `0 1 * * *` UTC, triggers `weather-polymarket` |
| Artifact Registry | `us-central1-docker.pkg.dev/fg-polylabs/polymarket/polymarket` |
| BigQuery | Project `fg-polylabs`, dataset `weather` |
| GCS (reference data) | `weather` — managed by `cloud-predict-analytics-data` repo |
| Firebase Project | `collection-showcase-auth` — token validation on all API writes |

### Code Structure

```
cmd/polymarket/main.go          CLI/job entry point — fetch, filter, load to BQ
cmd/api/main.go                 HTTP API server (Cloud Run Service)
cmd/setup/main.go               One-time BQ table creation and seeding
internal/polymarket/client.go   HTTP client for Polymarket Gamma + CLOB APIs
internal/polymarket/models.go   PredictionSnapshot (BQ row) + API response types
internal/polymarket/loader.go   BigQuery MERGE writer (staging → target)
internal/api/server.go          HTTP router, CORS, Firebase auth middleware
internal/api/cities.go          CRUD handlers for tracked_cities
internal/api/snapshots.go       Query handler for polymarket_snapshots
sql/tracked_cities.sql          Reference DDL
scripts/setup.sh                One-time GCP infra provisioning
scripts/run.sh                  Trigger or schedule Cloud Run Job executions
.github/workflows/build.yml     CI/CD: build image, push to AR, update Cloud Run Job
```

### API Endpoints (weather-api)

All endpoints require `Authorization: Bearer <firebase-id-token>`.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/tracked-cities` | List all tracked cities |
| `POST` | `/tracked-cities` | Add a city |
| `PUT` | `/tracked-cities/:city` | Update city (display name, timezone, active, notes) |
| `DELETE` | `/tracked-cities/:city` | Remove a city |
| `GET` | `/snapshots?city=&date=&limit=` | Query snapshot data |

### BigQuery Tables

- **`polymarket_snapshots`** — output table, day-partitioned on `date`, clustered on `city`
- **`tracked_cities`** — reference table read by the daily job

See README.md for full schemas.

### Development Notes

- CI/CD: every push to `main` builds the Docker image, pushes to Artifact Registry, and updates the Cloud Run Job
- To run the job locally: `go run ./cmd/polymarket --city=london --date=2026-03-22 --dry-run`
- To run the API locally: `go run ./cmd/api` (listens on `:8080`)
- The admin frontend (`../cloud-predict-analytics-frontend-admin`) talks to this API — set `HUGO_PARAMS_BACKENDURL` to the Cloud Run Service URL or `http://localhost:8080` for local dev
- Firebase token validation uses Application Default Credentials in production and the `collection-showcase-auth` project
- The `cloud-predict-analytics-data` repo (`../cloud-predict-analytics-data`) manages the `tracked_cities` seed data — push there to bulk-reset reference data
