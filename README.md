# cloud-predict-analytics

## Multi-Repo Project

This is one of three repositories:

| Repo | Role |
|------|------|
| [`cloud-predict-analytics-frontend-admin`](https://github.com/FG-PolyLabs/cloud-predict-analytics-frontend-admin) | Admin UI (Hugo + Firebase Auth + GitHub Pages) |
| [`cloud-predict-analytics`](https://github.com/FG-PolyLabs/cloud-predict-analytics) | **This repo** — backend (Cloud Run API + scheduled job) |
| [`cloud-predict-analytics-data`](https://github.com/FG-PolyLabs/cloud-predict-analytics-data) | Reference city data (JSONL → GCS → BigQuery) |

---

Go backend for the FutureGadgetLabs weather prediction analytics platform. It has two roles:

- **Batch jobs** — Cloud Run Jobs that pull Polymarket prediction market data for weather events and land it into BigQuery. A single Cloud Scheduler fires daily and the job iterates over all cities configured in the `tracked_cities` reference table.
- **API service** — Cloud Run Service that exposes the BigQuery data to the frontend admin dashboard.

---

## Live resources

| Resource | Name | URL / details |
|---|---|---|
| Cloud Run Service | `weather-api` | `https://weather-api-846376753241.us-central1.run.app` |
| Cloud Run Job | `weather-polymarket` | args: `--all-cities --yesterday` |
| Cloud Scheduler | `weather-daily` | `0 1 * * *` UTC → triggers `weather-polymarket` |
| Artifact Registry | `polymarket` | `us-central1-docker.pkg.dev/fg-polylabs/polymarket/polymarket` |

---

## System architecture

```
Cloud Scheduler (daily, 1 job)
        │
        ▼
Cloud Run Job  ──reads──▶  tracked_cities (BQ reference table)
        │                   active cities, timezones
        │  for each city
        ▼
  Polymarket APIs
  (Gamma + CLOB)
        │
        ▼
  polymarket_snapshots (BQ output table)
        │
        ▼
Cloud Run Service  ◀──── Frontend Admin Dashboard
```

---

## BigQuery tables

### `fg-polylabs.weather.polymarket_snapshots` — output table

Price snapshots for each tracked Polymarket weather market. Day-partitioned on `date`, clustered on `city`.

| Column | Type | Mode | Description |
|---|---|---|---|
| `city` | STRING | REQUIRED | Normalized city name (lowercase), e.g. `london` |
| `date` | DATE | REQUIRED | The event resolution date |
| `timestamp` | TIMESTAMP | REQUIRED | UTC time of this price snapshot |
| `temp_threshold` | FLOAT64 | REQUIRED | Temperature threshold in °C parsed from the market question |
| `yes_cost` | FLOAT64 | REQUIRED | Implied probability of YES (0.0–1.0) |
| `no_cost` | FLOAT64 | REQUIRED | Implied probability of NO (0.0–1.0) |
| `best_bid` | FLOAT64 | NULLABLE | Best bid for YES token at time of fetch |
| `best_ask` | FLOAT64 | NULLABLE | Best ask for YES token at time of fetch |
| `spread` | FLOAT64 | NULLABLE | Bid-ask spread (`best_ask - best_bid`) |
| `volume_24h` | FLOAT64 | NULLABLE | 24-hour trading volume in USDC |
| `volume_total` | FLOAT64 | NULLABLE | Lifetime trading volume in USDC |
| `liquidity` | FLOAT64 | NULLABLE | Market liquidity in USDC |
| `event_slug` | STRING | REQUIRED | Polymarket event slug |
| `market_end_date` | STRING | NULLABLE | ISO date when the market closes and resolves |

**Quick look:**

```sql
-- Price history for all thresholds in London on a given date
SELECT city, date, temp_threshold, yes_cost, no_cost, spread, timestamp
FROM `fg-polylabs.weather.polymarket_snapshots`
WHERE city = 'london'
  AND date = '2026-03-06'
ORDER BY temp_threshold, timestamp;
```

---

### `fg-polylabs.weather.tracked_cities` — reference table

Controls which cities are included in each scheduled run. To add or pause a city, update this table — no scheduler changes needed.

| Column | Type | Mode | Description |
|---|---|---|---|
| `city` | STRING | REQUIRED | Slug form used in Polymarket slugs, e.g. `london`, `new-york` |
| `display_name` | STRING | REQUIRED | Human-readable name, e.g. `London` |
| `timezone` | STRING | REQUIRED | IANA timezone identifier, e.g. `Europe/London` |
| `active` | BOOL | REQUIRED | Set to `FALSE` to pause tracking without removing the row |
| `added_date` | DATE | REQUIRED | Date this city was added to tracking |
| `notes` | STRING | NULLABLE | Optional free-text notes |

**Create and seed the table** (uses Application Default Credentials):

```bash
go run ./cmd/setup
```

This is idempotent — existing rows are skipped on re-run.

**Add a city:**

```sql
INSERT INTO `fg-polylabs.weather.tracked_cities` (city, display_name, timezone, active, added_date)
VALUES ('sydney', 'Sydney', 'Australia/Sydney', TRUE, CURRENT_DATE());
```

**Pause a city:**

```sql
UPDATE `fg-polylabs.weather.tracked_cities`
SET active = FALSE
WHERE city = 'dallas';
```

---

## Data filtering — why rows may appear to be missing

The pipeline applies four filters at ingestion time to avoid storing noise.
**If a row is absent from the table it does not mean the data is unavailable — it means one of the rules below applied.**

### Filter 1 — Zero-activity markets (entire market dropped)

If a market has `volume_total = 0` AND `liquidity = 0` at the time of fetch, it has never attracted any trading. Its prices are placeholders with no real price discovery behind them. The entire market is skipped.

**What to do if you need it:** re-run the job with `--temp=<threshold>` to force-fetch that specific market regardless of activity, or query Polymarket directly.

### Filter 2 — Post-resolution rows (rows after market close dropped)

Rows with a `timestamp` after `market_end_date` are skipped. Once a market resolves, the price locks at ~0 or ~1 and carries no new information.

**What to do if you need it:** the final pre-resolution price is the last row for that `(event_slug, temp_threshold)` combination in the table.

### Filter 3 — Effectively-resolved prices (extreme YES probabilities dropped)

Rows where `yes_cost >= 0.98` or `yes_cost <= 0.02` are dropped. Prices this extreme indicate the market has already resolved with very high certainty and carry no meaningful price discovery signal.

**What to do if you need it:** query Polymarket directly for the tail of each market's price history.

### Filter 4 — Unchanged prices (rows where price did not move are dropped)

If `yes_cost` has not changed by more than `0.0005` (0.05%) from the previous snapshot, the row is dropped. Only rows where the price actually moved are stored.

**This is the most important one to understand when querying.** The table is sparse by design — gaps between timestamps do not mean the price was unknown. They mean the price was the same as the preceding row.

**To reconstruct a continuous price series, use `LAST_VALUE` with `IGNORE NULLS`:**

```sql
-- Fill gaps: carry the last known price forward for every hour in the window
WITH spine AS (
  SELECT ts, temp_threshold
  FROM
    UNNEST(GENERATE_TIMESTAMP_ARRAY(
      TIMESTAMP '2026-03-04 00:00:00 UTC',
      TIMESTAMP '2026-03-06 23:00:00 UTC',
      INTERVAL 1 HOUR
    )) AS ts
  CROSS JOIN UNNEST([13.0, 14.0, 15.0, 16.0]) AS temp_threshold
),
prices AS (
  SELECT timestamp, temp_threshold, yes_cost, no_cost
  FROM `fg-polylabs.weather.polymarket_snapshots`
  WHERE event_slug = 'highest-temperature-in-london-on-march-6-2026'
)
SELECT
  s.ts,
  s.temp_threshold,
  LAST_VALUE(p.yes_cost IGNORE NULLS) OVER (
    PARTITION BY s.temp_threshold ORDER BY s.ts
    ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
  ) AS yes_cost
FROM spine s
LEFT JOIN prices p
  ON p.timestamp = s.ts AND p.temp_threshold = s.temp_threshold
ORDER BY s.temp_threshold, s.ts;
```

---

## Deploying and running in production

### First-time infra setup

Run once to create all GCP resources (Artifact Registry, service accounts, Workload Identity Federation, Cloud Run Job):

```bash
./scripts/setup.sh --github-repo=FG-PolyLabs/cloud-predict-analytics
```

The script prints two values at the end — add them as GitHub Actions secrets:

| Secret | Description |
|---|---|
| `WIF_PROVIDER` | Workload Identity Federation provider resource name |
| `WIF_SERVICE_ACCOUNT` | CI service account email |

Then create and seed the `tracked_cities` table:

```bash
bq query --project_id=fg-polylabs --use_legacy_sql=false < sql/tracked_cities.sql
```

### CI/CD — GitHub Actions

Every push to `main` automatically:
1. Builds the Docker image
2. Pushes it to Artifact Registry (`us-central1-docker.pkg.dev/fg-polylabs/polymarket/polymarket`)
3. Updates the Cloud Run Job to the new image SHA

Workflow: `.github/workflows/build.yml`

### Cloud Scheduler — single daily job

One scheduler triggers the Cloud Run Job daily. The job reads all `active = TRUE` cities from `tracked_cities` and fetches snapshots for each.

```bash
# Create the single daily scheduler (runs at 10:00 UTC)
./scripts/run.sh schedule \
  --yesterday \
  --cron="0 10 * * *" \
  --name=polymarket-daily
```

To add or remove cities from the daily run, update `tracked_cities` in BigQuery — no scheduler changes needed.

### Trigger a job on-demand

```bash
# Run for all active cities (yesterday's markets)
./scripts/run.sh execute --yesterday

# Run for a specific city and date
./scripts/run.sh execute --city=london --date=2026-03-06
```

### Manage schedules

```bash
# List all scheduled jobs
./scripts/run.sh list-schedules

# Delete a schedule
./scripts/run.sh delete-schedule --name=<schedule-name>
```

---

## Running the job locally

### Prerequisites

- Go 1.25+
- GCP credentials: `gcloud auth application-default login`

### Build

```bash
go build -o polymarket ./cmd/polymarket
```

### Usage

```
polymarket --date=<YYYY-MM-DD> [--city=<city>] [--slug=<event-slug>] [--temp=<celsius>] [--fidelity=<minutes>] [--dry-run]
```

| Flag | Default | Description |
|---|---|---|
| `--date` | _(required)_ | Event resolution date in `YYYY-MM-DD` format |
| `--city` | | City name for weather events, e.g. `london`. Not needed when `--slug` is set |
| `--slug` | | Polymarket event slug. Overrides auto slug construction from `--city`/`--date` |
| `--temp` | `0` (all) | Filter to a specific temperature threshold in °C |
| `--fidelity` | `60` | Price snapshot granularity in minutes (`1`=per-minute, `60`=hourly) |
| `--dry-run` | `false` | Print rows as JSONL to stdout instead of loading to BigQuery |
| `--yesterday` | `false` | Use yesterday's UTC date as the event date |
| `--no-volume` | `false` | Store NULL for volume/liquidity fields (use for historical backfills) |

### Examples

```bash
# Dry-run: print all markets for London on March 6 as JSONL
go run ./cmd/polymarket --city=london --date=2026-03-06 --dry-run

# Load to BigQuery using an explicit slug
go run ./cmd/polymarket --slug=highest-temperature-in-london-on-march-6-2026 --date=2026-03-06

# Per-minute granularity for a specific threshold
go run ./cmd/polymarket --city=london --date=2026-03-06 --temp=16 --fidelity=1 --dry-run
```

---

## Data sources

All data comes from the public Polymarket APIs — no API key required.

| API | Base URL | Used for |
|---|---|---|
| Gamma API | `https://gamma-api.polymarket.com` | Event/market metadata, prices, volume, liquidity |
| CLOB API | `https://clob.polymarket.com` | Time-series price history per token |

Polymarket weather events follow the slug pattern:
```
highest-temperature-in-{city}-on-{month}-{day}-{year}
```
e.g. `highest-temperature-in-london-on-march-6-2026`

---

## API endpoints

The API service (`cmd/api`) is a Cloud Run Service consumed by the frontend admin. Protected routes require a Firebase ID token: `Authorization: Bearer <token>`.

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/health` | None | Liveness check — returns `{"status":"ok"}` |
| `GET` | `/info` | None | Non-sensitive runtime config (service, revision, project, region, bq_dataset) |
| `GET` | `/tracked-cities` | Required | List all tracked cities |
| `POST` | `/tracked-cities` | Required | Add a city |
| `PUT` | `/tracked-cities/:city` | Required | Update display name, timezone, active flag, or notes |
| `DELETE` | `/tracked-cities/:city` | Required | Remove a city |
| `GET` | `/snapshots?city=&date=&limit=` | Required | Query snapshot data |

### Running the API server locally

```bash
go run ./cmd/api
# Listening on :8080

# Set a different port
PORT=9000 go run ./cmd/api
```

---

## Project structure

```
cmd/polymarket/main.go             CLI entry point, orchestration, ingestion filters
cmd/api/main.go                    HTTP API server (Cloud Run Service)
cmd/setup/main.go                  One-time BQ table creation and seeding
internal/polymarket/client.go      HTTP client for Gamma and CLOB APIs
internal/polymarket/models.go      API response types and PredictionSnapshot (BQ row)
internal/polymarket/loader.go      BigQuery MERGE writer (staging table → target)
internal/api/server.go             HTTP router, CORS, Firebase auth middleware
internal/api/cities.go             CRUD handlers for tracked_cities
internal/api/snapshots.go          Query handler for polymarket_snapshots
sql/tracked_cities.sql             Reference DDL (go run ./cmd/setup is preferred)
scripts/setup.sh                   One-time GCP infra provisioning
scripts/run.sh                     Trigger or schedule Cloud Run Job executions
.github/workflows/build.yml        CI/CD: build image, push to AR, update Cloud Run Job
```
