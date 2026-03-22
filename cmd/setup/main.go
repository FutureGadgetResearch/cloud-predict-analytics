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
	tbl := fmt.Sprintf("`%s`", project)

	// Create table with full schema (no-op if already exists).
	ddl := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s.weather.tracked_cities (
  city         STRING  NOT NULL OPTIONS (description = 'Slug form used in event slugs, e.g. london, nyc, buenos-aires'),
  source       STRING  NOT NULL OPTIONS (description = 'Service this city is tracked for, e.g. polymarket. Composite PK with city.'),
  display_name STRING  NOT NULL OPTIONS (description = 'Human-readable city name, e.g. London'),
  timezone     STRING  NOT NULL OPTIONS (description = 'IANA timezone identifier, e.g. Europe/London'),
  active       BOOL    NOT NULL OPTIONS (description = 'FALSE to pause tracking without removing the row'),
  added_date   DATE    NOT NULL OPTIONS (description = 'Date this city was added to tracking'),
  notes        STRING           OPTIONS (description = 'Optional free-text notes')
)`, tbl)

	log.Println("Creating tracked_cities table (if not exists)...")
	if err := runQuery(ctx, client, ddl); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	log.Println("tracked_cities: OK")

	// Migration: add source column to existing tables that predate this field.
	log.Println("Adding source column if missing...")
	alterDDL := fmt.Sprintf(
		"ALTER TABLE %s.weather.tracked_cities ADD COLUMN IF NOT EXISTS source STRING",
		tbl,
	)
	if err := runQuery(ctx, client, alterDDL); err != nil {
		return fmt.Errorf("alter table: %w", err)
	}

	// Backfill: any rows without a source were created before the composite key
	// was introduced — they are all Polymarket cities.
	log.Println("Backfilling source = 'polymarket' for rows with NULL source...")
	backfill := fmt.Sprintf(
		"UPDATE %s.weather.tracked_cities SET source = 'polymarket' WHERE source IS NULL",
		tbl,
	)
	if err := runQuery(ctx, client, backfill); err != nil {
		return fmt.Errorf("backfill source: %w", err)
	}
	log.Println("Backfill complete.")

	// Seed default cities (idempotent — skips rows where (city, source) already exists).
	seed := fmt.Sprintf(`
INSERT INTO %s.weather.tracked_cities (city, source, display_name, timezone, active, added_date)
SELECT city, source, display_name, timezone, active, added_date FROM UNNEST([
  STRUCT('london'       AS city, 'polymarket' AS source, 'London'        AS display_name, 'Europe/London'                   AS timezone, TRUE AS active, DATE '2026-01-01' AS added_date),
  STRUCT('singapore',            'polymarket',            'Singapore',                      'Asia/Singapore',                  TRUE, DATE '2026-01-01'),
  STRUCT('paris',                'polymarket',            'Paris',                          'Europe/Paris',                    TRUE, DATE '2026-02-01'),
  STRUCT('tokyo',                'polymarket',            'Tokyo',                          'Asia/Tokyo',                      TRUE, DATE '2026-02-01'),
  STRUCT('nyc',                  'polymarket',            'New York',                       'America/New_York',                TRUE, DATE '2026-02-01'),
  STRUCT('chicago',              'polymarket',            'Chicago',                        'America/Chicago',                 TRUE, DATE '2026-02-01'),
  STRUCT('miami',                'polymarket',            'Miami',                          'America/New_York',                TRUE, DATE '2026-02-01'),
  STRUCT('dallas',               'polymarket',            'Dallas',                         'America/Chicago',                 TRUE, DATE '2026-02-01'),
  STRUCT('toronto',              'polymarket',            'Toronto',                        'America/Toronto',                 TRUE, DATE '2026-02-01'),
  STRUCT('seoul',                'polymarket',            'Seoul',                          'Asia/Seoul',                      TRUE, DATE '2026-02-03'),
  STRUCT('ankara',               'polymarket',            'Ankara',                         'Europe/Istanbul',                 TRUE, DATE '2026-02-03'),
  STRUCT('buenos-aires',         'polymarket',            'Buenos Aires',                   'America/Argentina/Buenos_Aires',  TRUE, DATE '2026-02-03')
]) t
WHERE NOT EXISTS (
  SELECT 1 FROM %s.weather.tracked_cities WHERE city = t.city AND source = t.source
)`,
		tbl,
		tbl,
	)

	log.Println("Seeding tracked_cities with default cities (skipping existing rows)...")
	if err := runQuery(ctx, client, seed); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	log.Println("Seed complete.")
	return nil
}

func runQuery(ctx context.Context, client *bigquery.Client, sql string) error {
	job, err := client.Query(sql).Run(ctx)
	if err != nil {
		return err
	}
	_, err = job.Wait(ctx)
	return err
}
