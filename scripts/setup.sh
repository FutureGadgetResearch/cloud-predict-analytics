#!/usr/bin/env bash
# setup.sh — one-time infra provisioning for cloud-predict-analytics.
#
# Creates:
#   - Artifact Registry repo
#   - Service accounts + IAM bindings
#   - Workload Identity Federation (for GitHub Actions CI)
#   - Cloud Run Job  (weather-polymarket) — daily data collection
#   - Cloud Run Service (weather-api)     — REST API for the admin frontend
#   - Cloud Scheduler   (weather-daily)  — triggers job at 01:00 UTC every day
#
# Prerequisites:
#   gcloud auth login
#   gcloud config set project fg-polylabs
#
# Usage:
#   ./scripts/setup.sh --github-repo=FG-PolyLabs/cloud-predict-analytics

set -euo pipefail

# ── Config ────────────────────────────────────────────────────────────────────
PROJECT_ID="fg-polylabs"
REGION="us-central1"
AR_REPO="polymarket"
IMAGE="us-central1-docker.pkg.dev/${PROJECT_ID}/${AR_REPO}/polymarket"
JOB_NAME="weather-polymarket"
SERVICE_NAME="weather-api"
SCHEDULER_NAME="weather-daily"
RUNNER_SA="polymarket-runner"
CI_SA="github-actions-ci"
WIF_POOL="github-pool"
WIF_PROVIDER="github-provider"

# Parse --github-repo=owner/repo
GITHUB_REPO=""
for arg in "$@"; do
  case $arg in
    --github-repo=*) GITHUB_REPO="${arg#*=}" ;;
  esac
done

if [[ -z "$GITHUB_REPO" ]]; then
  echo "Usage: $0 --github-repo=<owner>/<repo>"
  exit 1
fi

GITHUB_OWNER="${GITHUB_REPO%%/*}"

echo "==> Setting active project to ${PROJECT_ID}"
gcloud config set project "${PROJECT_ID}"

# ── Enable APIs ───────────────────────────────────────────────────────────────
echo "==> Enabling required APIs"
gcloud services enable \
  artifactregistry.googleapis.com \
  run.googleapis.com \
  cloudscheduler.googleapis.com \
  cloudbuild.googleapis.com \
  iam.googleapis.com \
  iamcredentials.googleapis.com \
  --project="${PROJECT_ID}"

# ── Artifact Registry ─────────────────────────────────────────────────────────
echo "==> Creating Artifact Registry repo: ${AR_REPO}"
gcloud artifacts repositories create "${AR_REPO}" \
  --repository-format=docker \
  --location="${REGION}" \
  --description="Polymarket pipeline images" \
  --project="${PROJECT_ID}" 2>/dev/null || echo "    (already exists, skipping)"

# ── Service accounts ─────────────────────────────────────────────────────────
echo "==> Creating runner service account: ${RUNNER_SA}"
gcloud iam service-accounts create "${RUNNER_SA}" \
  --display-name="Polymarket Cloud Run Runner" \
  --project="${PROJECT_ID}" 2>/dev/null || echo "    (already exists, skipping)"

RUNNER_SA_EMAIL="${RUNNER_SA}@${PROJECT_ID}.iam.gserviceaccount.com"

echo "==> Granting BigQuery + Firebase permissions to runner SA"
for role in roles/bigquery.dataEditor roles/bigquery.jobUser roles/firebaseauth.admin; do
  gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
    --member="serviceAccount:${RUNNER_SA_EMAIL}" \
    --role="${role}" --condition=None
done

echo "==> Creating GitHub Actions CI service account: ${CI_SA}"
gcloud iam service-accounts create "${CI_SA}" \
  --display-name="GitHub Actions CI" \
  --project="${PROJECT_ID}" 2>/dev/null || echo "    (already exists, skipping)"

CI_SA_EMAIL="${CI_SA}@${PROJECT_ID}.iam.gserviceaccount.com"

echo "==> Granting Artifact Registry and Cloud Run permissions to CI SA"
for role in roles/artifactregistry.writer roles/run.admin; do
  gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
    --member="serviceAccount:${CI_SA_EMAIL}" \
    --role="${role}" --condition=None
done

gcloud iam service-accounts add-iam-policy-binding "${RUNNER_SA_EMAIL}" \
  --member="serviceAccount:${CI_SA_EMAIL}" \
  --role="roles/iam.serviceAccountUser" \
  --project="${PROJECT_ID}"

# ── Workload Identity Federation ──────────────────────────────────────────────
echo "==> Creating Workload Identity Pool: ${WIF_POOL}"
gcloud iam workload-identity-pools create "${WIF_POOL}" \
  --location=global \
  --display-name="GitHub Actions Pool" \
  --project="${PROJECT_ID}" 2>/dev/null || echo "    (already exists, skipping)"

echo "==> Creating Workload Identity Provider: ${WIF_PROVIDER}"
gcloud iam workload-identity-pools providers create-oidc "${WIF_PROVIDER}" \
  --location=global \
  --workload-identity-pool="${WIF_POOL}" \
  --display-name="GitHub OIDC Provider" \
  --issuer-uri="https://token.actions.githubusercontent.com" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.repository_owner=assertion.repository_owner" \
  --attribute-condition="assertion.repository_owner == '${GITHUB_OWNER}'" \
  --project="${PROJECT_ID}" 2>/dev/null || echo "    (already exists, skipping)"

WIF_POOL_ID=$(gcloud iam workload-identity-pools describe "${WIF_POOL}" \
  --location=global --project="${PROJECT_ID}" --format="value(name)")

echo "==> Binding CI SA to Workload Identity Pool for repo: ${GITHUB_REPO}"
gcloud iam service-accounts add-iam-policy-binding "${CI_SA_EMAIL}" \
  --role="roles/iam.workloadIdentityUser" \
  --member="principalSet://iam.googleapis.com/${WIF_POOL_ID}/attribute.repository/${GITHUB_REPO}" \
  --project="${PROJECT_ID}"

# ── Cloud Run Job ─────────────────────────────────────────────────────────────
echo "==> Creating Cloud Run Job: ${JOB_NAME}"
gcloud run jobs create "${JOB_NAME}" \
  --image="${IMAGE}:latest" \
  --region="${REGION}" \
  --service-account="${RUNNER_SA_EMAIL}" \
  --task-timeout=15m \
  --max-retries=2 \
  --args="--all-cities,--yesterday" \
  --set-env-vars="GOOGLE_CLOUD_PROJECT=${PROJECT_ID}" \
  --project="${PROJECT_ID}" 2>/dev/null || \
gcloud run jobs update "${JOB_NAME}" \
  --image="${IMAGE}:latest" \
  --region="${REGION}" \
  --service-account="${RUNNER_SA_EMAIL}" \
  --task-timeout=15m \
  --max-retries=2 \
  --args="--all-cities,--yesterday" \
  --set-env-vars="GOOGLE_CLOUD_PROJECT=${PROJECT_ID}" \
  --project="${PROJECT_ID}"

# ── Cloud Run Service (API) ───────────────────────────────────────────────────
echo "==> Creating Cloud Run Service: ${SERVICE_NAME}"
gcloud run deploy "${SERVICE_NAME}" \
  --image="${IMAGE}:latest" \
  --region="${REGION}" \
  --service-account="${RUNNER_SA_EMAIL}" \
  --platform=managed \
  --memory=256Mi \
  --cpu=1 \
  --min-instances=0 \
  --max-instances=3 \
  --timeout=30s \
  --allow-unauthenticated \
  --set-env-vars="GOOGLE_CLOUD_PROJECT=${PROJECT_ID}" \
  --project="${PROJECT_ID}"

# ── Cloud Scheduler ───────────────────────────────────────────────────────────
echo "==> Setting up Cloud Scheduler for daily job"
PROJECT_NUMBER=$(gcloud projects describe "${PROJECT_ID}" --format="value(projectNumber)")
SCHEDULER_SA="service-${PROJECT_NUMBER}@gcp-sa-cloudscheduler.iam.gserviceaccount.com"

# Grant scheduler permission to invoke the Cloud Run Job
gcloud run jobs add-iam-policy-binding "${JOB_NAME}" \
  --region="${REGION}" \
  --member="serviceAccount:${SCHEDULER_SA}" \
  --role="roles/run.invoker" \
  --project="${PROJECT_ID}" 2>/dev/null || true

JOB_URI="https://${REGION}-run.googleapis.com/apis/run.googleapis.com/v1/namespaces/${PROJECT_ID}/jobs/${JOB_NAME}:run"

gcloud scheduler jobs create http "${SCHEDULER_NAME}" \
  --location="${REGION}" \
  --schedule="0 1 * * *" \
  --time-zone="UTC" \
  --uri="${JOB_URI}" \
  --message-body="{}" \
  --oauth-service-account-email="${RUNNER_SA_EMAIL}" \
  --oauth-token-scope="https://www.googleapis.com/auth/cloud-platform" \
  --description="Runs polymarket daily snapshot job for all active cities at 01:00 UTC" \
  --project="${PROJECT_ID}" 2>/dev/null || \
gcloud scheduler jobs update http "${SCHEDULER_NAME}" \
  --location="${REGION}" \
  --schedule="0 1 * * *" \
  --time-zone="UTC" \
  --uri="${JOB_URI}" \
  --message-body="{}" \
  --oauth-service-account-email="${RUNNER_SA_EMAIL}" \
  --oauth-token-scope="https://www.googleapis.com/auth/cloud-platform" \
  --description="Runs polymarket daily snapshot job for all active cities at 01:00 UTC" \
  --project="${PROJECT_ID}"

# ── Print GitHub Actions secrets ──────────────────────────────────────────────
WIF_PROVIDER_FULL=$(gcloud iam workload-identity-pools providers describe "${WIF_PROVIDER}" \
  --location=global \
  --workload-identity-pool="${WIF_POOL}" \
  --project="${PROJECT_ID}" \
  --format="value(name)")

SERVICE_URL=$(gcloud run services describe "${SERVICE_NAME}" \
  --region="${REGION}" \
  --project="${PROJECT_ID}" \
  --format="value(status.url)")

echo ""
echo "======================================================================"
echo " Setup complete."
echo "======================================================================"
echo ""
echo " GitHub Actions secrets (Settings → Secrets and variables → Actions):"
echo "   WIF_PROVIDER        = ${WIF_PROVIDER_FULL}"
echo "   WIF_SERVICE_ACCOUNT = ${CI_SA_EMAIL}"
echo ""
echo " Cloud Run Job:     ${JOB_NAME} (${REGION})"
echo "   Schedule:        daily at 01:00 UTC via ${SCHEDULER_NAME}"
echo "   Args:            --all-cities --yesterday"
echo ""
echo " Cloud Run Service: ${SERVICE_NAME}"
echo "   URL:             ${SERVICE_URL}"
echo ""
echo " Frontend admin — set backendURL to:"
echo "   ${SERVICE_URL}"
echo "======================================================================"
