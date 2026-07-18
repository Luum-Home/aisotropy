#!/usr/bin/env bash
# smoke.sh — proves the nimbus-status MCP server actually speaks MCP.
#
# Spawns `node server.mjs` as a child process and drives a raw JSON-RPC 2.0
# / MCP handshake against its stdin/stdout using a tiny inline Node client
# (no SDK client dependency — this deliberately re-implements the minimal
# wire protocol by hand so the proof does not depend on the same SDK code
# the server itself uses):
#
#   1. initialize            (client -> server)
#   2. notifications/initialized (client -> server, no response expected)
#   3. tools/list             -> asserts "nimbus_status" is present
#   4. tools/call             -> "Skylark Line"  -> asserts status=operational
#   5. tools/call             -> "Nonexistent Line" -> asserts status=unknown
#
# Every JSON-RPC frame sent and received is printed so the transcript is a
# verifiable artifact, not a claim. Exit code is 0 iff every assertion
# passes; any failure prints a diagnostic and exits non-zero.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$DIR"

if [ ! -d node_modules ]; then
  echo "PROGRESS: node_modules missing — running npm install" >&2
  npm install --no-audit --no-fund >&2
fi

CLIENT_DIR="$(mktemp -d -t nimbus-mcp-smoke.XXXXXX)"
CLIENT_FILE="$CLIENT_DIR/smoke-client.mjs"
trap 'rm -rf "$CLIENT_DIR"' EXIT

cat > "$CLIENT_FILE" <<'NODE_CLIENT'
// Tiny inline MCP client: hand-rolled JSON-RPC 2.0 over newline-delimited
// stdio framing (the same framing arenas/live/_shared/mcp-server/server.mjs
// uses via StdioServerTransport — see node_modules/@modelcontextprotocol/sdk
// dist/esm/shared/stdio.js: one JSON object per line, "\n"-terminated).
import { spawn } from "node:child_process";

const PROTOCOL_VERSION = "2025-06-18"; // widely-supported MCP protocol date

function log(label, obj) {
  console.log(`\n--- ${label} ---`);
  console.log(JSON.stringify(obj, null, 2));
}

function fail(msg) {
  console.error(`SMOKE FAIL: ${msg}`);
  process.exit(1);
}

const child = spawn("node", ["server.mjs"], {
  stdio: ["pipe", "pipe", "pipe"],
});

child.stderr.on("data", (chunk) => {
  process.stderr.write(`[server stderr] ${chunk}`);
});

let buffer = "";
const pending = new Map(); // id -> resolve fn
let nextId = 1;

child.stdout.on("data", (chunk) => {
  buffer += chunk.toString("utf8");
  let idx;
  while ((idx = buffer.indexOf("\n")) !== -1) {
    const line = buffer.slice(0, idx).replace(/\r$/, "");
    buffer = buffer.slice(idx + 1);
    if (!line.trim()) continue;
    const msg = JSON.parse(line);
    if (msg.id !== undefined && pending.has(msg.id)) {
      const resolve = pending.get(msg.id);
      pending.delete(msg.id);
      resolve(msg);
    } else {
      log(`notification <- server`, msg);
    }
  }
});

function send(method, params) {
  const id = nextId++;
  const req = { jsonrpc: "2.0", id, method, params };
  log(`request -> server (${method})`, req);
  child.stdin.write(JSON.stringify(req) + "\n");
  return new Promise((resolve) => pending.set(id, resolve));
}

function sendNotification(method, params) {
  const note = { jsonrpc: "2.0", method, params };
  log(`notification -> server (${method})`, note);
  child.stdin.write(JSON.stringify(note) + "\n");
}

async function main() {
  // 1. initialize
  const initResp = await send("initialize", {
    protocolVersion: PROTOCOL_VERSION,
    capabilities: {},
    clientInfo: { name: "nimbus-smoke-client", version: "1.0.0" },
  });
  log("response <- server (initialize)", initResp);
  if (!initResp.result || !initResp.result.serverInfo) {
    fail("initialize did not return serverInfo");
  }
  if (initResp.result.serverInfo.name !== "nimbus-status-mcp-server") {
    fail(`unexpected serverInfo.name: ${initResp.result.serverInfo.name}`);
  }

  // 2. initialized notification (required by the MCP handshake before any
  // further requests; no response is expected).
  sendNotification("notifications/initialized", {});

  // 3. tools/list
  const listResp = await send("tools/list", {});
  log("response <- server (tools/list)", listResp);
  const tools = (listResp.result && listResp.result.tools) || [];
  const names = tools.map((t) => t.name);
  if (!names.includes("nimbus_status")) {
    fail(`tools/list did not include "nimbus_status" (got: ${names.join(", ")})`);
  }
  console.log(`\nASSERT OK: tools/list includes "nimbus_status" (tools: ${names.join(", ")})`);

  // 4. tools/call — known line, deterministic "operational" status.
  const callKnown = await send("tools/call", {
    name: "nimbus_status",
    arguments: { line: "Skylark Line" },
  });
  log("response <- server (tools/call nimbus_status Skylark Line)", callKnown);
  const knownStructured = callKnown.result && callKnown.result.structuredContent;
  if (!knownStructured || knownStructured.found !== true || knownStructured.status !== "operational") {
    fail(`tools/call("Skylark Line") did not return found=true status=operational (got: ${JSON.stringify(knownStructured)})`);
  }
  console.log(`\nASSERT OK: nimbus_status("Skylark Line") -> ${JSON.stringify(knownStructured)}`);

  // 5. tools/call — unknown line, deterministic "unknown" status (proves
  // the tool is a real lookup, not a hardcoded constant reply).
  const callUnknown = await send("tools/call", {
    name: "nimbus_status",
    arguments: { line: "Nonexistent Line" },
  });
  log("response <- server (tools/call nimbus_status Nonexistent Line)", callUnknown);
  const unknownStructured = callUnknown.result && callUnknown.result.structuredContent;
  if (!unknownStructured || unknownStructured.found !== false || unknownStructured.status !== "unknown") {
    fail(`tools/call("Nonexistent Line") did not return found=false status=unknown (got: ${JSON.stringify(unknownStructured)})`);
  }
  console.log(`\nASSERT OK: nimbus_status("Nonexistent Line") -> ${JSON.stringify(unknownStructured)}`);

  console.log("\nSMOKE PASS: initialize + tools/list + tools/call (known + unknown line) all verified.");
  child.stdin.end();
  child.kill();
  process.exit(0);
}

main().catch((err) => {
  console.error("SMOKE FAIL: unhandled error", err);
  child.kill();
  process.exit(1);
});
NODE_CLIENT

node "$CLIENT_FILE"
