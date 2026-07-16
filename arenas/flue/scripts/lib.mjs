// Shared harness for the offline smokes: boots the fake model + a built Flue
// Node server as real subprocesses and drives them over HTTP. No API key.
import { spawn } from 'node:child_process';
import { setTimeout as sleep } from 'node:timers/promises';

const ROOT = new URL('..', import.meta.url).pathname;

import { execSync } from 'node:child_process';

/** Free a TCP port by killing whatever holds it (stale server from a prior run). */
export function freePort(port) {
	try {
		const pids = execSync(`lsof -nP -tiTCP:${port} -sTCP:LISTEN`, { stdio: ['ignore', 'pipe', 'ignore'] })
			.toString().trim().split('\n').filter(Boolean);
		for (const pid of pids) {
			try { process.kill(Number(pid), 'SIGKILL'); } catch {}
		}
	} catch {}
}

export async function startFakeModel(port = 4117) {
	freePort(port);
	const proc = spawn('node', ['scripts/fake-model.mjs', String(port)], { cwd: ROOT, stdio: 'ignore' });
	const url = `http://127.0.0.1:${port}/v1`;
	// Readiness: probe our own /chat/completions until it answers 200 (proves OUR
	// fake owns the port, not a stale server that would 404).
	for (let i = 0; i < 50; i++) {
		try {
			const r = await fetch(`${url}/chat/completions`, {
				method: 'POST',
				headers: { 'content-type': 'application/json' },
				body: JSON.stringify({ messages: [{ role: 'user', content: 'ping' }] }),
			});
			if (r.ok) return { proc, url };
		} catch {}
		await sleep(150);
	}
	proc.kill('SIGKILL');
	throw new Error(`fake model on ${port} did not become ready`);
}

export async function startServer({ port, fakeUrl, dbPath }) {
	freePort(port);
	const proc = spawn('node', ['dist/server.mjs'], {
		cwd: ROOT,
		env: { ...process.env, PORT: String(port), ORBIT_FAKE_MODEL_URL: fakeUrl, ORBIT_DB_PATH: dbPath },
		stdio: 'ignore',
	});
	const base = `http://127.0.0.1:${port}`;
	for (let i = 0; i < 60; i++) {
		try {
			const r = await fetch(`${base}/health`);
			if (r.ok) return { proc, base };
		} catch {}
		await sleep(200);
	}
	proc.kill('SIGKILL');
	throw new Error(`server on ${port} did not become ready`);
}

export async function stop(proc) {
	if (!proc) return;
	proc.kill('SIGKILL');
	await sleep(150);
}

/**
 * Graceful stop: SIGTERM lets Flue stop the active submission at a turn
 * boundary and checkpoint the file-backed SQLite stream (docs: durable-execution
 * §"On graceful shutdown, active submissions stop at a turn boundary"). Waits for
 * the process to actually exit so the db file is fully flushed before restart.
 */
export async function stopGraceful(proc) {
	if (!proc) return;
	await new Promise((resolve) => {
		proc.once('exit', resolve);
		proc.kill('SIGTERM');
		setTimeout(() => {
			proc.kill('SIGKILL');
			resolve();
		}, 8000);
	});
	await sleep(200);
}

/** Prompt an Orbit agent instance. Returns { status, body }. */
export async function prompt(base, id, message, token) {
	const headers = { 'content-type': 'application/json' };
	if (token) headers.authorization = `Bearer ${token}`;
	const r = await fetch(`${base}/agents/orbit/${id}?wait=result`, {
		method: 'POST',
		headers,
		body: JSON.stringify({ message }),
	});
	let body;
	try {
		body = await r.json();
	} catch {
		body = { text: await r.text() };
	}
	return { status: r.status, body };
}

export function replyText(res) {
	return res.body?.result?.text ?? res.body?.text ?? JSON.stringify(res.body);
}

let failures = 0;
export function check(label, cond, detail = '') {
	const ok = !!cond;
	if (!ok) failures++;
	console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${label}${detail ? `  — ${detail}` : ''}`);
	return ok;
}
export function exitCode() {
	return failures === 0 ? 0 : 1;
}
