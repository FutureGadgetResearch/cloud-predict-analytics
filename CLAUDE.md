# cloud-predict-analytics (Backend)

## Multi-Repo Project: cloud-predict-analytics

This repo is **one of three** repositories that together form the cloud-predict-analytics system. All three repos should be cloned as siblings under the same parent directory.

### Repository Layout

```
FutureGadgetLabs/
├── cloud-predict-analytics-frontend-admin/   (admin UI — Hugo + Firebase Auth + GitHub Pages)
├── cloud-predict-analytics/                  ← THIS REPO (backend — Cloud Run API + jobs)
└── cloud-predict-analytics-data/             (reference data JSONL + public frontend)
```

### Repository Roles

| Repo | GitHub | Role |
|------|--------|------|
| `cloud-predict-analytics-frontend-admin` | https://github.com/FG-PolyLabs/cloud-predict-analytics-frontend-admin | Admin UI; calls this repo's API for CRUD |
| `cloud-predict-analytics` | https://github.com/FG-PolyLabs/cloud-predict-analytics | **This repo** — Cloud Run API (`weather-api`) + Cloud Run Jobs (`weather-polymarket`, `weather-sync`) |
| `cloud-predict-analytics-data` | https://github.com/FG-PolyLabs/cloud-predict-analytics-data | JSONL data files written by `weather-sync`; also hosts the public frontend |

---

## This Repo: Backend

### What it does

Three Go entry points under `cmd/`:

- **`cmd/polymarket`** — Cloud Run Job (batch). Fetches Polymarket weather prediction market data and merges snapshots into BigQuery (`polymarket_snapshots`). Triggered daily by Cloud Scheduler at 01:00 UTC; iterates over all `active = TRUE` cities from `tracked_cities`. Supports `--date-range` for backfills.
- **`cmd/sync`** — Cloud Run Job (batch). Exports `tracked_cities` and `polymarket_snapshots` from BigQuery to GCS and GitHub. Triggered daily at 03:00 UTC (2 hours after polymarket fetch). Also callable on-demand via `POST /sync`.
- **`cmd/api`** — Cloud Run Service. REST API consumed by the admin frontend. Public endpoints: `/health`, `/info`. All other endpoints require a Firebase ID token (`Authorization: Bearer <token>`).
- **`cmd/setup`** — One-time BQ table creation and seeding.

### GCP Infrastructure

| Resource | Details |
|----------|---------|
| GCP Project | `fg-polylabs` |
| Cloud Run Service | `weather-api` — `https://weather-api-846376753241.us-central1.run.app` |
| Cloud Run Job | `weather-polymarket` — args: `--all-cities --yesterday`; runs daily at 01:00 UTC |
| Cloud Run Job | `weather-sync` — exports BQ → GCS + GitHub; runs daily at 03:00 UTC |
| Cloud Scheduler | `weather-daily` — `0 1 * * *` UTC, triggers `weather-polymarket` |
| Cloud Scheduler | `weather-sync-daily` — `0 3 * * *` UTC, triggers `weather-sync` |
| Artifact Registry | `us-central1-docker.pkg.dev/fg-polylabs/polymarket/polymarket` |
| BigQuery | Project `fg-polylabs`, dataset `weather` |
| GCS Bucket | `fg-polylabs-weather-data` — written by `weather-sync`, read by admin frontend |
| Firebase Project | `collection-showcase-auth` — token validation on all API writes |
| Service Account | `polymarket-runner@fg-polylabs.iam.gserviceaccount.com` — used by all three Cloud Run resources |

### Code Structure

```
cmd/polymarket/main.go          CLI/job entry point — fetch, filter, merge to BQ
cmd/sync/main.go                CLI/job entry point — export BQ → GCS + GitHub
cmd/api/main.go                 HTTP API server (Cloud Run Service)
cmd/setup/main.go               One-time BQ table creation and seeding
internal/polymarket/client.go   HTTP client for Polymarket Gamma + CLOB APIs
internal/polymarket/models.go   PredictionSnapshot (BQ row) + API response types
internal/polymarket/loader.go   BigQuery MERGE writer (staging → target table)
internal/syncer/syncer.go       Export logic — BQ → JSONL → GCS + GitHub
internal/api/server.go          HTTP router, CORS, Firebase auth middleware
internal/api/cities.go          CRUD handlers for tracked_cities
internal/api/snapshots.go       Query handler for polymarket_snapshots
internal/api/sync.go            POST /sync handler — triggers syncer.Run()
internal/api/backfill.go        POST /backfill handler — triggers weather-polymarket job
sql/tracked_cities.sql          Reference DDL
scripts/setup.sh                One-time GCP infra provisioning
scripts/run.sh                  Trigger or schedule Cloud Run Job executions
.github/workflows/build.yml     CI/CD: build image, push to AR, update all Cloud Run resources
```

### API Endpoints (weather-api)

`/health` and `/info` are public. All other endpoints require `Authorization: Bearer <firebase-id-token>`.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Liveness check — returns `{"status":"ok"}` |
| `GET` | `/info` | Runtime config — returns project, bucket, API version |
| `GET` | `/tracked-cities` | List all tracked cities |
| `POST` | `/tracked-cities` | Add a city |
| `PUT` | `/tracked-cities/{source}/{city}` | Update city (display name, timezone, active, notes) |
| `DELETE` | `/tracked-cities/{source}/{city}` | Remove a city |
| `GET` | `/snapshots?city=&date=&date_from=&date_to=&limit=` | Query snapshot data; `limit=0` = no limit |
| `POST` | `/sync` | Export BQ → GCS + GitHub via `weather-sync` syncer |
| `POST` | `/backfill` | Trigger `weather-polymarket` job with `--date-range`; body: `{"date_from":"YYYY-MM-DD","date_to":"YYYY-MM-DD","city":""}` |

### BigQuery Tables

- **`polymarket_snapshots`** — output table, day-partitioned on `date`, clustered on `city`. Written by `weather-polymarket`, read by `weather-sync` and `weather-api`.
- **`tracked_cities`** — reference table. Composite key: `(source, city)`. Read by `weather-polymarket` for active city list; managed via CRUD API.

### polymarket job flags

```
--city=<slug>              Single city (e.g. london, new-york)
--date=YYYY-MM-DD          Event date
--date-range=FROM:TO       Date range for backfill (inclusive); iterates each day
--yesterday                Use yesterday's UTC date (used by scheduled run)
--all-cities               Run for all active cities in tracked_cities
--slug=<event-slug>        Override auto-constructed Polymarket event slug
--temp=<threshold>         Filter to a specific temperature threshold market
--fidelity=<minutes>       Price history granularity (default: 60 = hourly)
--no-volume                Store NULL for volume/liquidity fields (use for backfills)
--dry-run                  Print rows as JSONL to stdout instead of writing to BQ
```

### Development Notes

- CI/CD: every push to `main` builds the Docker image, pushes to Artifact Registry, and updates all three Cloud Run resources (`weather-api`, `weather-polymarket`, `weather-sync`). The `weather-sync` job also has `GITHUB_TOKEN` and `GITHUB_DATA_REPO` injected from the `DATA_SYNC_PAT` GitHub org secret.
- To run the polymarket job locally: `go run ./cmd/polymarket --city=london --date=2026-03-22 --dry-run`
- To run a backfill locally: `go run ./cmd/polymarket --all-cities --date-range=2026-03-18:2026-03-22 --no-volume --dry-run`
- To run the sync job locally: `go run ./cmd/sync`
- To run the API locally: `go run ./cmd/api` (listens on `:8080`)
- The admin frontend (`../cloud-predict-analytics-frontend-admin`) talks to this API — set `HUGO_PARAMS_BACKENDURL` to the Cloud Run Service URL or `http://localhost:8080` for local dev
- Firebase token validation uses Application Default Credentials in production and the `collection-showcase-auth` project
- Tracked cities use a composite key `(source, city)` — the `source` field identifies which prediction market platform the city belongs to (e.g. `polymarket`)
