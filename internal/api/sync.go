package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

// syncData exports tracked_cities from BigQuery to GCS as JSONL.
// POST /sync — requires auth.
func (s *Server) syncData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	q := s.bq.Query(fmt.Sprintf(`
		SELECT city, source, display_name, timezone, active,
		       CAST(added_date AS STRING) AS added_date,
		       IFNULL(notes, '') AS notes
		FROM %s
		ORDER BY source, city
	`, s.table("tracked_cities")))

	it, err := q.Read(ctx)
	if err != nil {
		jsonError(w, "reading tracked_cities: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var cities []City
	for {
		var row []bigquery.Value
		if err := it.Next(&row); err == iterator.Done {
			break
		} else if err != nil {
			jsonError(w, "iterating tracked_cities: "+err.Error(), http.StatusInternalServerError)
			return
		}
		cities = append(cities, City{
			City:        fmt.Sprint(row[0]),
			Source:      fmt.Sprint(row[1]),
			DisplayName: fmt.Sprint(row[2]),
			Timezone:    fmt.Sprint(row[3]),
			Active:      row[4].(bool),
			AddedDate:   fmt.Sprint(row[5]),
			Notes:       fmt.Sprint(row[6]),
		})
	}

	gcsErr := s.writeTrackedCitiesToGCS(ctx, cities)

	result := map[string]any{
		"synced_at":      time.Now().UTC().Format(time.RFC3339),
		"tracked_cities": len(cities),
		"gcs":            gcsErr == nil,
		"gcs_bucket":     s.gcsBucket,
	}
	if gcsErr != nil {
		result["gcs_error"] = gcsErr.Error()
	}

	jsonOK(w, result)
}

func (s *Server) writeTrackedCitiesToGCS(ctx context.Context, cities []City) error {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating storage client: %w", err)
	}
	defer client.Close()

	wc := client.Bucket(s.gcsBucket).Object("data/tracked_cities.jsonl").NewWriter(ctx)
	wc.ContentType = "application/x-ndjson"

	enc := json.NewEncoder(wc)
	for _, c := range cities {
		var notes any
		if c.Notes != "" {
			notes = c.Notes
		}
		row := map[string]any{
			"city":         c.City,
			"source":       c.Source,
			"display_name": c.DisplayName,
			"timezone":     c.Timezone,
			"active":       c.Active,
			"added_date":   c.AddedDate,
			"notes":        notes,
		}
		if err := enc.Encode(row); err != nil {
			wc.Close()
			return fmt.Errorf("encoding row: %w", err)
		}
	}

	return wc.Close()
}
