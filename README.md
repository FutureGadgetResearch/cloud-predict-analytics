# cloud-predict-analytics

A Go CLI that pulls Polymarket prediction market data for weather-based events ("highest temperature in \<city\> on \<date\>") and lands it into BigQuery for analysis.

---

## Where the data lives

After running the job, rows land in BigQuery at:

```
Project:  fg-polylabs
Dataset:  weather
Table:    polymarket_snapshots
```

Full table path: **`fg-polylabs.weather.polymarket_snapshots`**

The table is **day-partitioned on `date`** and **clustered on `city`, `market_id`**, so queries filtered by date range or city are cheap.

### Quick look in BigQuery console

1. Open [BigQuery Studio](https://console.cloud.google.com/bigquery?project=fg-polylabs)
2. In the explorer, expand **fg-polylabs → weather → polymarket_snapshots**
3. Click **Preview** to see recent rows

### Quick SQL

```sql
-- Latest YES probability for each threshold in London on a given date
SELECT
  city,
  date,
  temp_threshold,
  yes_cost,
  no_cost,
  spread,
  timestamp
FROM `fg-polylabs.weather.polymarket_snapshots`
WHERE city = 'london'
  AND date = '2026-03-10'
ORDER BY temp_threshold, timestamp;
```

---

## Table schema

| Column | Type | Mode | Description |
|---|---|---|---|
| `city` | STRING | REQUIRED | Normalized city name (lowercase), e.g. `london` |
| `date` | DATE | REQUIRED | The event date the market resolves on |
| `timestamp` | TIMESTAMP | REQUIRED | UTC time of this price snapshot (from CLOB price-history feed) |
| `temp_threshold` | FLOAT64 | REQUIRED | Temperature threshold in °C parsed from the market question |
| `yes_cost` | FLOAT64 | REQUIRED | Implied probability of YES (0.0–1.0) |
| `no_cost` | FLOAT64 | REQUIRED | Implied probability of NO (0.0–1.0) |
| `best_bid` | FLOAT64 | NULLABLE | Best bid for YES token at time of fetch |
| `best_ask` | FLOAT64 | NULLABLE | Best ask for YES token at time of fetch |
| `spread` | FLOAT64 | NULLABLE | Bid-ask spread (`best_ask - best_bid`) |
| `volume_24h` | FLOAT64 | NULLABLE | 24-hour trading volume in USDC at time of fetch |
| `volume_total` | FLOAT64 | NULLABLE | Lifetime trading volume in USDC at time of fetch |
| `liquidity` | FLOAT64 | NULLABLE | Market liquidity in USDC at time of fetch |
| `market_id` | STRING | REQUIRED | Polymarket condition ID (stable market identifier) |
| `event_slug` | STRING | REQUIRED | Polymarket event slug used to query the Gamma API |
| `market_end_date` | STRING | NULLABLE | ISO datetime when the market closes and resolves |
| `market_start_date` | STRING | NULLABLE | ISO datetime when the market opened for trading |
| `accepting_orders` | BOOL | NULLABLE | Whether the market was still open for trading at fetch time |
| `neg_risk` | BOOL | NULLABLE | Whether this is a neg-risk market (pooled liquidity; prices behave differently) |
| `ingested_at` | TIMESTAMP | REQUIRED | UTC time the pipeline collected and wrote this row |

---

## Deploying and running in production

### First-time infra setup

Run once to create all GCP resources (Artifact Registry, service accounts, Workload Identity Federation, Cloud Run Job):

```bash
./scripts/setup.sh --github-repo=FutureGadgetLabs/cloud-predict-analytics2
```

The script prints two values at the end — add them as GitHub Actions secrets:

| Secret | Description |
|---|---|
| `WIF_PROVIDER` | Workload Identity Federation provider resource name |
| `WIF_SERVICE_ACCOUNT` | CI service account email |

### CI/CD — GitHub Actions

Every push to `main` automatically:
1. Builds the Docker image
2. Pushes it to Artifact Registry (`us-docker.pkg.dev/fg-polylabs/polymarket/polymarket`)
3. Updates the Cloud Run Job to the new image SHA

Workflow: `.github/workflows/build.yml`

### Trigger a job on-demand

```bash
# Run now and wait for completion
./scripts/run.sh execute --city=london --date=2026-03-10

# With a specific threshold and per-minute granularity
./scripts/run.sh execute --city=london --date=2026-03-10 --temp=10 --fidelity=1
```

### Schedule recurring jobs

```bash
# Daily at 8 AM UTC for a fixed city/date/threshold
./scripts/run.sh schedule \
  --city=london \
  --date=2026-03-10 \
  --temp=10 \
  --cron="0 8 * * *"

# With a custom name
./scripts/run.sh schedule \
  --city=new-york \
  --date=2026-03-10 \
  --temp=15 \
  --cron="0 */6 * * *" \
  --name=nyc-15c-every6h

# List all scheduled jobs
./scripts/run.sh list-schedules

# Delete a schedule
./scripts/run.sh delete-schedule --name=nyc-15c-every6h
```

---

## Running the job

### Prerequisites

- Go 1.22+
- GCP credentials with BigQuery Data Editor on `fg-polylabs.weather`

```bash
gcloud auth application-default login
```

### Build

```bash
go build -o polymarket ./cmd/polymarket
```

### Usage

```
polymarket --city=<city> --date=<YYYY-MM-DD> [--temp=<celsius>] [--fidelity=<minutes>] [--dry-run]
```

| Flag | Default | Description |
|---|---|---|
| `--city` | _(required)_ | City name, e.g. `london`, `new-york` |
| `--date` | _(required)_ | Event date in `YYYY-MM-DD` format |
| `--temp` | `0` (all) | Filter to a specific temperature threshold in °C |
| `--fidelity` | `60` | Price snapshot granularity in minutes (`1`=per-minute, `60`=hourly) |
| `--dry-run` | `false` | Print rows as JSONL to stdout instead of loading to BigQuery |

### Examples

```bash
# Dry-run: print all markets for London on March 10 2026 as JSONL
polymarket --city=london --date=2026-03-10 --dry-run

# Dry-run: only the 10°C threshold market, per-minute granularity
polymarket --city=london --date=2026-03-10 --temp=10 --fidelity=1 --dry-run

# Load to BigQuery (not yet implemented — use --dry-run for now)
polymarket --city=london --date=2026-03-10 --temp=10
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
e.g. `highest-temperature-in-london-on-march-10-2026`

---

## Project structure

```
cmd/polymarket/main.go          CLI entry point, orchestration
internal/polymarket/client.go   HTTP client for Gamma and CLOB APIs
internal/polymarket/models.go   API response types and PredictionSnapshot (BQ row)
```
