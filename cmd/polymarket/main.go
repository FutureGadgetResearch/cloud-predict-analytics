// polymarket is a CLI runner that pulls Polymarket prediction market data and
// lands it into BigQuery. Pass --dry-run to print JSONL to stdout instead.
//
// Single city:
//
//	polymarket --city=london --date=2026-03-10
//	polymarket --slug=highest-temperature-in-london-on-march-6-2026 --date=2026-03-06
//
// All active cities (for scheduled runs):
//
//	polymarket --all-cities --yesterday
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/FutureGadgetLabs/cloud-predict-analytics/internal/polymarket"
	"google.golang.org/api/iterator"
)

const bqProject = "fg-polylabs"

func main() {
	city      := flag.String("city", "", "City name for weather events (e.g., london, new-york); not required when --slug is set")
	date      := flag.String("date", "", "Event date in YYYY-MM-DD format (required unless --yesterday or --date-range is set)")
	dateRange := flag.String("date-range", "", "Date range for backfill in YYYY-MM-DD:YYYY-MM-DD format (inclusive)")
	yesterday := flag.Bool("yesterday", false, "Use yesterday's UTC date as the event date (overrides --date)")
	slug      := flag.String("slug", "", "Polymarket event slug; overrides auto slug construction from --city/--date")
	temp      := flag.Float64("temp", 0, "Temperature threshold in °C to filter a specific market (0 = all markets)")
	fidelity  := flag.Int("fidelity", 60, "Price snapshot granularity in minutes (e.g., 60=hourly, 1=per-minute)")
	dryRun    := flag.Bool("dry-run", false, "Print rows as JSONL to stdout instead of loading to BigQuery")
	noVolume  := flag.Bool("no-volume", false, "Store NULL for volume/liquidity/bid-ask fields (use for historical backfills)")
	allCities := flag.Bool("all-cities", false, "Run for all active cities in the tracked_cities BQ table (use with --yesterday or --date-range)")
	flag.Parse()

	if *yesterday {
		y := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
		date = &y
	}

	ctx := context.Background()

	// Date range mode: iterate each day in the range.
	if *dateRange != "" {
		parts := strings.SplitN(*dateRange, ":", 2)
		if len(parts) != 2 {
			fmt.Fprintln(os.Stderr, "Error: --date-range must be YYYY-MM-DD:YYYY-MM-DD")
			os.Exit(1)
		}
		from, err1 := time.Parse("2006-01-02", parts[0])
		to, err2   := time.Parse("2006-01-02", parts[1])
		if err1 != nil || err2 != nil || from.After(to) {
			fmt.Fprintln(os.Stderr, "Error: invalid --date-range; expected YYYY-MM-DD:YYYY-MM-DD with from <= to")
			os.Exit(1)
		}
		if !*allCities && *city == "" && *slug == "" {
			fmt.Fprintln(os.Stderr, "Error: --date-range requires --all-cities or --city")
			os.Exit(1)
		}
		log.Printf("backfill: %s → %s", parts[0], parts[1])
		var failures []string
		for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
			ds := d.Format("2006-01-02")
			log.Printf("=== date %s ===", ds)
			if *allCities {
				runAllCities(ctx, ds, *fidelity, *dryRun, *noVolume)
			} else {
				if err := processCity(ctx, *city, ds, *slug, *temp, *fidelity, *dryRun, *noVolume); err != nil {
					log.Printf("[%s] FAILED: %v", ds, err)
					failures = append(failures, fmt.Sprintf("%s: %v", ds, err))
				}
			}
		}
		if len(failures) > 0 {
			log.Fatalf("backfill completed with %d failures:\n  %s", len(failures), strings.Join(failures, "\n  "))
		}
		log.Printf("backfill complete")
		return
	}

	if *date == "" {
		fmt.Fprintln(os.Stderr, "Error: provide --date=YYYY-MM-DD, --yesterday, or --date-range=YYYY-MM-DD:YYYY-MM-DD")
		os.Exit(1)
	}

	if *allCities {
		runAllCities(ctx, *date, *fidelity, *dryRun, *noVolume)
		return
	}

	if *slug == "" && *city == "" {
		fmt.Fprintln(os.Stderr, "Error: provide --city, --slug, or --all-cities")
		os.Exit(1)
	}

	if err := processCity(ctx, *city, *date, *slug, *temp, *fidelity, *dryRun, *noVolume); err != nil {
		log.Fatalf("city %q: %v", *city, err)
	}
}

// runAllCities queries the tracked_cities reference table and processes each active city.
func runAllCities(ctx context.Context, date string, fidelity int, dryRun, noVolume bool) {
	bq, err := bigquery.NewClient(ctx, bqProject)
	if err != nil {
		log.Fatalf("bigquery.NewClient: %v", err)
	}
	defer bq.Close()

	it, err := bq.Query(fmt.Sprintf(
		"SELECT city FROM `%s.weather.tracked_cities` WHERE active = TRUE AND source = 'polymarket' ORDER BY city",
		bqProject,
	)).Read(ctx)
	if err != nil {
		log.Fatalf("querying tracked_cities: %v", err)
	}

	var cities []string
	for {
		var row []bigquery.Value
		if err := it.Next(&row); err == iterator.Done {
			break
		} else if err != nil {
			log.Fatalf("reading tracked_cities: %v", err)
		}
		cities = append(cities, fmt.Sprint(row[0]))
	}

	if len(cities) == 0 {
		log.Fatal("no active cities found in tracked_cities — nothing to do")
	}
	log.Printf("running for %d active cities: %v", len(cities), cities)

	var failures []string
	for _, c := range cities {
		log.Printf("==> [%s] starting", c)
		if err := processCity(ctx, c, date, "", 0, fidelity, dryRun, noVolume); err != nil {
			log.Printf("==> [%s] FAILED: %v", c, err)
			failures = append(failures, fmt.Sprintf("%s: %v", c, err))
		} else {
			log.Printf("==> [%s] done", c)
		}
	}

	if len(failures) > 0 {
		log.Fatalf("%d/%d cities failed:\n  %s", len(failures), len(cities), strings.Join(failures, "\n  "))
	}
	log.Printf("all %d cities completed successfully", len(cities))
}

// processCity fetches Polymarket data for one city/date and loads it to BigQuery.
func processCity(ctx context.Context, city, date, slug string, temp float64, fidelity int, dryRun, noVolume bool) error {
	eventDate, err := time.Parse("2006-01-02", date)
	if err != nil {
		return fmt.Errorf("invalid date %q: expected YYYY-MM-DD", date)
	}

	eventSlug := slug
	if eventSlug == "" {
		eventSlug = buildEventSlug(city, eventDate)
	}
	log.Printf("[%s] event slug: %s", city, eventSlug)

	cityLabel := normalizeCity(city)
	client := polymarket.NewClient()

	event, err := client.GetEventBySlug(eventSlug)
	if err != nil {
		return fmt.Errorf("could not find event for slug %q: %w", eventSlug, err)
	}
	if cityLabel == "" {
		cityLabel = extractCityFromTitle(event.Title)
	}

	markets := event.Markets
	if len(markets) == 0 {
		return fmt.Errorf("no markets found for event slug %q", eventSlug)
	}
	log.Printf("[%s] found %d market(s)", city, len(markets))

	// Hydrate any markets missing clobTokenIds.
	for i, m := range markets {
		if len(m.ClobTokenIDs) == 0 && m.ID != "" {
			full, err := client.GetMarketByID(m.ID)
			if err != nil {
				log.Printf("[%s] warning: could not hydrate market %s: %v", city, m.ID, err)
				continue
			}
			markets[i] = *full
		}
	}

	if temp != 0 {
		markets = filterMarketsByTemp(markets, temp)
		if len(markets) == 0 {
			return fmt.Errorf("no market found matching temperature threshold %.1f°C", temp)
		}
	}

	dayEnd := time.Date(eventDate.Year(), eventDate.Month(), eventDate.Day()+1, 0, 0, 0, 0, time.UTC)
	var snapshots []polymarket.PredictionSnapshot

	for _, market := range markets {
		threshold := extractTempThreshold(market.Question)

		histStart := time.Date(eventDate.Year(), eventDate.Month(), eventDate.Day(), 0, 0, 0, 0, time.UTC)
		if market.StartDateIso != "" {
			if t, err := time.Parse("2006-01-02", market.StartDateIso[:10]); err == nil {
				histStart = t
			}
		}

		log.Printf("[%s] pulling history for market: %s (%.1f°C) from %s to %s",
			city, market.Question, threshold,
			histStart.Format("2006-01-02"), eventDate.Format("2006-01-02"))

		yesHistory, err := client.GetPriceHistory(market.YesTokenID(), histStart.Unix(), dayEnd.Unix(), fidelity)
		if err != nil {
			log.Printf("[%s] warning: could not fetch YES history for market %s: %v", city, market.ConditionID, err)
			continue
		}

		noHistory, err := client.GetPriceHistory(market.NoTokenID(), histStart.Unix(), dayEnd.Unix(), fidelity)
		if err != nil {
			log.Printf("[%s] warning: could not fetch NO history, deriving from YES: %v", city, err)
			noHistory = deriveNoHistory(yesHistory)
		}

		// Filter 1: skip markets with no trading activity.
		// Bypass when --no-volume is set: resolved markets return 0 for these fields
		// but price history is still available via the CLOB API.
		if !noVolume && market.VolumeTotal == 0 && market.Liquidity == 0 {
			log.Printf("[%s] skipping market %.1f°C — no trading activity", city, threshold)
			continue
		}

		var marketEnd time.Time
		if market.EndDateIso != "" {
			marketEnd, _ = time.Parse("2006-01-02", market.EndDateIso[:10])
			marketEnd = marketEnd.Add(24 * time.Hour)
		}

		noPriceByTs := make(map[int64]float64, len(noHistory))
		for _, pt := range noHistory {
			noPriceByTs[pt.T] = pt.P
		}

		var lastYesCost float64 = -1

		for _, pt := range yesHistory {
			ts := time.Unix(pt.T, 0).UTC().Round(15 * time.Minute)

			// Filter 2: skip rows after market resolution.
			if !marketEnd.IsZero() && ts.After(marketEnd) {
				continue
			}
			// Filter 3: skip effectively-resolved prices.
			if pt.P >= 0.98 || pt.P <= 0.02 {
				continue
			}
			// Filter 4: skip unchanged prices (< 0.05% movement).
			if math.Abs(pt.P-lastYesCost) < 0.0005 {
				continue
			}
			lastYesCost = pt.P

			noPrice := noPriceByTs[pt.T]
			if noPrice == 0 {
				noPrice = 1.0 - pt.P
			}

			snap := polymarket.PredictionSnapshot{
				City:          cityLabel,
				Date:          date,
				Timestamp:     ts,
				TempThreshold: threshold,
				YesCost:       pt.P,
				NoCost:        noPrice,
				EventSlug:     eventSlug,
				MarketEndDate: market.EndDateIso,
			}
			if !noVolume {
				bid := market.BestBid
				ask := market.BestAsk
				spr := market.BestAsk - market.BestBid
				vol24 := market.Volume24hr
				volTotal := market.VolumeTotal
				liq := market.Liquidity
				snap.BestBid = &bid
				snap.BestAsk = &ask
				snap.Spread = &spr
				snap.Volume24h = &vol24
				snap.VolumeTotal = &volTotal
				snap.Liquidity = &liq
			}
			snapshots = append(snapshots, snap)
		}
	}

	log.Printf("[%s] collected %d snapshots", city, len(snapshots))

	if dryRun {
		enc := json.NewEncoder(os.Stdout)
		for _, s := range snapshots {
			if err := enc.Encode(s); err != nil {
				log.Printf("[%s] warning: failed to encode snapshot: %v", city, err)
			}
		}
		return nil
	}

	loader, err := polymarket.NewBQLoader(ctx, bqProject, "weather", "polymarket_snapshots")
	if err != nil {
		return fmt.Errorf("creating BigQuery loader: %w", err)
	}
	defer loader.Close()

	inserted, err := loader.MergeSnapshots(ctx, snapshots)
	if err != nil {
		return fmt.Errorf("merging snapshots: %w", err)
	}
	log.Printf("[%s] done: %d new rows inserted, %d duplicates skipped", city, inserted, len(snapshots)-inserted)
	return nil
}

func extractCityFromTitle(title string) string {
	lower := strings.ToLower(title)
	inIdx := strings.Index(lower, " in ")
	onIdx := strings.Index(lower, " on ")
	if inIdx == -1 || onIdx == -1 || onIdx <= inIdx {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(title[inIdx+4 : onIdx]))
}

func buildEventSlug(city string, date time.Time) string {
	citySlug := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(city), " ", "-"))
	return fmt.Sprintf("highest-temperature-in-%s-on-%s-%d-%d",
		citySlug,
		strings.ToLower(date.Month().String()),
		date.Day(),
		date.Year(),
	)
}

func normalizeCity(city string) string {
	return strings.ToLower(strings.TrimSpace(city))
}

func filterMarketsByTemp(markets []polymarket.GammaMarket, temp float64) []polymarket.GammaMarket {
	targets := []string{
		fmt.Sprintf("%.1f", temp),
		fmt.Sprintf("%g", temp),
		fmt.Sprintf("%.0f", temp),
	}
	var matched []polymarket.GammaMarket
	for _, m := range markets {
		q := strings.ToLower(m.Question)
		for _, t := range targets {
			if strings.Contains(q, t) {
				matched = append(matched, m)
				break
			}
		}
	}
	return matched
}

func extractTempThreshold(question string) float64 {
	lower := strings.ToLower(question)
	for _, marker := range []string{"°c", "°", "celsius", "degrees"} {
		idx := strings.Index(lower, marker)
		if idx == -1 {
			continue
		}
		token := extractNumberBefore(question, idx)
		if token == "" {
			continue
		}
		// Handle range format like "68-69°F": '-' at position > 0 is a range separator,
		// not a leading negative sign. Take the upper bound (number right before the marker).
		if i := strings.LastIndex(token, "-"); i > 0 {
			token = token[i+1:]
		}
		if v, err := strconv.ParseFloat(token, 64); err == nil {
			return v
		}
	}
	return 0
}

func extractNumberBefore(s string, idx int) string {
	end := idx
	for end > 0 && s[end-1] == ' ' {
		end--
	}
	start := end
	for start > 0 && (s[start-1] >= '0' && s[start-1] <= '9' || s[start-1] == '.' || s[start-1] == '-') {
		start--
	}
	return s[start:end]
}

func deriveNoHistory(yesHistory []polymarket.CLOBPricePoint) []polymarket.CLOBPricePoint {
	no := make([]polymarket.CLOBPricePoint, len(yesHistory))
	for i, pt := range yesHistory {
		no[i] = polymarket.CLOBPricePoint{T: pt.T, P: 1.0 - pt.P}
	}
	return no
}
