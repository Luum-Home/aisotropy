#!/usr/bin/env node
// aisotropy plugin template — a minimal stdio MCP server (zero dependencies).
//
// This is the executable the plugin manifest (aisotropy-plugin.yaml) launches:
// a Model Context Protocol server speaking newline-delimited JSON-RPC 2.0 over
// stdin/stdout. Aisotropy spawns it as a subprocess (ADR-0016 lever #2), lists
// its tools, and re-exposes each one as a *scope-gated* Aisotropy tool. The Go
// core keeps owning tenant scoping and identity; this process only computes a
// result from validated arguments.
//
// It has NO build step and NO npm dependencies on purpose — it implements just
// enough of MCP (initialize, tools/list, tools/call) by hand so a plugin author
// can copy it, run `node server.js`, and see it work. Graduate to the official
// SDK (`@modelcontextprotocol/sdk`) once the tool surface grows.
//
// The MCP transport is stdout-only for protocol frames: everything this server
// prints to stdout MUST be a single-line JSON-RPC message. Logs go to stderr.

'use strict';

const readline = require('readline');

// The wire protocol version Aisotropy's MCP client (gollem ext/mcp) speaks.
const PROTOCOL_VERSION = '2025-11-25';

// ---------------------------------------------------------------------------
// The tool this plugin exposes.
//
// One tool: `word_count`. The JSON Schema below is the contract — Aisotropy
// shows it to the model verbatim and validates the model's arguments against it
// BEFORE the call ever crosses into this process (a violation reflects back to
// the model, never reaches `handler`). Keep the schema honest: every field the
// handler reads must be declared here.
// ---------------------------------------------------------------------------
const TOOLS = [
  {
    name: 'word_count',
    description:
      'Count the words and characters in a piece of text. Returns a short summary.',
    inputSchema: {
      type: 'object',
      properties: {
        text: {
          type: 'string',
          description: 'The text to analyze.',
        },
      },
      required: ['text'],
      additionalProperties: false,
    },
    handler(args) {
      const text = String(args.text ?? '');
      const words = text.split(/\s+/).filter(Boolean);
      return `${words.length} word(s), ${text.length} character(s).`;
    },
  },
];

// ---------------------------------------------------------------------------
// JSON-RPC plumbing. One message per line in, one message per line out.
// ---------------------------------------------------------------------------

// send writes a single JSON-RPC frame followed by the newline the transport
// uses as its delimiter. Responses without an `id` are dropped (a notification
// gets no reply).
function send(message) {
  if (message === null || message === undefined) return;
  process.stdout.write(`${JSON.stringify(message)}\n`);
}

function result(id, value) {
  return { jsonrpc: '2.0', id, result: value };
}

function error(id, code, message) {
  return { jsonrpc: '2.0', id, error: { code, message } };
}

// handle dispatches a single decoded request and returns the frame to send back
// (or null for notifications, which expect no response).
function handle(req) {
  const { id, method, params } = req;

  switch (method) {
    // The handshake. We advertise a single capability — tools — and echo the
    // protocol version so the client knows we speak its dialect.
    case 'initialize':
      return result(id, {
        protocolVersion: PROTOCOL_VERSION,
        capabilities: { tools: {} },
        serverInfo: { name: 'aisotropy-plugin-template', version: '0.1.0' },
      });

    // Fire-and-forget: the client tells us it finished initializing. No reply.
    case 'notifications/initialized':
      return null;

    // Discovery: hand back the tool list without the local `handler` field,
    // which is an implementation detail the wire never sees.
    case 'tools/list':
      return result(id, {
        tools: TOOLS.map(({ name, description, inputSchema }) => ({
          name,
          description,
          inputSchema,
        })),
      });

    // Execution: find the tool, run its handler on the (already schema-checked)
    // arguments, and wrap the string in the MCP text-content envelope. A thrown
    // handler becomes an MCP tool error (isError) rather than a transport crash.
    case 'tools/call': {
      const { name, arguments: args } = params || {};
      const tool = TOOLS.find((t) => t.name === name);
      if (!tool) {
        return error(id, -32602, `unknown tool: ${name}`);
      }
      try {
        const text = tool.handler(args || {});
        return result(id, { content: [{ type: 'text', text }] });
      } catch (err) {
        return result(id, {
          content: [{ type: 'text', text: `error: ${err.message}` }],
          isError: true,
        });
      }
    }

    default:
      // Requests carry an id and deserve a "method not found"; notifications
      // (no id) are silently ignored.
      if (id === undefined || id === null) return null;
      return error(id, -32601, `method not found: ${method}`);
  }
}

// ---------------------------------------------------------------------------
// Main loop: read stdin line by line, decode, dispatch, reply.
// ---------------------------------------------------------------------------
const rl = readline.createInterface({ input: process.stdin, terminal: false });

rl.on('line', (line) => {
  const trimmed = line.trim();
  if (!trimmed) return;
  let req;
  try {
    req = JSON.parse(trimmed);
  } catch (_err) {
    // A frame we cannot parse has no id to reply to — log and move on.
    process.stderr.write(`aisotropy-plugin-template: skipping unparseable line\n`);
    return;
  }
  send(handle(req));
});

rl.on('close', () => process.exit(0));
