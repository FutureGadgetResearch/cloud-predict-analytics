// Package syncer exports BigQuery data (tracked_cities + snapshots) to GCS
// and optionally to GitHub. It is used by both the API handler (POST /sync)
// and the standalone weather-sync Cloud Run Job (cmd/sync).
package syncer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

// Config holds the runtime configuration for a sync run.
type Config struct {
	Project      string // GCP project, e.g. fg-polylabs
	GCSBucket    string // target bucket, e.g. fg-polylabs-weather-data
	GitHubToken  string // PAT with contents:write on the data repo; empty = skip
	GitHubRepo   string // owner/repo, e.g. FG-PolyLabs/cloud-predict-analytics-data
	SnapshotDays int    // how many days back to export (default 90)
}

// FileResult records the outcome for one exported file.
type FileResult struct {
	Count int    `json:"count"`
	GCS   bool   `json:"gcs"`
	Error string `json:"error,omitempty"`
}

// Result is returned by Run.
type Result struct {
	SyncedAt      string     `json:"synced_at"`
	GCSBucket     string     `json:"gcs_bucket"`
	TrackedCities FileResult `json:"tracked_cities"`
	Snapshots     FileResult `json:"snapshots"`
	GitHub        bool       `json:"github"`
	GitHubSkipped string     `json:"github_skipped,omitempty"`
	GitHubErrors  []string   `json:"github_errors,omitempty"`
}

// Run performs the full export: tracked_cities and snapshots → GCS (+ GitHub).
func Run(ctx context.Context, bq *bigquery.Client, cfg Config) *Result {
	if cfg.SnapshotDays <= 0 {
		cfg.SnapshotDays = 90
	}

	result := &Result{
		SyncedAt:  time.Now().UTC().Format(time.RFC3339),
		GCSBucket: cfg.GCSBucket,
	}

	tbl := func(name string) string {
		return fmt.Sprintf("`%s.weather.%s`", cfg.Project, name)
	}

	// --- Tracked cities ---
	cities, citiesJSONL, err := exportTrackedCities(ctx, bq, tbl("tracked_cities"))
	result.TrackedCities.Count = len(cities)
	if err != nil {
		result.TrackedCities.Error = err.Error()
	} else if gcsErr := writeGCS(ctx, cfg.GCSBucket, "data/tracked_cities.jsonl", "application/x-ndjson", citiesJSONL); gcsErr != nil {
		result.TrackedCities.Error = gcsErr.Error()
	} else {
		result.TrackedCities.GCS = true
	}

	// --- Snapshots ---
	snapshotsJSONL, snapshotCount, err := exportSnapshots(ctx, bq, tbl("polymarket_snapshots"), cfg.SnapshotDays)
	result.Snapshots.Count = snapshotCount
	if err != nil {
		result.Snapshots.Error = err.Error()
	} else if gcsErr := writeGCS(ctx, cfg.GCSBucket, "data/snapshots.jsonl", "application/x-ndjson", snapshotsJSONL); gcsErr != nil {
		result.Snapshots.Error = gcsErr.Error()
	} else {
		result.Snapshots.GCS = true
	}

	// --- GitHub ---
	if cfg.GitHubToken == "" || cfg.GitHubRepo == "" {
		result.GitHubSkipped = "GITHUB_TOKEN or GITHUB_DATA_REPO not configured"
		return result
	}
	var ghErrs []string
	if citiesJSONL != nil {
		if err := pushToGitHub(ctx, cfg.GitHubToken, cfg.GitHubRepo, "data/tracked_cities.jsonl", citiesJSONL); err != nil {
			ghErrs = append(ghErrs, "tracked_cities: "+err.Error())
		}
	}
	if snapshotsJSONL != nil {
		if err := pushToGitHub(ctx, cfg.GitHubToken, cfg.GitHubRepo, "data/snapshots.jsonl", snapshotsJSONL); err != nil {
			ghErrs = append(ghErrs, "snapshots: "+err.Error())
		}
	}
	if len(ghErrs) == 0 {
		result.GitHub = true
	} else {
		result.GitHubErrors = ghErrs
	}
	return result
}

// --- BigQuery readers ---

type cityRow struct {
	City        string
	Source      string
	DisplayName string
	Timezone    string
	Active      bool
	AddedDate   string
	Notes       string
}

func exportTrackedCities(ctx context.Context, bq *bigquery.Client, table string) ([]cityRow, []byte, error) {
	q := bq.Query(fmt.Sprintf(`
		SELECT city, source, display_name, timezone, active,
		       CAST(added_date AS STRING) AS added_date,
		       IFNULL(notes, '') AS notes
		FROM %s ORDER BY source, city
	`, table))

	it, err := q.Read(ctx)
	if err != nil {
		return nil, nil, err
	}

	var rows []cityRow
	for {
		var row []bigquery.Value
		if err := it.Next(&row); err == iterator.Done {
			break
		} else if err != nil {
			return nil, nil, err
		}
		rows = append(rows, cityRow{
			City: fmt.Sprint(row[0]), Source: fmt.Sprint(row[1]),
			DisplayName: fmt.Sprint(row[2]), Timezone: fmt.Sprint(row[3]),
			Active: row[4].(bool), AddedDate: fmt.Sprint(row[5]),
			Notes: fmt.Sprint(row[6]),
		})
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		var notes any
		if r.Notes != "" {
			notes = r.Notes
		}
		enc.Encode(map[string]any{
			"city": r.City, "source": r.Source, "display_name": r.DisplayName,
			"timezone": r.Timezone, "active": r.Active,
			"added_date": r.AddedDate, "notes": notes,
		})
	}
	return rows, buf.Bytes(), nil
}

func exportSnapshots(ctx context.Context, bq *bigquery.Client, table string, days int) ([]byte, int, error) {
	q := bq.Query(fmt.Sprintf(`
		SELECT
		  city, CAST(date AS STRING) AS date,
		  CAST(timestamp AS STRING) AS timestamp,
		  temp_threshold, yes_cost, no_cost,
		  best_bid, best_ask, spread,
		  volume_24h, volume_total, liquidity,
		  event_slug, market_end_date
		FROM %s
		WHERE date >= DATE_SUB(CURRENT_DATE(), INTERVAL @days DAY)
		ORDER BY city, date, temp_threshold, timestamp
	`, table))
	q.Parameters = []bigquery.QueryParameter{{Name: "days", Value: days}}

	it, err := q.Read(ctx)
	if err != nil {
		return nil, 0, err
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	count := 0
	for {
		var row []bigquery.Value
		if err := it.Next(&row); err == iterator.Done {
			break
		} else if err != nil {
			return nil, count, err
		}
		enc.Encode(map[string]any{
			"city": fmt.Sprint(row[0]), "date": fmt.Sprint(row[1]),
			"timestamp": fmt.Sprint(row[2]), "temp_threshold": toFloat(row[3]),
			"yes_cost": toFloat(row[4]), "no_cost": toFloat(row[5]),
			"best_bid": toFloatPtr(row[6]), "best_ask": toFloatPtr(row[7]),
			"spread": toFloatPtr(row[8]), "volume_24h": toFloatPtr(row[9]),
			"volume_total": toFloatPtr(row[10]), "liquidity": toFloatPtr(row[11]),
			"event_slug": fmt.Sprint(row[12]), "market_end_date": toStringPtr(row[13]),
		})
		count++
	}
	return buf.Bytes(), count, nil
}

// --- GCS writer ---

func writeGCS(ctx context.Context, bucket, object, contentType string, data []byte) error {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("storage client: %w", err)
	}
	defer client.Close()
	wc := client.Bucket(bucket).Object(object).NewWriter(ctx)
	wc.ContentType = contentType
	if _, err := io.Copy(wc, bytes.NewReader(data)); err != nil {
		wc.Close()
		return err
	}
	return wc.Close()
}

// --- GitHub writer ---

func pushToGitHub(ctx context.Context, token, repo, path string, content []byte) error {
	owner, repoName, found := strings.Cut(repo, "/")
	if !found {
		return fmt.Errorf("invalid repo format, expected owner/repo")
	}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", owner, repoName, path)

	// Get current file SHA for updates.
	sha := ""
	getReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	getReq.Header.Set("Authorization", "Bearer "+token)
	getReq.Header.Set("Accept", "application/vnd.github+json")
	if resp, err := http.DefaultClient.Do(getReq); err == nil && resp.StatusCode == http.StatusOK {
		var meta struct {
			SHA string `json:"sha"`
		}
		json.NewDecoder(resp.Body).Decode(&meta)
		resp.Body.Close()
		sha = meta.SHA
	}

	payload := map[string]any{
		"message": fmt.Sprintf("sync: update %s [skip ci]", path),
		"content": base64.StdEncoding.EncodeToString(content),
	}
	if sha != "" {
		payload["sha"] = sha
	}
	body, _ := json.Marshal(payload)

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	putReq.Header.Set("Authorization", "Bearer "+token)
	putReq.Header.Set("Accept", "application/vnd.github+json")
	putReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var errBody struct{ Message string `json:"message"` }
		json.NewDecoder(resp.Body).Decode(&errBody)
		return fmt.Errorf("GitHub API %d: %s", resp.StatusCode, errBody.Message)
	}
	return nil
}

// --- BQ value helpers (mirrors internal/api) ---

func toFloat(v bigquery.Value) float64 {
	if v == nil {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	}
	return 0
}

func toFloatPtr(v bigquery.Value) *float64 {
	if v == nil {
		return nil
	}
	f := toFloat(v)
	return &f
}

func toStringPtr(v bigquery.Value) *string {
	if v == nil {
		return nil
	}
	s := fmt.Sprint(v)
	return &s
}
