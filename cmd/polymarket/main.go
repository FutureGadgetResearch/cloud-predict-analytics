// polymarket is a CLI runner that pulls Polymarket prediction market data and
// lands it into BigQuery. Pass --dry-run to print JSONL to stdout instead.
//
// Weather events (auto slug construction):
//
//	polymarket --city=london --date=2026-03-10 --temp=10
//	polymarket --city=london --date=2026-03-10 --dry-run
//
// Any Polymarket event (explicit slug):
//
//	polymarket --slug=highest-temperature-in-london-on-march-6-2026 --date=2026-03-06 --dry-run
//	polymarket --slug=will-trump-win-the-2024-election --date=2024-11-05 --dry-run
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

	"github.com/FutureGadgetLabs/cloud-predict-analytics/internal/polymarket"
)

func main() {
	city := flag.String("city", "", "City name for weather events (e.g., london, new-york); not required when --slug is set")
	date := flag.String("date", "", "Event date in YYYY-MM-DD format — used for price history time range (required unless --yesterday is set)")
	yesterday := flag.Bool("yesterday", false, "Use yesterday's UTC date as the event date (overrides --date)")
	slug := flag.String("slug", "", "Polymarket event slug; overrides auto slug construction from --city/--date")
	temp := flag.Float64("temp", 0, "Temperature threshold in Celsius to filter a specific market (0 = all markets)")
	fidelity := flag.Int("fidelity", 60, "Price snapshot granularity in minutes (e.g., 60=hourly, 1=per-minute)")
	dryRun := flag.Bool("dry-run", false, "Print rows as JSONL to stdout instead of loading to BigQuery")
	noVolume := flag.Bool("no-volume", false, "Store NULL for volume/liquidity/bid-ask fields (use for historical backfills where market-state data is misleading)")
	flag.Parse()

	if *yesterday {
		y := time.Now().UTC().AddDate(0, 0, -1)
		s := y.Format("2006-01-02")
		date = &s
	}
	if *date == "" {
		fmt.Fprintln(os.Stderr, "Usage: polymarket --date=2026-03-10 [--city=london] [--slug=<event-slug>] [--temp=10] [--fidelity=60] [--yesterday]")
		os.Exit(1)
	}
	if *slug == "" && *city == "" {
		fmt.Fprintln(os.Stderr, "Error: provide either --city (weather events) or --slug (any event)")
		os.Exit(1)
	}

	eventDate, err := time.Parse("2006-01-02", *date)
	if err != nil {
		log.Fatalf("invalid date %q: expected YYYY-MM-DD format", *date)
	}

	eventSlug := *slug
	if eventSlug == "" {
		eventSlug = buildEventSlug(*city, eventDate)
	}
	log.Printf("resolved event slug: %s", eventSlug)

	cityLabel := normalizeCity(*city)

	client := polymarket.NewClient()

	// The event endpoint is the canonical source: it returns the event's markets directly.
	event, err := client.GetEventBySlug(eventSlug)
	if err != nil {
		log.Fatalf("could not find event for slug %q: %v", slug, err)
	}
	// If city wasn't provided via flag, extract it from the event title.
	// Title format: "Highest Temperature in London on March 6, 2026"
	if cityLabel == "" {
		cityLabel = extractCityFromTitle(event.Title)
	}

	markets := event.Markets

	if len(markets) == 0 {
		log.Fatalf("no markets found for event slug %q", slug)
	}
	log.Printf("found %d market(s) for event", len(markets))

	// The event endpoint sometimes returns markets without clobTokenIds populated.
	// Fetch the full market details for any market missing token IDs.
	for i, m := range markets {
		if len(m.ClobTokenIDs) == 0 && m.ID != "" {
			full, err := client.GetMarketByID(m.ID)
			if err != nil {
				log.Printf("warning: could not hydrate market %s: %v", m.ID, err)
				continue
			}
			markets[i] = *full
		}
	}

	// Filter to the requested temperature threshold if provided.
	if *temp != 0 {
		markets = filterMarketsByTemp(markets, *temp)
		if len(markets) == 0 {
			log.Fatalf("no market found matching temperature threshold %.1f°C", *temp)
		}
	}

	// History end = end of the event/resolution date.
	dayEnd := time.Date(eventDate.Year(), eventDate.Month(), eventDate.Day()+1, 0, 0, 0, 0, time.UTC)

	var snapshots []polymarket.PredictionSnapshot

	for _, market := range markets {
		threshold := extractTempThreshold(market.Question)

		// History start = when this market opened for trading.
		// Falls back to the start of the event date if StartDateIso is missing.
		histStart := time.Date(eventDate.Year(), eventDate.Month(), eventDate.Day(), 0, 0, 0, 0, time.UTC)
		if market.StartDateIso != "" {
			if t, err := time.Parse("2006-01-02", market.StartDateIso[:10]); err == nil {
				histStart = t
			}
		}

		log.Printf("pulling price history for market: %s (%.1f°C) from %s to %s",
			market.Question, threshold,
			histStart.Format("2006-01-02"), eventDate.Format("2006-01-02"))

		yesHistory, err := client.GetPriceHistory(
			market.YesTokenID(),
			histStart.Unix(),
			dayEnd.Unix(),
			*fidelity,
		)
		if err != nil {
			log.Printf("warning: could not fetch YES price history for market %s: %v", market.ConditionID, err)
			continue
		}

		noHistory, err := client.GetPriceHistory(
			market.NoTokenID(),
			histStart.Unix(),
			dayEnd.Unix(),
			*fidelity,
		)
		if err != nil {
			log.Printf("warning: could not fetch NO price history for market %s: %v", market.ConditionID, err)
			noHistory = deriveNoHistory(yesHistory)
		}

		// Filter 1: skip the entire market if it has never been traded.
		// Zero volume + zero liquidity means no real price discovery has happened.
		if market.VolumeTotal == 0 && market.Liquidity == 0 {
			log.Printf("skipping market %.1f°C — no trading activity", threshold)
			continue
		}

		// Parse market end time for post-resolution filter.
		var marketEnd time.Time
		if market.EndDateIso != "" {
			marketEnd, _ = time.Parse("2006-01-02", market.EndDateIso[:10])
			marketEnd = marketEnd.Add(24 * time.Hour) // end of that day
		}

		noPriceByTs := make(map[int64]float64, len(noHistory))
		for _, pt := range noHistory {
			noPriceByTs[pt.T] = pt.P
		}

		var lastYesCost float64 = -1 // sentinel so the first point always passes

		for _, pt := range yesHistory {
			ts := time.Unix(pt.T, 0).UTC().Round(15 * time.Minute)

			// Filter 2: skip rows after the market has resolved.
			if !marketEnd.IsZero() && ts.After(marketEnd) {
				continue
			}

			// Filter 3: skip rows where the YES price is at or near resolution (≥0.99 or ≤0.01).
			// These indicate the market has effectively settled and carry no analytical signal.
			if pt.P >= 0.99 || pt.P <= 0.01 {
				continue
			}

			// Filter 4: skip rows where price hasn't changed from the previous snapshot.
			// Tolerance of 0.001 (0.1%) avoids storing noise-level fluctuations.
			if math.Abs(pt.P-lastYesCost) < 0.001 {
				continue
			}
			lastYesCost = pt.P

			noPrice := noPriceByTs[pt.T]
			if noPrice == 0 {
				noPrice = 1.0 - pt.P
			}

			snap := polymarket.PredictionSnapshot{
				City:          cityLabel,
				Date:          *date,
				Timestamp:     ts,
				TempThreshold: threshold,
				YesCost:       pt.P,
				NoCost:        noPrice,
				EventSlug:     eventSlug,
				MarketEndDate: market.EndDateIso,
			}
			if !*noVolume {
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

	log.Printf("collected %d snapshots", len(snapshots))

	if *dryRun {
		enc := json.NewEncoder(os.Stdout)
		for _, s := range snapshots {
			if err := enc.Encode(s); err != nil {
				log.Printf("warning: failed to encode snapshot: %v", err)
			}
		}
		return
	}

	ctx := context.Background()
	loader, err := polymarket.NewBQLoader(ctx, "fg-polylabs", "weather", "polymarket_snapshots")
	if err != nil {
		log.Fatalf("creating BigQuery loader: %v", err)
	}
	defer loader.Close()

	inserted, err := loader.MergeSnapshots(ctx, snapshots)
	if err != nil {
		log.Fatalf("merging snapshots into BigQuery: %v", err)
	}
	skipped := len(snapshots) - inserted
	log.Printf("done: %d new rows inserted, %d duplicates skipped", inserted, skipped)
}

// extractCityFromTitle parses the city from a Polymarket event title.
// e.g., "Highest Temperature in London on March 6, 2026" → "london"
func extractCityFromTitle(title string) string {
	lower := strings.ToLower(title)
	inIdx := strings.Index(lower, " in ")
	onIdx := strings.Index(lower, " on ")
	if inIdx == -1 || onIdx == -1 || onIdx <= inIdx {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(title[inIdx+4 : onIdx]))
}

// buildEventSlug constructs the Polymarket event slug from city and date.
// e.g., city="london", date=2026-03-10 → "highest-temperature-in-london-on-march-10-2026"
func buildEventSlug(city string, date time.Time) string {
	citySlug := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(city), " ", "-"))
	monthName := strings.ToLower(date.Month().String())
	day := strconv.Itoa(date.Day()) // no leading zero
	year := strconv.Itoa(date.Year())
	return fmt.Sprintf("highest-temperature-in-%s-on-%s-%s-%s", citySlug, monthName, day, year)
}

// normalizeCity returns a clean, lowercase city name for storage.
func normalizeCity(city string) string {
	return strings.ToLower(strings.TrimSpace(city))
}

// filterMarketsByTemp returns markets whose question text contains the temperature value.
func filterMarketsByTemp(markets []polymarket.GammaMarket, temp float64) []polymarket.GammaMarket {
	// Match both integer-like values ("10") and decimals ("10.5").
	targets := []string{
		fmt.Sprintf("%.1f", temp),    // "10.0"
		fmt.Sprintf("%g", temp),      // "10"
		fmt.Sprintf("%.0f", temp),    // "10"
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

// extractTempThreshold attempts to parse the temperature threshold from a market question.
// Returns 0 if the value cannot be determined.
func extractTempThreshold(question string) float64 {
	// Questions are like: "Will the highest temperature in London be above 10°C on March 10, 2026?"
	// Scan for numeric tokens adjacent to "°" or "celsius" or "degrees".
	lower := strings.ToLower(question)
	markers := []string{"°c", "°", "celsius", "degrees"}
	for _, marker := range markers {
		idx := strings.Index(lower, marker)
		if idx == -1 {
			continue
		}
		// Walk backwards to find the number before the marker.
		token := extractNumberBefore(question, idx)
		if token != "" {
			if v, err := strconv.ParseFloat(token, 64); err == nil {
				return v
			}
		}
	}
	return 0
}

// extractNumberBefore returns the numeric token immediately before position idx in s.
func extractNumberBefore(s string, idx int) string {
	end := idx
	// Skip any space before the marker.
	for end > 0 && s[end-1] == ' ' {
		end--
	}
	start := end
	for start > 0 && (s[start-1] >= '0' && s[start-1] <= '9' || s[start-1] == '.' || s[start-1] == '-') {
		start--
	}
	return s[start:end]
}

// deriveNoHistory computes NO prices as 1 - YES price for each point.
func deriveNoHistory(yesHistory []polymarket.CLOBPricePoint) []polymarket.CLOBPricePoint {
	no := make([]polymarket.CLOBPricePoint, len(yesHistory))
	for i, pt := range yesHistory {
		no[i] = polymarket.CLOBPricePoint{T: pt.T, P: 1.0 - pt.P}
	}
	return no
}
