// DOCKER smoke driver — drives the CONTAINERIZED Orbit-on-Flue server over HTTP.
//
// Same assertions as the native isolation smoke (scripts/isolation.mjs §[A]/[B]),
// but the server under test is a Docker container (built + booted by run.sh),
// reached only over its published HTTP port. This process manages NO subprocess:
// it reuses the arena's own HTTP helpers (prompt/replyText/check) so the Docker
// lane exercises the exact same wire path the native lane does.
//
// Shape: write a secret as ONE instance, then assert cross-instance and
// cross-product ISOLATION (wrong tenant / user / product never see it), plus the
// developer-owned auth boundary (route guard → 401). The deterministic fake model
// (running inside the container) echoes any ORBIT-SECRET-* it is shown, so a leak
// would be directly observable in the reply text — it cannot silently pass.
import { prompt, replyText, check, exitCode } from '../scripts/lib.mjs';

const BASE = process.argv[2] || 'http://127.0.0.1:18080';
const SECRET = 'ORBIT-SECRET-A';

async function main() {
	console.log(`== Flue Orbit DOCKER smoke (isolation over HTTP against container ${BASE}) ==\n`);

	// Seed the secret into acme/bob/analytics (one instance).
	const seed = await prompt(BASE, 't=acme;u=bob;p=analytics', `secret is ${SECRET}`, 'acme:bob');
	check('containerized turn accepted (200)', seed.status === 200, `status=${seed.status}`);

	console.log('[A] positive control + by-construction isolation (distinct id ⇒ distinct stream)');
	const own = await prompt(BASE, 't=acme;u=bob;p=analytics', 'what is my secret?', 'acme:bob');
	check('same instance id recalls its own secret (durable file-backed stream in container)',
		replyText(own).includes(SECRET), replyText(own));

	const otherTenant = await prompt(BASE, 't=globex;u=bob;p=analytics', 'what is my secret?', 'globex:bob');
	check('WRONG TENANT cannot read it (separate canonical stream)',
		!replyText(otherTenant).includes(SECRET), replyText(otherTenant));

	const otherUser = await prompt(BASE, 't=acme;u=alice;p=analytics', 'what is my secret?', 'acme:alice');
	check('WRONG USER cannot read it', !replyText(otherUser).includes(SECRET), replyText(otherUser));

	const otherProduct = await prompt(BASE, 't=acme;u=bob;p=billing', 'what is my secret?', 'acme:bob');
	check('WRONG PRODUCT cannot read it (cross-product isolation when product is folded into the id)',
		!replyText(otherProduct).includes(SECRET), replyText(otherProduct));

	console.log('\n[B] the authorization boundary is 100% developer-written (route guard)');
	const badToken = await prompt(BASE, 't=acme;u=bob;p=analytics', 'what is my secret?', 'globex:bob');
	check('mismatched bearer token ⇒ 401', badToken.status === 401, `status=${badToken.status}`);
	const noToken = await prompt(BASE, 't=acme;u=bob;p=analytics', 'what is my secret?', undefined);
	check('missing bearer token ⇒ 401', noToken.status === 401, `status=${noToken.status}`);

	const code = exitCode();
	console.log(`\n== DOCKER smoke ${code === 0 ? 'PASSED (all assertions held against the container)' : 'FAILED'} ==`);
	process.exit(code);
}
main().catch((e) => {
	console.error(e);
	process.exit(1);
});
