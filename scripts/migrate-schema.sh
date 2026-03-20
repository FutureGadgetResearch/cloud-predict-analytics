#!/usr/bin/env bash
# migrate-schema.sh — alter polymarket_snapshots to make volume/bid-ask columns NULLABLE.
#
# Background: best_bid, best_ask, spread, volume_24h, volume_total, and liquidity
# were originally REQUIRED FLOAT columns. The --no-volume flag (added for backfills)
# sends NULL for these fields, causing MERGE INSERT to fail if the target table still
# has them as REQUIRED. Run this once to relax the constraints.
#
# Usage:
#   ./scripts/migrate-schema.sh

set -euo pipefail

PROJECT_ID="fg-polylabs"
DATASET="weather"
TABLE="polymarket_snapshots"

echo "==> Relaxing REQUIRED constraints on ${PROJECT_ID}.${DATASET}.${TABLE}"

for col in best_bid best_ask spread volume_24h volume_total liquidity; do
  echo "    Setting ${col} to NULLABLE..."
  bq query --project_id="${PROJECT_ID}" --use_legacy_sql=false \
    "ALTER TABLE \`${PROJECT_ID}.${DATASET}.${TABLE}\`
     ALTER COLUMN ${col} DROP NOT NULL"
done

echo ""
echo "==> Migration complete. Re-run any failed backfill executions."
