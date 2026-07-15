// Orbit live server for the Eino arena.
//
// Serves the same route shape as aisotropy
// (/v1/tenant/{tenant_id}/user/{user_id}/chat[/stream]) but backed by the REAL
// CloudWeGo Eino runtime via the eino-arena/orbit package. Uses the offline
// stub ChatModel, so it boots with no API key.
//
// Port: 4113
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

	"eino-arena/orbit"
)

type chatRequest struct {
	Prompt    string `json:"prompt"`
	SessionID string `json:"session_id"`
	Note      string `json:"note,omitempty"` // optional working-memory note to remember first
}

type chatResponse struct {
	Reply     string `json:"reply"`
	TenantID  string `json:"tenant_id"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
	Streamed  bool   `json:"streamed"`
	Chunks    int    `json:"chunks,omitempty"`
}

func main() {
	ctx := context.Background()
	ob, err := orbit.New(ctx)
	if err != nil {
		log.Fatalf("orbit.New: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok", "framework": "eino", "port": "4113"})
	})

	mux.HandleFunc("POST /v1/tenant/{tenant_id}/user/{user_id}/chat", func(w http.ResponseWriter, r *http.Request) {
		tenantID, userID := r.PathValue("tenant_id"), r.PathValue("user_id")
		req, err := decode(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Note != "" {
			ob.Remember(tenantID, userID, req.SessionID, req.Note)
		}
		reply, err := ob.Chat(r.Context(), tenantID, userID, req.SessionID, req.Prompt)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, chatResponse{Reply: reply, TenantID: tenantID, UserID: userID, SessionID: req.SessionID})
	})

	mux.HandleFunc("POST /v1/tenant/{tenant_id}/user/{user_id}/chat/stream", func(w http.ResponseWriter, r *http.Request) {
		tenantID, userID := r.PathValue("tenant_id"), r.PathValue("user_id")
		req, err := decode(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		full, n, err := ob.Stream(r.Context(), tenantID, userID, req.SessionID, req.Prompt)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, chatResponse{Reply: full, TenantID: tenantID, UserID: userID, SessionID: req.SessionID, Streamed: true, Chunks: n})
	})

	addr := ":4113"
	if v := os.Getenv("ORBIT_ADDR"); v != "" {
		addr = v
	}
	server := &http.Server{Addr: addr, Handler: mux}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down...")
		_ = server.Close()
	}()

	fmt.Printf("[orbit/eino] serving on %s (real Eino runtime, stub model)\n", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func decode(r *http.Request) (chatRequest, error) {
	var req chatRequest
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, fmt.Errorf("bad request: %w", err)
	}
	if req.SessionID == "" {
		req.SessionID = "default"
	}
	return req, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
