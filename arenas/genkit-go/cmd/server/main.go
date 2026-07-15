// Orbit live server for the Genkit Go arena — mirrors the aisotropy HTTP surface.
//
// This is a REAL Genkit build: every request runs through genkit.DefineFlow /
// genkit.Generate against the offline fake model (no API key). It exposes the
// same route shape as aisotropy so you can probe the isolation model live.
//
// Port: 4116
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

	"genkit-go-arena/orbit"
)

type chatRequest struct {
	Prompt    string `json:"prompt"`
	SessionID string `json:"session_id"`
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app, err := orbit.New(ctx)
	if err != nil {
		log.Fatalf("orbit init: %v", err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "ok", "framework": "genkit-go", "model": orbit.FakeModelName, "port": "4116",
		})
	})

	// Non-streaming chat. NOTE the session key: this server namespaces it as
	// tenant:user:session — the "correct" convention. It is still only a
	// convention; the framework does not enforce that the key matches the path
	// identity (see the smoke for the leak).
	mux.HandleFunc("POST /v1/tenant/{tenant_id}/user/{user_id}/chat", func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		tenant := r.PathValue("tenant_id")
		user := r.PathValue("user_id")
		key := fmt.Sprintf("%s:%s:%s", tenant, user, req.SessionID)

		out, err := app.Chat(r.Context(), orbit.ChatInput{
			TenantID: tenant, UserID: user, SessionKey: key, Prompt: req.Prompt,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	// Streaming chat over SSE, driven by the real genkit streaming callback.
	mux.HandleFunc("POST /v1/tenant/{tenant_id}/user/{user_id}/chat/stream", func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		tenant := r.PathValue("tenant_id")
		user := r.PathValue("user_id")
		key := fmt.Sprintf("%s:%s:%s", tenant, user, req.SessionID)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		_, err := app.ChatStream(r.Context(), orbit.ChatInput{
			TenantID: tenant, UserID: user, SessionKey: key, Prompt: req.Prompt,
		}, func(chunk string) {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		})
		if err != nil {
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
			flusher.Flush()
			return
		}
		fmt.Fprint(w, "event: done\ndata: [DONE]\n\n")
		flusher.Flush()
	})

	server := &http.Server{Addr: ":4116", Handler: mux}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down...")
		_ = server.Close()
		cancel()
	}()

	fmt.Println("[genkit-go orbit] real Genkit build serving on :4116 (model:", orbit.FakeModelName+")")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
