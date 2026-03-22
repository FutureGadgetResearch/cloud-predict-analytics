package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"
)

type City struct {
	City        string `json:"city"`
	DisplayName string `json:"display_name"`
	Timezone    string `json:"timezone"`
	Active      bool   `json:"active"`
	AddedDate   string `json:"added_date"`
	Notes       string `json:"notes"`
}

func (s *Server) listCities(w http.ResponseWriter, r *http.Request) {
	q := s.bq.Query(fmt.Sprintf(`
		SELECT city, display_name, timezone, active,
		       CAST(added_date AS STRING) AS added_date,
		       IFNULL(notes, '') AS notes
		FROM %s
		ORDER BY city
	`, s.table("tracked_cities")))

	it, err := q.Read(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var cities []City
	for {
		var row []bigquery.Value
		if err := it.Next(&row); err == iterator.Done {
			break
		} else if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cities = append(cities, City{
			City:        fmt.Sprint(row[0]),
			DisplayName: fmt.Sprint(row[1]),
			Timezone:    fmt.Sprint(row[2]),
			Active:      row[3].(bool),
			AddedDate:   fmt.Sprint(row[4]),
			Notes:       fmt.Sprint(row[5]),
		})
	}
	if cities == nil {
		cities = []City{}
	}
	jsonOK(w, cities)
}

func (s *Server) createCity(w http.ResponseWriter, r *http.Request) {
	var body City
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	body.City = strings.ToLower(strings.TrimSpace(body.City))
	if body.City == "" || body.DisplayName == "" || body.Timezone == "" {
		jsonError(w, "city, display_name, and timezone are required", http.StatusBadRequest)
		return
	}
	if body.AddedDate == "" {
		body.AddedDate = time.Now().UTC().Format("2006-01-02")
	}

	q := s.bq.Query(fmt.Sprintf(`
		INSERT INTO %s (city, display_name, timezone, active, added_date, notes)
		VALUES (@city, @display_name, @timezone, @active, @added_date, @notes)
	`, s.table("tracked_cities")))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "city", Value: body.City},
		{Name: "display_name", Value: body.DisplayName},
		{Name: "timezone", Value: body.Timezone},
		{Name: "active", Value: body.Active},
		{Name: "added_date", Value: body.AddedDate},
		{Name: "notes", Value: nullString(body.Notes)},
	}

	job, err := q.Run(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := job.Wait(r.Context()); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, body)
}

func (s *Server) updateCity(w http.ResponseWriter, r *http.Request) {
	city := r.PathValue("city")
	var body City
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	q := s.bq.Query(fmt.Sprintf(`
		UPDATE %s
		SET display_name = @display_name,
		    timezone     = @timezone,
		    active       = @active,
		    notes        = @notes
		WHERE city = @city
	`, s.table("tracked_cities")))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "city", Value: city},
		{Name: "display_name", Value: body.DisplayName},
		{Name: "timezone", Value: body.Timezone},
		{Name: "active", Value: body.Active},
		{Name: "notes", Value: nullString(body.Notes)},
	}

	job, err := q.Run(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := job.Wait(r.Context()); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteCity(w http.ResponseWriter, r *http.Request) {
	city := r.PathValue("city")

	q := s.bq.Query(fmt.Sprintf(`
		DELETE FROM %s WHERE city = @city
	`, s.table("tracked_cities")))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "city", Value: city},
	}

	job, err := q.Run(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := job.Wait(r.Context()); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// table returns a fully-qualified backtick-quoted BigQuery table reference.
func (s *Server) table(name string) string {
	return fmt.Sprintf("`%s.weather.%s`", s.project, name)
}

// nullString returns a bigquery.NullString so empty values are stored as NULL.
func nullString(s string) bigquery.NullString {
	return bigquery.NullString{StringVal: s, Valid: s != ""}
}
