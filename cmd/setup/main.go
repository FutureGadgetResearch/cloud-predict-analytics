// setup creates or updates BigQuery resources for cloud-predict-analytics.
// Run once:  go run ./cmd/setup
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"cloud.google.com/go/bigquery"
)

func main() {
	ctx := context.Background()

	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		project = "fg-polylabs"
	}

	client, err := bigquery.NewClient(ctx, project)
	if err != nil {
		log.Fatalf("bigquery.NewClient: %v", err)
	}
	defer client.Close()

	if err := createTrackedCities(ctx, client, project); err != nil {
		log.Fatalf("createTrackedCities: %v", err)
	}
}

func createTrackedCities(ctx context.Context, client *bigquery.Client, project string) error {
	ddl := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s.weather.tracked_cities (
  city         STRING  NOT NULL OPTIONS (description = 'Slug form used in Polymarket event slugs, e.g. london, new-york'),
  display_name STRING  NOT NULL OPTIONS (description = 'Human-readable city name, e.g. London'),
  timezone     STRING  NOT NULL OPTIONS (description = 'IANA timezone identifier, e.g. Europe/London'),
  active       BOOL    NOT NULL OPTIONS (description = 'FALSE to pause tracking without removing the row'),
  added_date   DATE    NOT NULL OPTIONS (description = 'Date this city was added to tracking'),
  notes        STRING           OPTIONS (description = 'Optional free-text notes')
)`, fmt.Sprintf("`%s`", project))

	log.Println("Creating tracked_cities table (if not exists)...")
	job, err := client.Query(ddl).Run(ctx)
	if err != nil {
		return err
	}
	if _, err := job.Wait(ctx); err != nil {
		return err
	}
	log.Println("tracked_cities: OK")

	seed := fmt.Sprintf(`
INSERT INTO %s.weather.tracked_cities (city, display_name, timezone, active, added_date)
SELECT city, display_name, timezone, active, added_date FROM UNNEST([
  STRUCT('london'       AS city, 'London'        AS display_name, 'Europe/London'                   AS timezone, TRUE AS active, DATE '2026-01-01' AS added_date),
  STRUCT('singapore',            'Singapore',                      'Asia/Singapore',                  TRUE, DATE '2026-01-01'),
  STRUCT('paris',                'Paris',                          'Europe/Paris',                    TRUE, DATE '2026-02-01'),
  STRUCT('tokyo',                'Tokyo',                          'Asia/Tokyo',                      TRUE, DATE '2026-02-01'),
  STRUCT('nyc',                  'New York',                       'America/New_York',                TRUE, DATE '2026-02-01'),
  STRUCT('chicago',              'Chicago',                        'America/Chicago',                 TRUE, DATE '2026-02-01'),
  STRUCT('miami',                'Miami',                          'America/New_York',                TRUE, DATE '2026-02-01'),
  STRUCT('dallas',               'Dallas',                        'America/Chicago',                  TRUE, DATE '2026-02-01'),
  STRUCT('toronto',              'Toronto',                        'America/Toronto',                 TRUE, DATE '2026-02-01'),
  STRUCT('seoul',                'Seoul',                          'Asia/Seoul',                      TRUE, DATE '2026-02-03'),
  STRUCT('ankara',               'Ankara',                         'Europe/Istanbul',                 TRUE, DATE '2026-02-03'),
  STRUCT('buenos-aires',         'Buenos Aires',                   'America/Argentina/Buenos_Aires',  TRUE, DATE '2026-02-03')
]) t
WHERE NOT EXISTS (
  SELECT 1 FROM %s.weather.tracked_cities WHERE city = t.city
)`,
		fmt.Sprintf("`%s`", project),
		fmt.Sprintf("`%s`", project),
	)

	log.Println("Seeding tracked_cities with default cities (skipping existing rows)...")
	job2, err := client.Query(seed).Run(ctx)
	if err != nil {
		return err
	}
	if _, err := job2.Wait(ctx); err != nil {
		return err
	}
	log.Println("Seed complete.")
	return nil
}
