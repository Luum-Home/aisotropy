// Orbit ISOLATION smoke on Flue — the crux. Runs through the REAL Flue runtime,
// fully offline. Proves exactly what Flue isolates by construction and what it
// leaves to developer convention.
//
// Flue's ONLY external scoping axis is the agent-instance `id` (one path
// segment). The canonical conversation stream is isolated per-id BY CONSTRUCTION.
// But (tenant,user,product) are NOT framework concepts — Orbit folds them into
// the one opaque id string, and the `route` guard is the only authorization.
// So isolation holds ONLY IF the developer (a) encodes every axis into the id
// without dropping one and (b) writes a correct route guard. Both are convention.
import { rm, mkdir } from 'node:fs/promises';
import { startFakeModel, startServer, stop, prompt, replyText, check, exitCode } from './lib.mjs';

const DB = './data/iso.db';
const SECRET = 'ORBIT-SECRET-A';

async function main() {
	await rm('./data/iso.db', { force: true }).catch(() => {});
	await mkdir('./data', { recursive: true });
	const fake = await startFakeModel(4127);
	const s = await startServer({ port: 4128, fakeUrl: fake.url, dbPath: DB });
	const B = s.base;

	console.log('== Flue Orbit isolation smoke (what the runtime isolates vs. what the dev must) ==\n');

	// Seed the secret into acme/bob/analytics.
	await prompt(B, 't=acme;u=bob;p=analytics', `secret is ${SECRET}`, 'acme:bob');

	console.log('[A] positive control + by-construction isolation (distinct id ⇒ distinct stream)');
	const own = await prompt(B, 't=acme;u=bob;p=analytics', 'what is my secret?', 'acme:bob');
	check('same instance id recalls its own secret', replyText(own).includes(SECRET));

	const otherTenant = await prompt(B, 't=globex;u=bob;p=analytics', 'what is my secret?', 'globex:bob');
	check('WRONG TENANT cannot read it (separate canonical stream)', !replyText(otherTenant).includes(SECRET),
		replyText(otherTenant));

	const otherUser = await prompt(B, 't=acme;u=alice;p=analytics', 'what is my secret?', 'acme:alice');
	check('WRONG USER cannot read it', !replyText(otherUser).includes(SECRET), replyText(otherUser));

	const otherProduct = await prompt(B, 't=acme;u=bob;p=billing', 'what is my secret?', 'acme:bob');
	check('WRONG PRODUCT cannot read it (when product is folded into the id correctly)',
		!replyText(otherProduct).includes(SECRET), replyText(otherProduct));

	console.log('\n[B] the authorization boundary is 100% developer-written (route guard)');
	const badToken = await prompt(B, 't=acme;u=bob;p=analytics', 'what is my secret?', 'globex:bob');
	check('mismatched bearer token ⇒ 401 (our guard; Flue authenticates nothing itself)',
		badToken.status === 401, `status=${badToken.status}`);
	const noToken = await prompt(B, 't=acme;u=bob;p=analytics', 'what is my secret?', undefined);
	check('missing bearer token ⇒ 401', noToken.status === 401, `status=${noToken.status}`);

	console.log('\n[C] THE LEAK — the id is an opaque string Flue binds to NOTHING');
	console.log('    A one-token developer slip — routing a *billing* conversation to the');
	console.log('    *analytics* instance id — makes product B read product A. Flue has no');
	console.log('    product concept to stop it; the p= segment is just characters.');
	// The dev intends product=billing but reuses the analytics id (typo / dropped axis).
	const leak = await prompt(B, 't=acme;u=bob;p=analytics', '[billing conversation] what is my secret?', 'acme:bob');
	const leaked = replyText(leak).includes(SECRET);
	check('CROSS-PRODUCT LEAK reproduced: billing request reading analytics secret', leaked, replyText(leak));
	console.log('    → In aisotropy, product_id is a field of the unforgeable Scope value object,');
	console.log('      carried in context.Context like tenant/user; the wrong-product read is');
	console.log('      unrepresentable, not one typo away.');

	await stop(s.proc);
	await stop(fake.proc);
	console.log(`\n== isolation smoke ${exitCode() === 0 ? 'PASSED (all assertions held)' : 'FAILED'} ==`);
	console.log('   Verdict: conversation-stream isolation is BY CONSTRUCTION per instance id;');
	console.log('   tenant/user/product isolation and auth are CONVENTION the developer owns.');
	process.exit(exitCode());
}
main().catch((e) => {
	console.error(e);
	process.exit(1);
});
