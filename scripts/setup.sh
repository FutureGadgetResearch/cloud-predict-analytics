#!/usr/bin/env bash
# setup.sh — one-time infra provisioning for cloud-predict-analytics.
#
# Run this once to create all GCP resources and configure GitHub Actions auth.
#
# Prerequisites:
#   gcloud auth login
#   gcloud config set project fg-polylabs
#
# Usage:
#   ./scripts/setup.sh --github-repo=FutureGadgetLabs/cloud-predict-analytics2

set -euo pipefail

# ── Config ────────────────────────────────────────────────────────────────────
PROJECT_ID="fg-polylabs"
REGION="us-central1"
AR_REPO="polymarket"
IMAGE="us-docker.pkg.dev/${PROJECT_ID}/${AR_REPO}/polymarket"
JOB_NAME="polymarket"
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
echo "==> Creating Cloud Run job runner service account: ${RUNNER_SA}"
gcloud iam service-accounts create "${RUNNER_SA}" \
  --display-name="Polymarket Cloud Run Job Runner" \
  --project="${PROJECT_ID}" 2>/dev/null || echo "    (already exists, skipping)"

RUNNER_SA_EMAIL="${RUNNER_SA}@${PROJECT_ID}.iam.gserviceaccount.com"

echo "==> Granting BigQuery permissions to runner SA"
gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
  --member="serviceAccount:${RUNNER_SA_EMAIL}" \
  --role="roles/bigquery.dataEditor" --condition=None

gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
  --member="serviceAccount:${RUNNER_SA_EMAIL}" \
  --role="roles/bigquery.jobUser" --condition=None

echo "==> Creating GitHub Actions CI service account: ${CI_SA}"
gcloud iam service-accounts create "${CI_SA}" \
  --display-name="GitHub Actions CI" \
  --project="${PROJECT_ID}" 2>/dev/null || echo "    (already exists, skipping)"

CI_SA_EMAIL="${CI_SA}@${PROJECT_ID}.iam.gserviceaccount.com"

echo "==> Granting Artifact Registry and Cloud Run permissions to CI SA"
gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
  --member="serviceAccount:${CI_SA_EMAIL}" \
  --role="roles/artifactregistry.writer" --condition=None

gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
  --member="serviceAccount:${CI_SA_EMAIL}" \
  --role="roles/run.admin" --condition=None

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
  --task-timeout=10m \
  --max-retries=2 \
  --project="${PROJECT_ID}" 2>/dev/null || echo "    (already exists — run 'gcloud run jobs update' to change image)"

# ── Grant Cloud Scheduler permission to invoke Cloud Run ──────────────────────
echo "==> Granting Cloud Run invoker role to Cloud Scheduler"
PROJECT_NUMBER=$(gcloud projects describe "${PROJECT_ID}" --format="value(projectNumber)")
SCHEDULER_SA="service-${PROJECT_NUMBER}@gcp-sa-cloudscheduler.iam.gserviceaccount.com"

gcloud run jobs add-iam-policy-binding "${JOB_NAME}" \
  --region="${REGION}" \
  --member="serviceAccount:${SCHEDULER_SA}" \
  --role="roles/run.invoker" \
  --project="${PROJECT_ID}" 2>/dev/null || true

# ── Print GitHub Actions secrets ──────────────────────────────────────────────
WIF_PROVIDER_FULL=$(gcloud iam workload-identity-pools providers describe "${WIF_PROVIDER}" \
  --location=global \
  --workload-identity-pool="${WIF_POOL}" \
  --project="${PROJECT_ID}" \
  --format="value(name)")

echo ""
echo "======================================================================"
echo " Setup complete. Add these secrets to your GitHub repo:"
echo " (Settings → Secrets and variables → Actions → New repository secret)"
echo "======================================================================"
echo ""
echo "  WIF_PROVIDER      = ${WIF_PROVIDER_FULL}"
echo "  WIF_SERVICE_ACCOUNT = ${CI_SA_EMAIL}"
echo ""
echo "======================================================================"
echo " Cloud Run Job:  ${JOB_NAME} (${REGION})"
echo " Image:          ${IMAGE}:latest"
echo " Runner SA:      ${RUNNER_SA_EMAIL}"
echo "======================================================================"
