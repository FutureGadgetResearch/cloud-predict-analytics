#!/usr/bin/env bash
# run.sh — trigger the weather-polymarket Cloud Run Job on-demand or create a Cloud Scheduler job.
#
# Weather events (auto slug):
#   ./scripts/run.sh execute --city=london --date=2026-03-10 --temp=10
#   ./scripts/run.sh execute --city=london --date=2026-03-10 --fidelity=1
#
# Any Polymarket event (explicit slug):
#   ./scripts/run.sh execute --slug=highest-temperature-in-london-on-march-6-2026 --date=2026-03-06
#
# Scheduling:
#   ./scripts/run.sh schedule --city=london --date=2026-03-10 --temp=10 --cron="0 8 * * *"
#   ./scripts/run.sh schedule --slug=some-event-slug --date=2026-03-10 --cron="0 8 * * *" --name=my-schedule
#
#   ./scripts/run.sh list-schedules
#   ./scripts/run.sh delete-schedule --name=london-10c-daily

set -euo pipefail

# ── Config ────────────────────────────────────────────────────────────────────
PROJECT_ID="fg-polylabs"
REGION="us-central1"
JOB_NAME="weather-polymarket"
RUNNER_SA="polymarket-runner@${PROJECT_ID}.iam.gserviceaccount.com"

# ── Parse subcommand ──────────────────────────────────────────────────────────
SUBCOMMAND="${1:-}"
shift || true

if [[ -z "$SUBCOMMAND" ]]; then
  echo "Usage: $0 <execute|schedule|list-schedules|delete-schedule> [options]"
  exit 1
fi

# ── Parse flags ───────────────────────────────────────────────────────────────
CITY=""
DATE=""
SLUG=""
TEMP="0"
FIDELITY="60"
CRON=""
SCHEDULE_NAME=""

for arg in "$@"; do
  case $arg in
    --city=*)     CITY="${arg#*=}" ;;
    --date=*)     DATE="${arg#*=}" ;;
    --slug=*)     SLUG="${arg#*=}" ;;
    --temp=*)     TEMP="${arg#*=}" ;;
    --fidelity=*) FIDELITY="${arg#*=}" ;;
    --cron=*)     CRON="${arg#*=}" ;;
    --name=*)     SCHEDULE_NAME="${arg#*=}" ;;
  esac
done

# Build the container args array for the Cloud Run job overrides
build_args() {
  local args="\"--date=${DATE}\""
  if [[ -n "$SLUG" ]]; then
    args="${args}, \"--slug=${SLUG}\""
  else
    args="${args}, \"--city=${CITY}\""
  fi
  if [[ "$TEMP" != "0" ]]; then
    args="${args}, \"--temp=${TEMP}\""
  fi
  if [[ "$FIDELITY" != "60" ]]; then
    args="${args}, \"--fidelity=${FIDELITY}\""
  fi
  echo "${args}"
}

# Generate a schedule name from flags if not provided
default_schedule_name() {
  if [[ -n "$SLUG" ]]; then
    local slug_short="${SLUG:0:40}"
    echo "polymarket_${slug_short//-/_}"
  else
    local city_slug="${CITY//-/_}"
    local date_slug="${DATE//-/}"
    local temp_slug="${TEMP//./_}"
    echo "polymarket_${city_slug}_${temp_slug}c_${date_slug}"
  fi
}

# ── Subcommands ───────────────────────────────────────────────────────────────

case "$SUBCOMMAND" in

  execute)
    if [[ -z "$DATE" ]]; then
      echo "Error: --date is required for execute"
      exit 1
    fi
    if [[ -z "$SLUG" && -z "$CITY" ]]; then
      echo "Error: provide either --city (weather events) or --slug (any event)"
      exit 1
    fi

    ARGS=$(build_args)
    echo "==> Executing Cloud Run Job: ${JOB_NAME}"
    if [[ -n "$SLUG" ]]; then
      echo "    slug=${SLUG}  date=${DATE}  fidelity=${FIDELITY}"
    else
      echo "    city=${CITY}  date=${DATE}  temp=${TEMP}  fidelity=${FIDELITY}"
    fi

    # Build comma-separated args for gcloud
    GCLOUD_ARGS="--date=${DATE}"
    [[ -n "$SLUG" ]]       && GCLOUD_ARGS="${GCLOUD_ARGS},--slug=${SLUG}"
    [[ -z "$SLUG" ]]       && GCLOUD_ARGS="${GCLOUD_ARGS},--city=${CITY}"
    [[ "$TEMP" != "0" ]]   && GCLOUD_ARGS="${GCLOUD_ARGS},--temp=${TEMP}"
    [[ "$FIDELITY" != "60" ]] && GCLOUD_ARGS="${GCLOUD_ARGS},--fidelity=${FIDELITY}"

    gcloud run jobs execute "${JOB_NAME}" \
      --region="${REGION}" \
      --project="${PROJECT_ID}" \
      --args="${GCLOUD_ARGS}" \
      --wait

    echo ""
    echo "==> Done. Check results in BigQuery:"
    echo "    https://console.cloud.google.com/bigquery?project=${PROJECT_ID}&ws=!1m5!1m4!4m3!1s${PROJECT_ID}!2sweather!3spolymarket_snapshots"
    ;;

  schedule)
    if [[ -z "$DATE" || -z "$CRON" ]]; then
      echo "Error: --date and --cron are required for schedule"
      echo "Example cron: \"0 8 * * *\" (daily at 8 AM UTC)"
      exit 1
    fi
    if [[ -z "$SLUG" && -z "$CITY" ]]; then
      echo "Error: provide either --city (weather events) or --slug (any event)"
      exit 1
    fi

    [[ -z "$SCHEDULE_NAME" ]] && SCHEDULE_NAME=$(default_schedule_name)

    ARGS=$(build_args)
    PROJECT_NUMBER=$(gcloud projects describe "${PROJECT_ID}" --format="value(projectNumber)")
    JOB_URI="https://${REGION}-run.googleapis.com/apis/run.googleapis.com/v1/namespaces/${PROJECT_ID}/jobs/${JOB_NAME}:run"

    # Build the request body with container arg overrides
    REQUEST_BODY=$(cat <<EOF
{
  "overrides": {
    "containerOverrides": [{
      "args": [$(echo "${ARGS}")]
    }]
  }
}
EOF
)

    echo "==> Creating Cloud Scheduler job: ${SCHEDULE_NAME}"
    echo "    cron: ${CRON}"
    echo "    city=${CITY}  date=${DATE}  temp=${TEMP}  fidelity=${FIDELITY}"

    gcloud scheduler jobs create http "${SCHEDULE_NAME}" \
      --location="${REGION}" \
      --schedule="${CRON}" \
      --uri="${JOB_URI}" \
      --message-body="${REQUEST_BODY}" \
      --oauth-service-account-email="${RUNNER_SA}" \
      --time-zone="UTC" \
      --project="${PROJECT_ID}"

    echo ""
    echo "==> Scheduler job created: ${SCHEDULE_NAME}"
    echo "    To trigger it now:  gcloud scheduler jobs run ${SCHEDULE_NAME} --location=${REGION}"
    echo "    To delete it:       $0 delete-schedule --name=${SCHEDULE_NAME}"
    ;;

  list-schedules)
    echo "==> Cloud Scheduler jobs in ${PROJECT_ID} / ${REGION}:"
    gcloud scheduler jobs list \
      --location="${REGION}" \
      --project="${PROJECT_ID}" \
      --format="table(name,schedule,state,lastAttemptTime)"
    ;;

  delete-schedule)
    if [[ -z "$SCHEDULE_NAME" ]]; then
      echo "Error: --name is required for delete-schedule"
      exit 1
    fi
    echo "==> Deleting scheduler job: ${SCHEDULE_NAME}"
    gcloud scheduler jobs delete "${SCHEDULE_NAME}" \
      --location="${REGION}" \
      --project="${PROJECT_ID}" \
      --quiet
    echo "    Deleted."
    ;;

  *)
    echo "Unknown subcommand: ${SUBCOMMAND}"
    echo "Usage: $0 <execute|schedule|list-schedules|delete-schedule> [options]"
    exit 1
    ;;

esac
