#!/usr/bin/env bash
# recover-daily.sh — re-fire daily (non-backfill) jobs for dates that failed due to the
# nullable-schema BQ mismatch (March 12–19, 2026). These runs do NOT use --no-volume since
# they capture live market state for the day.
#
# Prerequisites: run migrate-schema.sh first.
#
# Usage: ./scripts/recover-daily.sh

set -euo pipefail

PROJECT_ID="fg-polylabs"
REGION="us-central1"
JOB_NAME="weather-polymarket"

# Dates that were missed while the schema mismatch was active.
START_DATE="2026-03-12"
END_DATE="2026-03-19"

# All cities with active daily scheduler jobs.
CITIES=(
  buenos-aires
  ankara
  seoul
  dallas
  miami
  nyc
  toronto
  chicago
  paris
  tokyo
  london
  singapore
)

TOTAL_FIRED=0

for CITY in "${CITIES[@]}"; do
  echo "==> Recovering city=${CITY}  from=${START_DATE}  to=${END_DATE}"
  CURRENT="${START_DATE}"
  while [[ "${CURRENT}" < "${END_DATE}" || "${CURRENT}" == "${END_DATE}" ]]; do
    echo "    Firing ${CITY} ${CURRENT} ..."
    gcloud run jobs execute "${JOB_NAME}" \
      --region="${REGION}" \
      --project="${PROJECT_ID}" \
      --args="--date=${CURRENT},--city=${CITY}" \
      --no-wait
    TOTAL_FIRED=$((TOTAL_FIRED + 1))
    CURRENT=$(date -d "${CURRENT} + 1 day" +%Y-%m-%d)
  done
done

echo ""
echo "==> Recovery complete. Total jobs fired: ${TOTAL_FIRED}"
echo "    Monitor in Cloud Run: https://console.cloud.google.com/run/jobs?project=${PROJECT_ID}"
