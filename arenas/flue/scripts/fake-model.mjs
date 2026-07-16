// Offline fake model speaking the OpenAI `openai-completions` wire protocol that
// Flue/pi-ai calls. No API key, no network. Deterministic so the arena is a real
// executed integration with a stable transcript.
//
// The reply is engineered to make memory + isolation OBSERVABLE:
//   - it reports how many prior messages it was given (durable-memory hydration),
//   - it echoes any ORBIT-SECRET-* token found anywhere in the messages it saw
//     (so a cross-tenant read is visible as a leaked secret in the reply).
import http from 'node:http';

const PORT = Number(process.env.PORT || process.argv[2] || 4117);

function replyText(messages) {
	const joined = messages.map((m) => (typeof m.content === 'string' ? m.content : JSON.stringify(m.content))).join('\n');
	const secrets = [...joined.matchAll(/ORBIT-SECRET-[A-Z0-9]+/g)].map((m) => m[0]);
	const lastUser = [...messages].reverse().find((m) => m.role === 'user');
	const last = typeof lastUser?.content === 'string' ? lastUser.content : '';
	return `ORBIT_REPLY :: seen_msgs=${messages.length} :: secrets=[${[...new Set(secrets)].join(',')}] :: last="${last}"`;
}

const server = http.createServer((req, res) => {
	if (req.method !== 'POST' || !req.url.includes('/chat/completions')) {
		res.writeHead(404).end('not found');
		return;
	}
	let body = '';
	req.on('data', (c) => (body += c));
	req.on('end', () => {
		let messages = [];
		let stream = false;
		try {
			const parsed = JSON.parse(body || '{}');
			messages = parsed.messages || [];
			stream = !!parsed.stream;
		} catch {}
		const text = replyText(messages);
		const id = 'chatcmpl-fake';
		const model = 'echo-1';
		const usage = { prompt_tokens: messages.length * 5, completion_tokens: 8, total_tokens: messages.length * 5 + 8 };

		if (!stream) {
			const payload = {
				id, object: 'chat.completion', created: Math.floor(Date.now() / 1000), model,
				choices: [{ index: 0, message: { role: 'assistant', content: text }, finish_reason: 'stop' }],
				usage,
			};
			res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify(payload));
			return;
		}

		res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache', connection: 'keep-alive' });
		const base = { id, object: 'chat.completion.chunk', created: Math.floor(Date.now() / 1000), model };
		res.write(`data: ${JSON.stringify({ ...base, choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }] })}\n\n`);
		res.write(`data: ${JSON.stringify({ ...base, choices: [{ index: 0, delta: { content: text }, finish_reason: null }] })}\n\n`);
		res.write(`data: ${JSON.stringify({ ...base, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }], usage })}\n\n`);
		res.write('data: [DONE]\n\n');
		res.end();
	});
});

server.listen(PORT, '127.0.0.1', () => {
	const addr = server.address();
	process.stdout.write(`fake-model listening http://127.0.0.1:${addr.port}/v1\n`);
});
