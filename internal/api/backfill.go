package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	runapi "google.golang.org/api/run/v2"
)

type backfillRequest struct {
	DateFrom string `json:"date_from"`
	DateTo   string `json:"date_to"`
	City     string `json:"city"` // optional; omit for all active cities
}

type backfillResponse struct {
	Execution string `json:"execution"`
	DateFrom  string `json:"date_from"`
	DateTo    string `json:"date_to"`
	City      string `json:"city,omitempty"`
	Days      int    `json:"days"`
}

func (s *Server) triggerBackfill(w http.ResponseWriter, r *http.Request) {
	var req backfillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	from, err1 := time.Parse("2006-01-02", req.DateFrom)
	to, err2 := time.Parse("2006-01-02", req.DateTo)
	if err1 != nil || err2 != nil {
		jsonError(w, "date_from and date_to must be YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	if from.After(to) {
		jsonError(w, "date_from must be <= date_to", http.StatusBadRequest)
		return
	}
	days := int(to.Sub(from).Hours()/24) + 1
	if days > 90 {
		jsonError(w, "date range cannot exceed 90 days", http.StatusBadRequest)
		return
	}

	dateRange := fmt.Sprintf("%s:%s", req.DateFrom, req.DateTo)
	args := []string{"--date-range=" + dateRange, "--no-volume"}
	if req.City != "" {
		args = append(args, "--city="+req.City)
	} else {
		args = append(args, "--all-cities")
	}

	svc, err := runapi.NewService(r.Context())
	if err != nil {
		jsonError(w, "failed to create Cloud Run client: "+err.Error(), http.StatusInternalServerError)
		return
	}

	jobName := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", s.project, s.region, s.polymarketJob)
	runReq := &runapi.GoogleCloudRunV2RunJobRequest{
		Overrides: &runapi.GoogleCloudRunV2Overrides{
			ContainerOverrides: []*runapi.GoogleCloudRunV2ContainerOverride{
				{Args: args},
			},
		},
	}

	op, err := svc.Projects.Locations.Jobs.Run(jobName, runReq).Do()
	if err != nil {
		jsonError(w, "failed to trigger backfill job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	jsonOK(w, backfillResponse{
		Execution: op.Name,
		DateFrom:  req.DateFrom,
		DateTo:    req.DateTo,
		City:      req.City,
		Days:      days,
	})
}
