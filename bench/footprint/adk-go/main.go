// Minimal "hello agent" app on the REAL Google ADK Go framework
// (google.golang.org/adk/v2) — footprint benchmark subject.
//
// The MODEL IS HELD CONSTANT as a deterministic offline fake implementing the
// framework's own model.LLM seam (no provider, no network). This measures the
// ADK-Go framework FOOTPRINT compiled as a static Go binary on distroless — the
// SAME packaging aisotropy uses. It is the same-language control: any size /
// RSS gap vs aisotropy is a FRAMEWORK difference, not a language one.
//
// Boot sequence, matched to the other subjects: construct llmagent + runner +
// in-memory session service -> run ONE warmup turn -> serve GET /healthz on :8080.
package main

import (
	"context"
	"iter"
	"log"
	"net/http"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// fakeModel is a deterministic, offline implementation of the real ADK model.LLM
// interface — the "model held constant". It never touches the network.
type fakeModel struct{}

func (fakeModel) Name() string { return "offline-stub" }

func (fakeModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content: &genai.Content{
				Role:  genai.RoleModel,
				Parts: []*genai.Part{{Text: "ACK offline stub"}},
			},
		}, nil)
	}
}

func main() {
	ag, err := llmagent.New(llmagent.Config{
		Name:        "footprint",
		Model:       fakeModel{},
		Description: "Footprint-benchmark hello agent.",
		Instruction: "You are an offline stub agent.",
	})
	if err != nil {
		log.Fatalf("llmagent.New: %v", err)
	}

	svc := session.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:           "footprint",
		Agent:             ag,
		SessionService:    svc,
		AutoCreateSession: true,
	})
	if err != nil {
		log.Fatalf("runner.New: %v", err)
	}

	// One warmup turn through the real ADK runner.
	msg := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{genai.NewPartFromText("benchmark warmup")}}
	for _, err := range r.Run(context.Background(), "u", "s", msg, agent.RunConfig{}) {
		if err != nil {
			log.Printf("warmup turn error: %v", err)
		}
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	log.Println("ready")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
