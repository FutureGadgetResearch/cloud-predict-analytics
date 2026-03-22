// sync is a Cloud Run Job that exports BigQuery data to GCS and GitHub.
// Run on-demand or scheduled daily after the polymarket fetch job.
//
// Required env vars:
//
//	GOOGLE_CLOUD_PROJECT   GCP project (default: fg-polylabs)
//	GCS_DATA_BUCKET        target GCS bucket (default: fg-polylabs-weather-data)
//
// Optional env vars:
//
//	GITHUB_TOKEN           PAT with contents:write on the data repo
//	GITHUB_DATA_REPO       owner/repo (default: FG-PolyLabs/cloud-predict-analytics-data)
//	SYNC_SNAPSHOT_DAYS     days of snapshot history to export (default: 90)
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"

	"cloud.google.com/go/bigquery"
	"github.com/FutureGadgetLabs/cloud-predict-analytics/internal/syncer"
)

func main() {
	ctx := context.Background()

	project := getenv("GOOGLE_CLOUD_PROJECT", "fg-polylabs")

	bq, err := bigquery.NewClient(ctx, project)
	if err != nil {
		log.Fatalf("bigquery.NewClient: %v", err)
	}
	defer bq.Close()

	days := 90
	if v := os.Getenv("SYNC_SNAPSHOT_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}

	result := syncer.Run(ctx, bq, syncer.Config{
		Project:      project,
		GCSBucket:    getenv("GCS_DATA_BUCKET", "fg-polylabs-weather-data"),
		GitHubToken:  os.Getenv("GITHUB_TOKEN"),
		GitHubRepo:   getenv("GITHUB_DATA_REPO", "FG-PolyLabs/cloud-predict-analytics-data"),
		SnapshotDays: days,
	})

	out, _ := json.MarshalIndent(result, "", "  ")
	log.Printf("sync result:\n%s", out)

	if result.TrackedCities.Error != "" || result.Snapshots.Error != "" {
		log.Fatal("sync completed with errors")
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
