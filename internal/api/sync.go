package api

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

// syncData exports tracked_cities and polymarket_snapshots from BigQuery to
// GCS, and optionally to GitHub if GITHUB_TOKEN is configured.
// POST /sync — requires auth.
func (s *Server) syncData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	type fileResult struct {
		Count int    `json:"count"`
		GCS   bool   `json:"gcs"`
		Error string `json:"error,omitempty"`
	}

	result := map[string]any{
		"synced_at":  time.Now().UTC().Format(time.RFC3339),
		"gcs_bucket": s.gcsBucket,
		"github":     false,
	}

	// --- Tracked cities ---
	cities, err := s.fetchTrackedCities(ctx)
	citiesResult := fileResult{Count: len(cities)}
	if err != nil {
		citiesResult.Error = err.Error()
	} else {
		citiesJSONL, err := toJSONL(cities, func(c City) any {
			var notes any
			if c.Notes != "" {
				notes = c.Notes
			}
			return map[string]any{
				"city": c.City, "source": c.Source, "display_name": c.DisplayName,
				"timezone": c.Timezone, "active": c.Active,
				"added_date": c.AddedDate, "notes": notes,
			}
		})
		if err != nil {
			citiesResult.Error = err.Error()
		} else if gcsErr := s.writeGCS(ctx, "data/tracked_cities.jsonl", "application/x-ndjson", citiesJSONL); gcsErr != nil {
			citiesResult.Error = gcsErr.Error()
		} else {
			citiesResult.GCS = true
		}
	}
	result["tracked_cities"] = citiesResult

	// --- Snapshots ---
	days := 90
	snapshots, err := s.fetchSnapshots(ctx, days)
	snapshotsResult := fileResult{Count: len(snapshots)}
	var snapshotsJSONL []byte
	if err != nil {
		snapshotsResult.Error = err.Error()
	} else {
		snapshotsJSONL, err = toJSONL(snapshots, func(s Snapshot) any { return s })
		if err != nil {
			snapshotsResult.Error = err.Error()
		} else if gcsErr := s.writeGCS(ctx, "data/snapshots.jsonl", "application/x-ndjson", snapshotsJSONL); gcsErr != nil {
			snapshotsResult.Error = gcsErr.Error()
		} else {
			snapshotsResult.GCS = true
		}
	}
	result["snapshots"] = snapshotsResult

	// --- GitHub push (both files, if token is set) ---
	if s.githubToken != "" && s.githubDataRepo != "" {
		cities2, _ := toJSONL(cities, func(c City) any {
			var notes any
			if c.Notes != "" {
				notes = c.Notes
			}
			return map[string]any{
				"city": c.City, "source": c.Source, "display_name": c.DisplayName,
				"timezone": c.Timezone, "active": c.Active,
				"added_date": c.AddedDate, "notes": notes,
			}
		})
		var ghErrors []string
		if err := s.pushToGitHub(ctx, "data/tracked_cities.jsonl", cities2); err != nil {
			ghErrors = append(ghErrors, "tracked_cities: "+err.Error())
		}
		if snapshotsJSONL != nil {
			if err := s.pushToGitHub(ctx, "data/snapshots.jsonl", snapshotsJSONL); err != nil {
				ghErrors = append(ghErrors, "snapshots: "+err.Error())
			}
		}
		if len(ghErrors) == 0 {
			result["github"] = true
		} else {
			result["github_errors"] = ghErrors
		}
	} else {
		result["github_skipped"] = "GITHUB_TOKEN or GITHUB_DATA_REPO not configured"
	}

	jsonOK(w, result)
}

func (s *Server) fetchTrackedCities(ctx context.Context) ([]City, error) {
	q := s.bq.Query(fmt.Sprintf(`
		SELECT city, source, display_name, timezone, active,
		       CAST(added_date AS STRING) AS added_date,
		       IFNULL(notes, '') AS notes
		FROM %s ORDER BY source, city
	`, s.table("tracked_cities")))

	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}
	var cities []City
	for {
		var row []bigquery.Value
		if err := it.Next(&row); err == iterator.Done {
			break
		} else if err != nil {
			return nil, err
		}
		cities = append(cities, City{
			City: fmt.Sprint(row[0]), Source: fmt.Sprint(row[1]),
			DisplayName: fmt.Sprint(row[2]), Timezone: fmt.Sprint(row[3]),
			Active: row[4].(bool), AddedDate: fmt.Sprint(row[5]),
			Notes: fmt.Sprint(row[6]),
		})
	}
	return cities, nil
}

func (s *Server) fetchSnapshots(ctx context.Context, days int) ([]Snapshot, error) {
	q := s.bq.Query(fmt.Sprintf(`
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
	`, s.table("polymarket_snapshots")))
	q.Parameters = []bigquery.QueryParameter{{Name: "days", Value: days}}

	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	for {
		var row []bigquery.Value
		if err := it.Next(&row); err == iterator.Done {
			break
		} else if err != nil {
			return nil, err
		}
		snaps = append(snaps, Snapshot{
			City: fmt.Sprint(row[0]), Date: fmt.Sprint(row[1]),
			Timestamp: fmt.Sprint(row[2]), TempThreshold: toFloat(row[3]),
			YesCost: toFloat(row[4]), NoCost: toFloat(row[5]),
			BestBid: toFloatPtr(row[6]), BestAsk: toFloatPtr(row[7]),
			Spread: toFloatPtr(row[8]), Volume24h: toFloatPtr(row[9]),
			VolumeTotal: toFloatPtr(row[10]), Liquidity: toFloatPtr(row[11]),
			EventSlug: fmt.Sprint(row[12]), MarketEndDate: toStringPtr(row[13]),
		})
	}
	return snaps, nil
}

// toJSONL serialises a slice to newline-delimited JSON using a mapper func.
func toJSONL[T any](items []T, mapper func(T) any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, item := range items {
		if err := enc.Encode(mapper(item)); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func (s *Server) writeGCS(ctx context.Context, object, contentType string, data []byte) error {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating storage client: %w", err)
	}
	defer client.Close()

	wc := client.Bucket(s.gcsBucket).Object(object).NewWriter(ctx)
	wc.ContentType = contentType
	if _, err := io.Copy(wc, bytes.NewReader(data)); err != nil {
		wc.Close()
		return err
	}
	return wc.Close()
}

// pushToGitHub creates or updates a file in the configured data repo.
func (s *Server) pushToGitHub(ctx context.Context, path string, content []byte) error {
	owner, repo, found := strings.Cut(s.githubDataRepo, "/")
	if !found {
		return fmt.Errorf("invalid GITHUB_DATA_REPO format, expected owner/repo")
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", owner, repo, path)

	// Get the current file SHA (needed for updates; absent for new files).
	sha := ""
	getReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	getReq.Header.Set("Authorization", "Bearer "+s.githubToken)
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

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	putReq.Header.Set("Authorization", "Bearer "+s.githubToken)
	putReq.Header.Set("Accept", "application/vnd.github+json")
	putReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var errBody struct {
			Message string `json:"message"`
		}
		json.NewDecoder(resp.Body).Decode(&errBody)
		return fmt.Errorf("GitHub API %d: %s", resp.StatusCode, errBody.Message)
	}
	return nil
}
