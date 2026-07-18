# nimbus-status-mcp-server — shared extensibility-test fixture (`_shared/mcp-server/`)

A trivial, **real** [Model Context Protocol](https://modelcontextprotocol.io) stdio
server, built with the official `@modelcontextprotocol/sdk` (MIT). It is the shared
foundation for a new arena dimension — **`extensibility`**: can a framework load an
external extension (an MCP tool) at runtime and actually use its result inside a real
agent turn against `agnes-2.0-flash`?

MCP is the emerging cross-framework standard for exactly this — every major
framework in the live arena ships (or can consume) an MCP client — so it is the fair,
apples-to-apples extensibility test: one server, one tool, every framework connects to
it the same way a real user's MCP-based extension would be connected.

Nothing here is stubbed. The server is a real child process speaking real JSON-RPC
2.0 over real stdin/stdout; the tool performs a real (if simple) deterministic lookup,
not a canned string baked into a prompt.

---

## 1. The server

### The tool: `nimbus_status(line: string)`

Ties into the arena's existing RAG corpus theme
(`arenas/live/_shared/corpus/` — the fictional **Nimbus Rail** maglev operator). Given
a line name, it returns that line's operating status from a fixed, in-memory lookup
table — no model call, no randomness, so the correct output for a given input is
always the same and mechanically checkable.

| Line (case-insensitive) | `status` | `found` |
|---|---|---|
| `Skylark Line` | `operational` | `true` |
| `Marrow Loop` | `operational` | `true` |
| `Halcyon Pass` | `seasonal-closure` | `true` |
| anything else | `unknown` | `false` |

Request/response shape (MCP `tools/call`):

```jsonc
// arguments
{ "line": "Skylark Line" }

// structuredContent
{
  "found": true,
  "line": "Skylark Line",
  "status": "operational",
  "note": "Flagship passenger maglev route, 47 stations, Vellmoor Central to Ashcombe, 71 min end-to-end."
}
```

This unambiguous, verifiable output is exactly what makes the extensibility mode
scoreable: an adapter (or a human) can check the model's final reply against the
known table above and know for certain whether the tool's result was actually used,
versus the model guessing or refusing.

### Files

| Path | Role |
|---|---|
| `package.json` | Deps: `@modelcontextprotocol/sdk` (MIT), `zod` (MIT). No build step — plain ESM `.mjs`. |
| `server.mjs` | The MCP server: `McpServer` + `StdioServerTransport`, one `nimbus_status` tool. |
| `start.sh` | Launcher a client execs: `npm install` if needed, then `exec node server.mjs`. |
| `smoke.sh` | Verification: spawns the server and drives a real handshake with a tiny inline JSON-RPC client (no SDK client dependency — proves the wire protocol, not just the SDK's own round-trip). |
| `README.md` | This document — the extensibility-mode contract every adapter implements against. |

### Running it directly

```bash
cd arenas/live/_shared/mcp-server
npm install         # clean install, MIT/BSD/Apache deps only (see §4)
./start.sh           # foreground; a client wires its stdin/stdout to this process
```

A client never talks HTTP to this server and it opens no port — it is spawned as a
child process and communicates over the child's stdin/stdout, exactly like every
`arenas/live/<fw>/run.sh` adapter itself is spawned by `arenas/live/runner/` per the
uniform contract in `../SPEC.md`.

### Verifying it (`smoke.sh`)

```bash
cd arenas/live/_shared/mcp-server
./smoke.sh
```

`smoke.sh` spawns `node server.mjs`, then performs, over raw newline-delimited
JSON-RPC 2.0 (the framing `StdioServerTransport` actually uses — one JSON object per
line, no `Content-Length` headers):

1. `initialize` (protocol handshake, asserts `serverInfo.name`)
2. `notifications/initialized`
3. `tools/list` — asserts `nimbus_status` is present
4. `tools/call` for `"Skylark Line"` — asserts `found:true, status:"operational"`
5. `tools/call` for `"Nonexistent Line"` — asserts `found:false, status:"unknown"` (proves
   this is a real lookup, not a hardcoded reply)

Every request/response frame is printed; the script exits `0` iff all five assertions
pass. This is the executable proof that the server actually speaks MCP — see the
transcript captured when this was last run, reproduced in full in the delivery
report for this change.

---

## 2. The extensibility-mode contract

Every `arenas/live/<fw>/` adapter that implements the `extensibility` mode follows
this contract so the comparison is honest and framework differences — not test
differences — are what gets measured.

### What an adapter does

1. **Spawn** this server: `arenas/live/_shared/mcp-server/start.sh` (or
   `node arenas/live/_shared/mcp-server/server.mjs` directly) as a child process.
2. **Connect** as an MCP client using the framework's own MCP integration — this is
   the point of the test, so each framework uses its native mechanism:

   | Framework | MCP client mechanism |
   |---|---|
   | LangGraph / LangChain | `langchain-mcp-adapters` (`langchain_mcp_adapters.client.MultiServerMCPClient`) |
   | LlamaIndex | LlamaIndex's built-in `llama-index-tools-mcp` `McpToolSpec` / `BasicMCPClient` |
   | Mastra | Mastra's native `MCPClient` (`@mastra/mcp`) |
   | Vercel AI SDK | the `ai` package's experimental MCP client (`experimental_createMCPClient`) |
   | Genkit (Go) | Genkit's MCP plugin (`genkit-mcp` / the Go `mcp` plugin) |
   | Eino | Eino's MCP tool adapter (`eino-ext/components/tool/mcp`) |
   | **aisotropy** | Two supported mechanisms — an adapter picks ONE and reports which in `extension_kind`: (a) **native MCP consumption** — aisotropy connects to this server as an MCP client and registers its tool(s) through the same `aisotropy.WithTool` / `WithToolExecutor` seam used by `tools` mode; or (b) aisotropy's own declarative **`aisotropy-plugin.yaml`** manifest — its native, non-MCP extension mechanism, listed here for contrast since it is the one framework-specific alternative to the MCP standard in this arena. |

3. **List** the tools the server exposes (`tools/list`) inside a real agent turn.
4. **Invoke** `nimbus_status` for one of the three known lines inside that same real
   agent turn against `agnes-2.0-flash` (per `../SPEC.md`'s `OPENAI_*` env).
5. **Use** the tool's result in the model's final reply — not just call it and
   discard the output.

### stdout contract

The adapter's `extensibility` mode writes the arena's standard envelope (`../SPEC.md`)
plus extensibility-specific fields:

```jsonc
{
  "framework": "string",
  "mode": "extensibility",
  "model": "agnes-2.0-flash",
  "extension_loaded": true,
  "extension_kind": "mcp",            // "mcp" | "plugin-manifest"
  "tools_discovered": ["nimbus_status"],
  "tool_invoked": "nimbus_status",
  "tool_result_used": true,
  "reply": "The Skylark Line is currently operational.",
  "input_tokens": 0,
  "output_tokens": 0,
  "latency_ms": 0,
  "tool_calls": [{"name": "nimbus_status", "arguments": {"line": "Skylark Line"}}],
  "error": ""
}
```

Field notes (additive to `../SPEC.md`'s base envelope, same rules apply — provider-
reported tokens only, errors are data not crashes, STDOUT is JSON-only):

| Field | Meaning |
|---|---|
| `extension_loaded` | `true` iff the adapter successfully connected to the extension (MCP handshake completed, or the plugin manifest was loaded) and it was available to the model. |
| `extension_kind` | `"mcp"` for every framework that connects to this server as an MCP client; `"plugin-manifest"` only for the aisotropy adapter when it exercises `aisotropy-plugin.yaml` instead of MCP. |
| `tools_discovered` | Tool names returned by `tools/list` (or the plugin manifest's declared tools). Must include `"nimbus_status"` for a real connection. |
| `tool_invoked` | Name of the tool the model actually called during the turn (empty string if none). |
| `tool_result_used` | `true` iff the tool's `structuredContent`/result appears reflected in the final `reply` (e.g. the reply states the correct `status` for the line asked about) — not merely that the call happened. |

### Scoring

**PASS** iff `extension_loaded && tool_invoked != "" && tool_result_used`. Because
`nimbus_status`'s output is a fixed table (§1), `tool_result_used` is objectively
checkable: the reply must state the correct `status` for the line the model asked
about, not an invented or generic one — the same anti-hallucination discipline the
RAG corpus's trap questions already apply (`../_shared/README.md` §4).

### Licensing

Adapters implementing `extensibility` mode must keep their MCP client dependency
MIT/BSD/Apache-2.0, per the arena's constitutional license gate (`../SPEC.md`
"Licensing"). See §4 below for this server's own dependency tree.

---

## 3. How to add this to a framework adapter

1. In `arenas/live/<fw>/`, add an `extensibility` mode (see e.g.
   `arenas/live/langgraph/modes/` or `arenas/live/ai-sdk/lib/modes/` for the existing
   per-mode dispatch pattern used in this arena).
2. Spawn `arenas/live/_shared/mcp-server/start.sh` as a child process (resolve the
   path relative to the adapter's own directory, the same way other adapters resolve
   `../_shared/embed-server` — see `arenas/live/langgraph/modes/common.py`).
3. Connect using the framework's native MCP client (table in §2).
4. Drive one real turn: list tools, let the model call `nimbus_status`, confirm the
   reply reflects the result, emit the stdout contract in §2.
5. Terminate the child process cleanly on exit (no orphaned server processes).

This document plus `server.mjs`/`smoke.sh` are the only new files this change adds —
implementing the `extensibility` mode in each of the seven framework adapters is
follow-up work, out of scope here.

---

## 4. Dependencies & licenses

`npm install` in this directory pulls exactly the packages below (verified via
`npm ls --all` against every installed `package.json`'s `license` field — 84 unique
packages, all MIT / BSD-2-Clause / BSD-3-Clause / ISC, zero GPL/AGPL/SSPL/BSL):

| Dependency | License |
|---|---|
| `@modelcontextprotocol/sdk` | MIT |
| `zod` | MIT |
| `express` (SDK transitive, HTTP transport support unused by this stdio server) | MIT |
| `hono` (SDK transitive) | MIT |
| `jose` (SDK transitive) | MIT |
| `ajv` / `ajv-formats` (SDK transitive, schema validation) | MIT |
| `cors` (SDK transitive) | MIT |
| `cross-spawn` (SDK transitive) | MIT |
| `eventsource` / `eventsource-parser` (SDK transitive) | MIT |
| `pkce-challenge` (SDK transitive) | MIT |
| `qs` (transitive of `express`) | BSD-3-Clause |
| `fast-uri` (transitive of `ajv`) | BSD-3-Clause |
| `json-schema-typed` (SDK transitive) | BSD-2-Clause |
| `zod-to-json-schema` (SDK transitive) | ISC |
| `inherits`, `once`, `wrappy`, `isexe`, `which`, `setprototypeof` (transitive) | ISC |
| *(remaining ~70 transitive packages — body-parser, router, send, statuses, etc.)* | MIT |

All pass the repo's constitutional license gate (no GPL/AGPL/SSPL/BSL) —
`.claude/rules/cos/license-policy.md`. Full per-package license verification command:

```bash
find node_modules -maxdepth 2 -name package.json | while read f; do
  node -e 'const p=require(process.argv[1]); console.log(p.name+"@"+p.version+"\t"+(p.license||"UNKNOWN"))' "$f"
done | sort -u
```
