// Typed aisotropyd client.
//
// Two transports, both typed against the SAME generated `schema.ts`
// (which is `openapi-typescript` output from the runtime's own /openapi.json):
//
//   1. `createChatClient()` — a thin `openapi-fetch` wrapper for the
//      request/response JSON routes (POST .../chat, non-streaming). openapi-fetch
//      gives compile-time-checked paths, params, and bodies with zero codegen of
//      client methods.
//
//   2. `streamChat()` — the SSE transport. openapi-fetch does not consume
//      `text/event-stream`, so this is a hand-written `fetch` reader that parses
//      the SSE frames the runtime emits (`event: token|done|error` + `data: {json}`),
//      typed against the generated Stream*Event component schemas. Per the transport
//      bake-off, SSE-over-HTTP is the default streaming wire format; the runtime's
//      /chat/stream route returns `text/event-stream`.
//
// No adopter-specific code here — this is the reference consumer surface.

import createClient from "openapi-fetch";
import type { paths, components } from "./schema.js";

export type ChatRequest = components["schemas"]["ChatRequest"];
export type ChatOutputBody = components["schemas"]["ChatOutputBody"];
export type StreamTokenEvent = components["schemas"]["StreamTokenEvent"];
export type StreamDoneEvent = components["schemas"]["StreamDoneEvent"];
export type StreamErrorEvent = components["schemas"]["StreamErrorEvent"];
export type ErrorModel = components["schemas"]["ErrorModel"];

export interface ClientOptions {
  /** e.g. "http://127.0.0.1:8080" — no trailing slash. */
  baseUrl: string;
  /** Optional bearer token. Sent as `Authorization: Bearer <token>` when set.
   *  Required unless the server runs in NoAuth (AISOTROPY_ALLOW_NOAUTH=1) mode. */
  token?: string;
}

function authHeaders(token?: string): Record<string, string> {
  return token ? { Authorization: `Bearer ${token}` } : {};
}

/**
 * openapi-fetch client for the JSON (non-streaming) routes. The generated `paths`
 * type makes every path/param/body compile-time checked.
 */
export function createChatClient(opts: ClientOptions) {
  return createClient<paths>({
    baseUrl: opts.baseUrl,
    headers: authHeaders(opts.token),
  });
}

/**
 * Non-streaming chat. Returns the schema-enforced reply body, or throws with the
 * RFC-7807 problem body (ErrorModel) on a non-2xx.
 */
export async function chat(
  opts: ClientOptions,
  tenantId: string,
  userId: string,
  body: ChatRequest,
): Promise<ChatOutputBody> {
  const client = createChatClient(opts);
  const { data, error } = await client.POST(
    "/v1/tenant/{tenant_id}/user/{user_id}/chat",
    {
      params: { path: { tenant_id: tenantId, user_id: userId } },
      body,
    },
  );
  if (error) {
    const e = error as ErrorModel;
    throw new Error(
      `chat failed: ${e.status ?? "?"} ${e.title ?? ""} — ${e.detail ?? ""}`.trim(),
    );
  }
  // openapi-fetch narrows `data` to ChatOutputBody on the 200 branch.
  return data;
}

export interface StreamCallbacks {
  onToken: (ev: StreamTokenEvent) => void;
  onDone?: (ev: StreamDoneEvent) => void;
  onError?: (ev: StreamErrorEvent) => void;
}

/**
 * SSE streaming chat. Opens POST .../chat/stream, parses the `text/event-stream`
 * body incrementally, and dispatches typed events. Honors AbortSignal — aborting
 * the fetch cancels the upstream LLM call (the runtime respects ctx cancellation).
 *
 * The runtime frames each event as:
 *   event: token\n data: {"text":"..."}\n\n
 *   event: done\n  data: {"tenant_id":"..."}\n\n
 *   event: error\n data: {"message":"..."}\n\n
 */
export async function streamChat(
  opts: ClientOptions,
  tenantId: string,
  userId: string,
  body: ChatRequest,
  cb: StreamCallbacks,
  signal?: AbortSignal,
): Promise<void> {
  const url =
    `${opts.baseUrl}/v1/tenant/${encodeURIComponent(tenantId)}` +
    `/user/${encodeURIComponent(userId)}/chat/stream`;

  const res = await fetch(url, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      accept: "text/event-stream",
      ...authHeaders(opts.token),
    },
    body: JSON.stringify(body),
    signal,
  });

  if (!res.ok || !res.body) {
    let detail = res.statusText;
    try {
      const e = (await res.json()) as ErrorModel;
      detail = `${e.status ?? res.status} ${e.title ?? ""} — ${e.detail ?? ""}`.trim();
    } catch {
      /* body was not JSON */
    }
    throw new Error(`stream failed: ${detail}`);
  }

  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = "";

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += value;

    // SSE events are separated by a blank line (\n\n).
    let sep: number;
    while ((sep = buffer.indexOf("\n\n")) !== -1) {
      const rawEvent = buffer.slice(0, sep);
      buffer = buffer.slice(sep + 2);
      dispatch(rawEvent, cb);
    }
  }
  // Flush any trailing event without a terminating blank line.
  if (buffer.trim()) dispatch(buffer, cb);
}

function dispatch(rawEvent: string, cb: StreamCallbacks): void {
  let eventName = "message";
  const dataLines: string[] = [];
  for (const line of rawEvent.split("\n")) {
    if (line.startsWith("event:")) eventName = line.slice(6).trim();
    else if (line.startsWith("data:")) dataLines.push(line.slice(5).trim());
    // lines starting with ":" are comments/heartbeats — ignored.
  }
  if (dataLines.length === 0) return;
  const data = JSON.parse(dataLines.join("\n"));
  switch (eventName) {
    case "token":
      cb.onToken(data as StreamTokenEvent);
      break;
    case "done":
      cb.onDone?.(data as StreamDoneEvent);
      break;
    case "error":
      cb.onError?.(data as StreamErrorEvent);
      break;
  }
}
