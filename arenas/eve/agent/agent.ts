import { defineAgent } from "eve";
import { orbitMockModel } from "#lib/model.js";

// `model` accepts an AI-SDK `LanguageModel` instance directly (the public type
// is `string | LanguageModel`), so we hand it the offline deterministic mock.
// On Vercel you would author `model: "anthropic/claude-sonnet-5"` and let the
// AI Gateway resolve it.
export default defineAgent({
  model: orbitMockModel,
  // Escape hatch: the mock model has no AI Gateway context-window metadata, so we
  // declare it verbatim (skips the gateway lookup for both the turn model and the
  // compaction summary model).
  modelContextWindowTokens: 128_000,
  compaction: {
    modelContextWindowTokens: 128_000,
  },
});
