// app.go builds the aisotropy façade App the modes share and reads the
// provider-reported token usage back out. The App auto-wires its REAL backend
// tiers from the environment (AISOTROPY_EMBED_URL -> e5 APIEmbedder, QDRANT_HOST
// -> real Qdrant semantic tier, RQLITE_URL -> real rqlite facts tier,
// AISOTROPY_DATA_DIR -> on-disk BadgerDB working memory) — exactly the seams a
// production consumer configures — so no mode here falls back to the in-process
// stub stores. run.sh / run-docker.sh export those vars; the adapter never
// hardcodes them.
package main

import (
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

const productID = "arena"

// newApp builds the shared App: the usage-capturing agnes client injected via
// WithLLM (OPENAI_* selects the real model), the request's system prompt carried
// as the product persona, and any extra options a mode needs (e.g. an
// authenticator for the App.Run modes). The semantic embedder + stores are
// resolved by the runtime from the environment, so a real e5/Qdrant/rqlite/Badger
// stack is used whenever those vars are set.
func newApp(agnes *agnesClient, persona string, extra ...aisotropy.Option) *aisotropy.App {
	opts := []aisotropy.Option{aisotropy.WithLLM(agnes)}
	if persona != "" {
		opts = append(opts, aisotropy.WithPersona(productID, aisotropy.Persona{BasePrompt: persona}))
	}
	opts = append(opts, extra...)
	return aisotropy.New(opts...)
}

// setUsage folds the agnes client's accumulated provider-reported usage into the
// result (or -1/-1 when the provider never reported it, per the arena contract).
func setUsage(out *result, agnes *agnesClient) {
	out.InputTokens, out.OutputTokens = agnes.usage()
}

// sessionID returns the request session_id or a fresh unique one.
func sessionID(req *request) string {
	if req.SessionID != "" {
		return req.SessionID
	}
	return "arena-" + time.Now().Format("20060102-150405.000000000")
}
