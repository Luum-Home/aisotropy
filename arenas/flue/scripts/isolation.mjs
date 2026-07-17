// Orbit ISOLATION smoke on Flue — the crux. Runs through the REAL Flue runtime,
// fully offline. Shows exactly what Flue isolates by construction and what it
// leaves to developer convention, with the naive and the strong paths at EQUAL
// weight.
//
// Flue's ONLY external scoping axis is the agent-instance `id` (one path
// segment). The canonical conversation stream is isolated per-id BY CONSTRUCTION:
// two distinct ids never cross. But (tenant,user,product) are not framework
// concepts — Orbit folds them into the one id string, and the `route` guard binds
// the caller's identity to that id. So the *binding* between a verified identity
// and the id reached is convention. Section [C] shows the naive path (client
// names the opaque id; a wrong segment routes to the wrong stream — a wrong-key
// developer bug, true of any id->instance map). Section [D] shows the strong path
// Flue's own docs teach (server derives the id from verified identity incl.
// product), where that slip is structurally unreachable.
import { rm, mkdir } from 'node:fs/promises';
import { startFakeModel, startServer, stop, prompt, replyText, check, exitCode } from './lib.mjs';

const DB = './data/iso.db';
const SECRET = 'ORBIT-SECRET-A';

// Strong-config primitive (mirrors src/shared/scope.ts `deriveInstanceId`): the
// server composes the id from an ALREADY-VERIFIED principal + the request's
// verified product — the caller never names a raw id. This is the TS analog of
// aisotropy building an unforgeable Scope from context.Context.
const deriveId = (principal, product) => `t=${principal.tenant};u=${principal.user};p=${product}`;

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

	console.log('\n[C] NAIVE path — the client names the opaque id by hand');
	console.log('    The dev means product=billing but reuses the analytics id (typo / dropped');
	console.log('    axis). Flue faithfully routes to the id it was handed and returns that');
	console.log('    stream. This is a wrong-key developer bug — the same class as querying any');
	console.log('    id->instance map (incl. aisotropy\'s own BadgerDB) with the wrong key — not');
	console.log('    Flue returning the wrong thing. The p= segment is convention, not a scope.');
	const naive = await prompt(B, 't=acme;u=bob;p=analytics', '[billing conversation] what is my secret?', 'acme:bob');
	check('naive: a hand-named wrong id reaches the analytics stream (dev bug, not framework leak)',
		replyText(naive).includes(SECRET), replyText(naive));

	console.log('\n[D] STRONG path — the SERVER derives the id from verified identity (Flue\'s own');
	console.log('    doc pattern: guide/building-agents "verify the caller can access the id").');
	console.log('    The billing request derives p=billing from its verified product; it can only');
	console.log('    ever touch the billing stream. The cross-product read is now unreachable.');
	// Verified principal acme:bob; the request is FOR product=billing, so the app
	// derives the id instead of letting the caller name it.
	const principal = { tenant: 'acme', user: 'bob' };
	const billingId = deriveId(principal, 'billing');
	const strong = await prompt(B, billingId, '[billing conversation] what is my secret?', 'acme:bob');
	check('strong: server-derived billing id sees the billing stream, NOT the analytics secret',
		!replyText(strong).includes(SECRET), `id=${billingId} :: ${replyText(strong)}`);
	console.log('    → Same discipline aisotropy enforces by construction: product_id is a field');
	console.log('      of an unforgeable Scope on context.Context, so the naive [C] path is');
	console.log('      unrepresentable, not one convention away.');

	await stop(s.proc);
	await stop(fake.proc);
	console.log(`\n== isolation smoke ${exitCode() === 0 ? 'PASSED (all assertions held)' : 'FAILED'} ==`);
	console.log('   Verdict: conversation-stream isolation is BY CONSTRUCTION per instance id;');
	console.log('   the identity->id binding (id composition + route guard) is CONVENTION the');
	console.log('   developer owns — Flue authenticates nothing by default but documents the seam.');
	process.exit(exitCode());
}
main().catch((e) => {
	console.error(e);
	process.exit(1);
});
