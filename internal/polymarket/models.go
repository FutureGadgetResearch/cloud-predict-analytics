package polymarket

import (
	"encoding/json"
	"time"
)

// StringSlice handles Polymarket's quirk of returning JSON arrays as encoded strings
// (e.g., `"[\"Yes\",\"No\"]"`) or as actual JSON arrays interchangeably.
type StringSlice []string

func (s *StringSlice) UnmarshalJSON(data []byte) error {
	// Try direct array first.
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*s = arr
		return nil
	}
	// Polymarket sometimes encodes arrays as JSON strings, e.g. "[\"Yes\",\"No\"]".
	var encoded string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(encoded), &arr); err != nil {
		return err
	}
	*s = arr
	return nil
}

// GammaEvent represents a Polymarket event from the Gamma API.
type GammaEvent struct {
	ID      string        `json:"id"`
	Slug    string        `json:"slug"`
	Title   string        `json:"title"`
	Markets []GammaMarket `json:"markets"`
}

// GammaMarket represents a single market (outcome) within a Polymarket event.
type GammaMarket struct {
	ID              string      `json:"id"`
	Question        string      `json:"question"`
	ConditionID     string      `json:"conditionId"`
	ClobTokenIDs    StringSlice `json:"clobTokenIds"`
	Outcomes        StringSlice `json:"outcomes"`
	OutcomePrices   StringSlice `json:"outcomePrices"`
	Active          bool        `json:"active"`
	Closed          bool        `json:"closed"`
	AcceptingOrders bool        `json:"acceptingOrders"`
	NegRisk         bool        `json:"negRisk"`
	BestBid         float64     `json:"bestBid"`
	BestAsk         float64     `json:"bestAsk"`
	LastTradePrice  float64     `json:"lastTradePrice"`
	Volume24hr      float64     `json:"volume24hrNum"`
	VolumeTotal     float64     `json:"volumeNum"`
	Liquidity       float64     `json:"liquidityNum"`
	EndDateIso      string      `json:"endDateIso"`
	StartDateIso    string      `json:"startDateIso"`
}

// YesTokenID returns the CLOB token ID for the YES outcome.
func (m *GammaMarket) YesTokenID() string {
	if len(m.ClobTokenIDs) > 0 {
		return m.ClobTokenIDs[0]
	}
	return ""
}

// NoTokenID returns the CLOB token ID for the NO outcome.
func (m *GammaMarket) NoTokenID() string {
	if len(m.ClobTokenIDs) > 1 {
		return m.ClobTokenIDs[1]
	}
	return ""
}

// CLOBPriceHistoryResponse is the response from /prices-history.
type CLOBPriceHistoryResponse struct {
	History []CLOBPricePoint `json:"history"`
}

// CLOBPricePoint is a single timestamped price from the CLOB API.
type CLOBPricePoint struct {
	T int64   `json:"t"` // unix timestamp
	P float64 `json:"p"` // price (0.0-1.0)
}

// PredictionSnapshot is a single row of prediction data — maps 1:1 to the BQ table.
type PredictionSnapshot struct {
	City            string    `json:"city"`
	Date            string    `json:"date"`             // YYYY-MM-DD, the event date
	Timestamp       time.Time `json:"timestamp"`        // when this price snapshot was taken
	TempThreshold   float64   `json:"temp_threshold"`   // e.g. 10.0 for "above 10°C"
	YesCost         float64   `json:"yes_cost"`         // probability/price of YES (0.0-1.0)
	NoCost          float64   `json:"no_cost"`          // probability/price of NO (0.0-1.0)
	BestBid         float64   `json:"best_bid"`         // best bid for YES token at snapshot
	BestAsk         float64   `json:"best_ask"`         // best ask for YES token at snapshot
	Spread          float64   `json:"spread"`           // best_ask - best_bid
	Volume24h       float64   `json:"volume_24h"`       // 24h trading volume at time of fetch
	VolumeTotal     float64   `json:"volume_total"`     // lifetime trading volume at time of fetch
	Liquidity       float64   `json:"liquidity"`        // market liquidity at time of fetch
	MarketID        string    `json:"market_id"`        // Polymarket condition ID
	EventSlug       string    `json:"event_slug"`       // Polymarket event slug
	MarketEndDate   string    `json:"market_end_date"`  // ISO date when the market closes/resolves
	MarketStartDate string    `json:"market_start_date"` // ISO date when the market opened for trading
	AcceptingOrders bool      `json:"accepting_orders"` // whether the market is still tradeable at fetch time
	NegRisk         bool      `json:"neg_risk"`         // whether this is a neg-risk market
	IngestedAt      time.Time `json:"ingested_at"`      // when the pipeline collected this row
}
