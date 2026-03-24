package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"cloud.google.com/go/bigquery"
	firebaseauth "firebase.google.com/go/v4/auth"
)

// Server is the HTTP API server.
type Server struct {
	mux            *http.ServeMux
	bq             *bigquery.Client
	auth           *firebaseauth.Client
	project        string
	region         string
	gcsBucket      string
	githubToken    string
	githubDataRepo string
	polymarketJob  string
}

// NewServer creates a new Server, wiring up all routes.
func NewServer(ctx context.Context, project string, auth *firebaseauth.Client) (*Server, error) {
	bq, err := bigquery.NewClient(ctx, project)
	if err != nil {
		return nil, err
	}
	s := &Server{
		mux:            http.NewServeMux(),
		bq:             bq,
		auth:           auth,
		project:        project,
		region:         getenv("CLOUD_RUN_REGION", "us-central1"),
		gcsBucket:      getenv("GCS_DATA_BUCKET", "fg-polylabs-weather-data"),
		githubToken:    os.Getenv("GITHUB_TOKEN"),
		githubDataRepo: getenv("GITHUB_DATA_REPO", "FG-PolyLabs/cloud-predict-analytics-data"),
		polymarketJob:  getenv("POLYMARKET_JOB_NAME", "weather-polymarket"),
	}
	s.routes()
	return s, nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /tracked-cities", s.listCities)
	s.mux.HandleFunc("POST /tracked-cities", s.createCity)
	s.mux.HandleFunc("PUT /tracked-cities/{source}/{city}", s.updateCity)
	s.mux.HandleFunc("DELETE /tracked-cities/{source}/{city}", s.deleteCity)
	s.mux.HandleFunc("GET /snapshots", s.querySnapshots)
	s.mux.HandleFunc("POST /snapshots/reset", s.resetSnapshots)
	s.mux.HandleFunc("POST /sync", s.syncData)
	s.mux.HandleFunc("POST /backfill", s.triggerBackfill)
}

// ServeHTTP applies CORS middleware to every request.
// /health and /info are public; all other routes require a valid Firebase token.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			s.health(w, r)
		case "/info":
			s.info(w, r)
		default:
			s.withAuth(s.mux).ServeHTTP(w, r)
		}
	})).ServeHTTP(w, r)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			jsonError(w, "missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}
		if _, err := s.auth.VerifyIDToken(r.Context(), strings.TrimPrefix(header, "Bearer ")); err != nil {
			jsonError(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
