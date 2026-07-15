// Command server exposes the Orbit chatbot over HTTP, built on the REAL ADK Go
// runner + session service with the offline FakeModel. Port 4114.
//
//	go run ./cmd/server
//	curl -s -X POST http://localhost:4114/v1/tenant/acme/user/alice/chat \
//	  -H 'Content-Type: application/json' -d '{"prompt":"MEMO: my code is 42","session_id":"s1"}'
//	curl -s -X POST http://localhost:4114/v1/tenant/acme/user/alice/chat \
//	  -H 'Content-Type: application/json' -d '{"prompt":"what did I tell you?","session_id":"s1"}'
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"adk-go-arena/orbit"
)

type chatRequest struct {
	Prompt    string `json:"prompt"`
	SessionID string `json:"session_id"`
}

type chatResponse struct {
	Reply    string `json:"reply"`
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id"`
}

// bot is a single real ADK runner shared across requests.
var bot *orbit.Orbit

func chatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	tenantID := r.PathValue("tenant_id")
	userID := r.PathValue("user_id")
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = "default"
	}

	// CONVENTION, not construction: the developer must fold tenant into the ADK
	// identity key. ADK Go has no tenant field; forget this and tenants collide.
	identity := tenantID + "|" + userID

	reply, err := bot.Reply(context.Background(), identity, sessionID, req.Prompt)
	if err != nil {
		http.Error(w, "agent error: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chatResponse{Reply: reply, TenantID: tenantID, UserID: userID})
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok", "framework": "google.golang.org/adk/v2", "port": "4114",
	})
}

func main() {
	var err error
	bot, err = orbit.New("orbit")
	if err != nil {
		log.Fatalf("failed to build Orbit on ADK Go: %v", err)
	}

	addr := ":4114"
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/v1/tenant/{tenant_id}/user/{user_id}/chat", chatHandler)

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down...")
		_ = srv.Close()
	}()

	fmt.Printf("[orbit/adk-go] serving on %s (real ADK Go runner + offline fake model)\n", addr)
	log.Fatal(srv.ListenAndServe())
}
