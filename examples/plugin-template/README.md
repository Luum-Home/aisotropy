# plugin-template — the minimal Aisotropy plugin

The smallest thing an author copies to ship a **community plugin**: one manifest,
one out-of-process tool server, no build step, no Aisotropy recompile. It is an
MCP-server plugin (`kind: mcp`) — the polyglot path — so the tool can be written
in any language. This one is a zero-dependency Node script.

For the field-by-field reference and the trust model, read
[`docs/guides/writing-a-plugin.md`](../../docs/guides/writing-a-plugin.md).

## What's here

| File | Role |
|---|---|
| `aisotropy-plugin.yaml` | The manifest: identity, license, `kind: mcp`, `trust: sandboxed`, how to launch the server, the tool it contributes (with `required_scope`), and a persona identifier |
| `server.js` | A tiny stdio MCP server (no npm deps) exposing one tool, `word_count` |
| `.gitignore` | Keeps `node_modules/` etc. out of git if you graduate to the official SDK |

## Try the server on its own (no Aisotropy needed)

The server speaks newline-delimited JSON-RPC 2.0 over stdin/stdout. You can drive
it by hand to prove it runs before wiring it into Aisotropy:

```bash
cd examples/plugin-template

# list the tools it exposes
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | node server.js
# → {"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"word_count",...}]}}

# call the tool
printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"word_count","arguments":{"text":"hello there world"}}}' | node server.js
# → {"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"3 word(s), 17 character(s)."}]}}
```

Syntax-check it any time with `node --check server.js`.

## Point Aisotropy at it

Wired now via the `aisotropy.App` builder (or its env equivalent):

1. **Builder options.** When you construct the App
   ([build-your-own-agent](../../docs/guides/build-your-own-agent.md)):

   ```go
   app := aisotropy.New(
       aisotropy.WithPluginDir("examples"),                              // scan a dir
       // or aisotropy.WithPlugin("examples/plugin-template/aisotropy-plugin.yaml"),
   )
   ```

2. **Directory discovery by env.** Drop the plugin folder where Aisotropy scans
   and set the env var:

   ```bash
   export AISOTROPY_PLUGIN_DIR=/absolute/path/to/examples/plugin-template
   ```

   Either way Aisotropy reads `aisotropy-plugin.yaml`, launches `node server.js`,
   discovers `word_count`, and registers it as a **scope-gated** tool.

3. **CLI (forthcoming, not yet shipped).** A planned `aisotropy plugins add
   ./examples/plugin-template` / `aisotropy plugins list` command will manage a
   persistent registry from the shell. The manifest, loader, and registry ship
   now; the `plugins` subcommand is a named follow-on.

Either way the tool only reaches the model when the request grants its scope. The
manifest requires `text:analyze`, so a call must include it:

```bash
curl -s localhost:8080/v1/tenant/acme/user/alice/chat \
  -H 'content-type: application/json' \
  -d '{"prompt":"How many words are in: the quick brown fox?","product_id":"demo","session_id":"s1","scopes":["text:analyze"]}' | jq
```

Drop `text:analyze` from `scopes` and the model is never offered `word_count` —
a forbidden tool is invisible, not merely refused.

## Run keyless

Like every Aisotropy example, this needs **no API key and no Docker**. With no
provider key set, the runtime uses the built-in stub LLM: the whole request path
(validation → scope-gated tools → the plugin subprocess → memory → reflection)
runs, the reply is just canned (`"stub":true`). Set `ANTHROPIC_API_KEY` (or
another provider key) and the real model decides when to call `word_count` — no
code or manifest change.

## Make it yours

1. Rename the plugin (`name`, `author`) and bump `version` in the manifest.
2. Replace the `word_count` entry in `TOOLS` (in `server.js`) with your tool —
   keep its JSON Schema honest, since Aisotropy validates the model's args
   against it before your handler runs.
3. Mirror the tool's `name` and a `required_scope` in the manifest's `tools:`.
4. Verify: `node --check server.js`, then the two `printf | node server.js`
   probes above.
