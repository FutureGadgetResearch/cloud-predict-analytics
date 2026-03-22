package api

import (
	"net/http"
	"os"
)

// health is a public liveness endpoint — no auth required.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]string{"status": "ok"})
}

// info is a public endpoint returning non-sensitive runtime configuration.
// Secrets (credentials, tokens) are never included.
func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]string{
		"service":    getenv("K_SERVICE", "weather-api"),
		"revision":   getenv("K_REVISION", "unknown"),
		"project":    s.project,
		"bq_dataset": "weather",
		"region":     getenv("REGION", "us-central1"),
	})
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
