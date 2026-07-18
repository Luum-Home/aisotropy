#!/usr/bin/env node
/**
 * nimbus-status-mcp-server — a trivial, real MCP stdio server.
 *
 * This is the shared extensibility-test fixture for arenas/live (see
 * ../SPEC.md and ./README.md for the full contract). It exposes exactly one
 * deterministic tool, `nimbus_status`, tied to the arena's existing RAG
 * corpus theme (Nimbus Rail, arenas/live/_shared/corpus/). The tool's output
 * is a fixed lookup table, not an LLM call and not randomness, so a caller's
 * "did it actually invoke the extension and use the result" is mechanically
 * checkable: the reply must contain the exact status string for the line
 * asked about.
 *
 * Transport: MCP over stdio (JSON-RPC 2.0, newline-delimited on
 * stdin/stdout). Every diagnostic goes to stderr — stdout is reserved for
 * the MCP protocol, exactly like the arena's own stdin/stdout adapter
 * contract (../SPEC.md).
 */

import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { z } from "zod";

const SERVER_NAME = "nimbus-status-mcp-server";
const SERVER_VERSION = "1.0.0";

// Deterministic, hand-authored status board. Keys are canonicalized
// (lowercased, whitespace-collapsed) at lookup time so "Skylark Line",
// "skylark line", and "  Skylark   Line " all resolve identically. Facts
// are consistent with arenas/live/_shared/corpus/*.md (the Nimbus Rail RAG
// corpus) but this tool does NOT read that corpus or call any model — it is
// a plain, in-memory table, so its output for a given line never changes.
const LINE_STATUS = new Map([
  [
    "skylark line",
    {
      line: "Skylark Line",
      status: "operational",
      note: "Flagship passenger maglev route, 47 stations, Vellmoor Central to Ashcombe, 71 min end-to-end.",
    },
  ],
  [
    "marrow loop",
    {
      line: "Marrow Loop",
      status: "operational",
      note: "Freight-only closed loop serving the Vellmoor port terminals, runs 22:00-05:00, no passengers carried.",
    },
  ],
  [
    "halcyon pass",
    {
      line: "Halcyon Pass",
      status: "seasonal-closure",
      note: "Mountain route branching off the Skylark Line at Ashcombe; closed 1 Dec - 1 Mar each year under the Frostbrake protocol.",
    },
  ],
]);

function canonicalize(line) {
  return String(line).trim().toLowerCase().replace(/\s+/g, " ");
}

function lookupLineStatus(line) {
  const key = canonicalize(line);
  const hit = LINE_STATUS.get(key);
  if (hit) {
    return { found: true, ...hit };
  }
  return {
    found: false,
    line: String(line).trim(),
    status: "unknown",
    note: `No status record for this line. Known lines: ${[...LINE_STATUS.values()]
      .map((v) => v.line)
      .join(", ")}.`,
  };
}

const NimbusStatusInputSchema = z
  .object({
    line: z
      .string()
      .min(1, "line must not be empty")
      .describe(
        'Nimbus Rail line name to look up, e.g. "Skylark Line", "Marrow Loop", or "Halcyon Pass". Matching is case-insensitive and whitespace-tolerant.'
      ),
  })
  .strict();

const server = new McpServer({
  name: SERVER_NAME,
  version: SERVER_VERSION,
});

server.registerTool(
  "nimbus_status",
  {
    title: "Nimbus Rail line status",
    description: `Look up the current operating status of a Nimbus Rail maglev line by name.

This is a deterministic, offline lookup against a fixed status table — it does
NOT call a model and does NOT change over time, so the same line always
returns the same result. It exists to give an MCP client an unambiguous,
verifiable extension to call: the caller can check the reply against the
known table below.

Args:
  - line (string): line name, e.g. "Skylark Line", "Marrow Loop", "Halcyon Pass". Case-insensitive.

Returns (structuredContent):
  {
    "found": boolean,     // true if the line is in the status table
    "line": string,       // canonical line name (as looked up if not found)
    "status": string,     // "operational" | "seasonal-closure" | "unknown"
    "note": string        // human-readable detail
  }

Known lines and their status: Skylark Line=operational, Marrow Loop=operational,
Halcyon Pass=seasonal-closure. Any other line name returns status="unknown".`,
    inputSchema: NimbusStatusInputSchema.shape,
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false,
    },
  },
  async ({ line }) => {
    const result = lookupLineStatus(line);
    return {
      content: [{ type: "text", text: JSON.stringify(result) }],
      structuredContent: result,
    };
  }
);

async function main() {
  const transport = new StdioServerTransport();
  await server.connect(transport);
  console.error(`${SERVER_NAME} v${SERVER_VERSION} — listening on stdio`);
}

main().catch((error) => {
  console.error("nimbus-status-mcp-server fatal error:", error);
  process.exit(1);
});
