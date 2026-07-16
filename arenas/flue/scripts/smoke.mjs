// Orbit smoke on Flue — proves the DISTINCTIVE angle: durable execution gives
// crash-safe CONVERSATION memory. Seed a secret, KILL the server process, boot a
// fresh one against the same file-backed SQLite adapter, and show the new process
// recalls the secret from the durable canonical stream. Fully offline.
import { rm, mkdir } from 'node:fs/promises';
import { startFakeModel, startServer, stop, stopGraceful, prompt, replyText, check, exitCode } from './lib.mjs';

const DB = './data/smoke.db';
const ID = 't=acme;u=bob;p=analytics';
const TOKEN = 'acme:bob';

async function main() {
	await rm('./data', { recursive: true, force: true });
	await mkdir('./data', { recursive: true });
	const fake = await startFakeModel(4117);

	console.log('== Flue Orbit smoke (durable execution → crash-safe conversation memory) ==\n');

	// --- Process P1: seed a secret, then die ---
	console.log('[1] process P1: seed secret into durable stream');
	let s1 = await startServer({ port: 4119, fakeUrl: fake.url, dbPath: DB });
	const seed = await prompt(s1.base, ID, 'please remember: my secret code is ORBIT-SECRET-A', TOKEN);
	check('turn 1 accepted (200)', seed.status === 200, `status=${seed.status}`);
	check('model saw the secret this turn', /ORBIT-SECRET-A/.test(replyText(seed)));
	console.log(`      reply: ${replyText(seed)}`);
	console.log('[2] stop P1 at a turn boundary + checkpoint durable db, then exit');
	await stopGraceful(s1.proc);

	// --- Process P2: fresh server, same durable db, recall ---
	console.log('[3] process P2: fresh server on the SAME file-backed db');
	let s2 = await startServer({ port: 4120, fakeUrl: fake.url, dbPath: DB });
	const recall = await prompt(s2.base, ID, 'what was my secret code?', TOKEN);
	const rt = replyText(recall);
	console.log(`      reply: ${rt}`);
	check('turn 2 accepted (200)', recall.status === 200, `status=${recall.status}`);
	check('DURABLE MEMORY: P2 recalled the secret written by the dead P1', /ORBIT-SECRET-A/.test(rt));
	const m = rt.match(/seen_msgs=(\d+)/);
	check('context was rebuilt from the canonical stream (history > 1 turn)', m && Number(m[1]) >= 4,
		m ? `seen_msgs=${m[1]}` : 'no marker');

	await stop(s2.proc);
	await stop(fake.proc);
	console.log(`\n== smoke ${exitCode() === 0 ? 'PASSED' : 'FAILED'} ==`);
	process.exit(exitCode());
}
main().catch((e) => {
	console.error(e);
	process.exit(1);
});
