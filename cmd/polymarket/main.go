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
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/FutureGadgetLabs/cloud-predict-analytics/internal/polymarket"
)

func main() {
	city := flag.String("city", "", "City name for weather events (e.g., london, new-york); not required when --slug is set")
	date := flag.String("date", "", "Event date in YYYY-MM-DD format — used for price history time range (required)")
	slug := flag.String("slug", "", "Polymarket event slug; overrides auto slug construction from --city/--date")
	temp := flag.Float64("temp", 0, "Temperature threshold in Celsius to filter a specific market (0 = all markets)")
	fidelity := flag.Int("fidelity", 60, "Price snapshot granularity in minutes (e.g., 60=hourly, 1=per-minute)")
	dryRun := flag.Bool("dry-run", false, "Print rows as JSONL to stdout instead of loading to BigQuery")
	flag.Parse()

	if *date == "" {
		fmt.Fprintln(os.Stderr, "Usage: polymarket --date=2026-03-10 [--city=london] [--slug=<event-slug>] [--temp=10] [--fidelity=60]")
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

	client := polymarket.NewClient()

	// The event endpoint is the canonical source: it returns the event's markets directly.
	event, err := client.GetEventBySlug(eventSlug)
	if err != nil {
		log.Fatalf("could not find event for slug %q: %v", slug, err)
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

	cityLabel := normalizeCity(*city) // empty string when --slug used without --city

	// Day boundaries in UTC.
	dayStart := time.Date(eventDate.Year(), eventDate.Month(), eventDate.Day(), 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)
	ingestedAt := time.Now().UTC()

	var snapshots []polymarket.PredictionSnapshot

	for _, market := range markets {
		threshold := extractTempThreshold(market.Question)
		log.Printf("pulling price history for market: %s (threshold: %.1f°C)", market.Question, threshold)

		yesHistory, err := client.GetPriceHistory(
			market.YesTokenID(),
			dayStart.Unix(),
			dayEnd.Unix(),
			*fidelity,
		)
		if err != nil {
			log.Printf("warning: could not fetch YES price history for market %s: %v", market.ConditionID, err)
			continue
		}

		noHistory, err := client.GetPriceHistory(
			market.NoTokenID(),
			dayStart.Unix(),
			dayEnd.Unix(),
			*fidelity,
		)
		if err != nil {
			log.Printf("warning: could not fetch NO price history for market %s: %v", market.ConditionID, err)
			noHistory = deriveNoHistory(yesHistory)
		}

		noPriceByTs := make(map[int64]float64, len(noHistory))
		for _, pt := range noHistory {
			noPriceByTs[pt.T] = pt.P
		}

		spread := market.BestAsk - market.BestBid

		for _, pt := range yesHistory {
			noPrice := noPriceByTs[pt.T]
			if noPrice == 0 {
				noPrice = 1.0 - pt.P
			}
			snapshots = append(snapshots, polymarket.PredictionSnapshot{
				City:            cityLabel,
				Date:            *date,
				Timestamp:       time.Unix(pt.T, 0).UTC(),
				TempThreshold:   threshold,
				YesCost:         pt.P,
				NoCost:          noPrice,
				BestBid:         market.BestBid,
				BestAsk:         market.BestAsk,
				Spread:          spread,
				Volume24h:       market.Volume24hr,
				VolumeTotal:     market.VolumeTotal,
				Liquidity:       market.Liquidity,
				MarketID:        market.ConditionID,
				EventSlug:       eventSlug,
				MarketEndDate:   market.EndDateIso,
				MarketStartDate: market.StartDateIso,
				AcceptingOrders: market.AcceptingOrders,
				NegRisk:         market.NegRisk,
				IngestedAt:      ingestedAt,
			})
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

	// TODO: load snapshots into BigQuery
	// bq.Load(ctx, "polymarket_predictions", snapshots)
	log.Fatal("BigQuery loading not yet implemented — rerun with --dry-run to print rows")
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
