package api

import (
	"net/http"

	"github.com/FutureGadgetLabs/cloud-predict-analytics/internal/syncer"
)

// syncData exports tracked_cities and polymarket_snapshots to GCS and GitHub.
// POST /sync — requires auth.
func (s *Server) syncData(w http.ResponseWriter, r *http.Request) {
	result := syncer.Run(r.Context(), s.bq, syncer.Config{
		Project:      s.project,
		GCSBucket:    s.gcsBucket,
		GitHubToken:  s.githubToken,
		GitHubRepo:   s.githubDataRepo,
		SnapshotDays: 90,
	})
	jsonOK(w, result)
}
