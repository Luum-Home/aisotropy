import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

// Runs tests INSIDE a real workerd (via miniflare) — the same runtime that
// powers `wrangler dev`. Durable Objects + their embedded SQLite are real here,
// so the isolation proof exercises the genuine platform boundary, offline.
export default defineConfig({
  plugins: [
    cloudflareTest({
      wrangler: { configPath: "./wrangler.jsonc" },
    }),
  ],
});
