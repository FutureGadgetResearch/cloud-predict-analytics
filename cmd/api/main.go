package main

import (
	"context"
	"log"
	"net/http"
	"os"

	firebase "firebase.google.com/go/v4"
	"github.com/FutureGadgetLabs/cloud-predict-analytics/internal/api"
)

func main() {
	ctx := context.Background()

	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		project = "fg-polylabs"
	}

	firebaseProject := os.Getenv("FIREBASE_PROJECT_ID")
	if firebaseProject == "" {
		firebaseProject = "collection-showcase-auth"
	}

	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: firebaseProject})
	if err != nil {
		log.Fatalf("firebase.NewApp: %v", err)
	}
	authClient, err := app.Auth(ctx)
	if err != nil {
		log.Fatalf("firebase Auth: %v", err)
	}

	srv, err := api.NewServer(ctx, project, authClient)
	if err != nil {
		log.Fatalf("api.NewServer: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("API server listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, srv))
}
