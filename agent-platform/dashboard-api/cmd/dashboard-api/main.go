package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"agentplatform/dashboardapi/internal/approvals"
	"agentplatform/dashboardapi/internal/health"
)

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

var (
	natsURL = getEnv("NATS_URL", "nats://nats.agent-platform.svc:4222")
	dbPath  = getEnv("APPROVAL_DB_PATH", "/data/approvals.db")
)

func main() {
	healthSrv := health.NewServer()
	healthSrv.AddCheck("approvals_db", approvals.Ping)
	healthSrv.AddCheck("nats", approvals.IsNatsConnected)

	if err := approvals.InitDB(dbPath); err != nil {
		log.Fatalf("failed to init approvals db: %v", err)
	}

	go func() {
		if err := approvals.StartNatsRelay(natsURL); err != nil {
			log.Fatalf("failed to connect to nats: %v", err)
		}
	}()

	healthSrv.MarkReady()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthSrv.LivezHandler)
	mux.HandleFunc("GET /readyz", healthSrv.ReadyzHandler)
	mux.HandleFunc("GET /ws", approvals.WSHandler)
	mux.HandleFunc("POST /proposed-actions", approvals.CreateProposedActionHandler)
	mux.HandleFunc("GET /proposed-actions", approvals.ListProposedActionsHandler)
	mux.HandleFunc("POST /proposed-actions/{id}/{decision}", approvals.ResolveActionHandler)

	log.Println("Dashboard API listening on :8000")
	srv := &http.Server{
		Addr:         ":8000",
		Handler:      approvals.CorsMiddleware(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
