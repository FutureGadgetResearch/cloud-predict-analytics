package api

import (
	"fmt"
	"net/http"
	"strconv"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"
)

type Snapshot struct {
	City          string   `json:"city"`
	Date          string   `json:"date"`
	Timestamp     string   `json:"timestamp"`
	TempThreshold float64  `json:"temp_threshold"`
	YesCost       float64  `json:"yes_cost"`
	NoCost        float64  `json:"no_cost"`
	BestBid       *float64 `json:"best_bid"`
	BestAsk       *float64 `json:"best_ask"`
	Spread        *float64 `json:"spread"`
	Volume24h     *float64 `json:"volume_24h"`
	VolumeTotal   *float64 `json:"volume_total"`
	Liquidity     *float64 `json:"liquidity"`
	EventSlug     string   `json:"event_slug"`
	MarketEndDate *string  `json:"market_end_date"`
}

func (s *Server) querySnapshots(w http.ResponseWriter, r *http.Request) {
	city      := r.URL.Query().Get("city")
	date      := r.URL.Query().Get("date")
	dateFrom  := r.URL.Query().Get("date_from")
	dateTo    := r.URL.Query().Get("date_to")
	limitStr  := r.URL.Query().Get("limit")

	// limit=0 means no limit; default 200, hard cap 10000
	limit := 200
	if limitStr == "0" {
		limit = 0
	} else if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
		if n > 10000 {
			n = 10000
		}
		limit = n
	}

	where := "WHERE 1=1"
	params := []bigquery.QueryParameter{}
	if city != "" {
		where += " AND city = @city"
		params = append(params, bigquery.QueryParameter{Name: "city", Value: city})
	}
	// single date takes precedence; otherwise use date range
	if date != "" {
		where += " AND date = @date"
		params = append(params, bigquery.QueryParameter{Name: "date", Value: date})
	} else {
		if dateFrom != "" {
			where += " AND date >= @date_from"
			params = append(params, bigquery.QueryParameter{Name: "date_from", Value: dateFrom})
		}
		if dateTo != "" {
			where += " AND date <= @date_to"
			params = append(params, bigquery.QueryParameter{Name: "date_to", Value: dateTo})
		}
	}

	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf("LIMIT %d", limit)
	}

	q := s.bq.Query(fmt.Sprintf(`
		SELECT
		  city,
		  CAST(date AS STRING)      AS date,
		  CAST(timestamp AS STRING) AS timestamp,
		  temp_threshold,
		  yes_cost, no_cost,
		  best_bid, best_ask, spread,
		  volume_24h, volume_total, liquidity,
		  event_slug, market_end_date
		FROM %s
		%s
		ORDER BY timestamp DESC
		%s
	`, s.table("polymarket_snapshots"), where, limitClause))
	q.Parameters = params

	it, err := q.Read(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var rows []Snapshot
	for {
		var row []bigquery.Value
		if err := it.Next(&row); err == iterator.Done {
			break
		} else if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		snap := Snapshot{
			City:          fmt.Sprint(row[0]),
			Date:          fmt.Sprint(row[1]),
			Timestamp:     fmt.Sprint(row[2]),
			TempThreshold: toFloat(row[3]),
			YesCost:       toFloat(row[4]),
			NoCost:        toFloat(row[5]),
			BestBid:       toFloatPtr(row[6]),
			BestAsk:       toFloatPtr(row[7]),
			Spread:        toFloatPtr(row[8]),
			Volume24h:     toFloatPtr(row[9]),
			VolumeTotal:   toFloatPtr(row[10]),
			Liquidity:     toFloatPtr(row[11]),
			EventSlug:     fmt.Sprint(row[12]),
			MarketEndDate: toStringPtr(row[13]),
		}
		rows = append(rows, snap)
	}
	if rows == nil {
		rows = []Snapshot{}
	}
	jsonOK(w, rows)
}

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
