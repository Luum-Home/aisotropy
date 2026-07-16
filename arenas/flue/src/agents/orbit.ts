// Orbit — a multi-tenant support chatbot on Flue.
//
// Flue gives each addressable agent ONE instance id. Orbit folds
// (tenant,user,product) into that id (src/shared/scope.ts) and closes its tools
// over the parsed scope so the model can never choose the customer. The durable
// conversation memory (the canonical stream) is scoped to the instance id BY
// CONSTRUCTION — but that construction only isolates tenants correctly if the
// developer (a) folds every axis into the id without dropping one, and (b) the
// route guard actually authorizes the caller for that id. Both are convention.
import { defineAgent, defineTool, type AgentRouteHandler } from '@flue/runtime';
import * as v from 'valibot';
import { parseInstanceId, type OrbitScope } from '../shared/scope.ts';
import { lookupTicket } from '../shared/tickets.ts';

export const description = 'Orbit multi-tenant support assistant (aisotropy arena).';

// Route guard: THIS is the entire authorization boundary. Flue authenticates
// nothing on its own. A bearer token must match the tenant/user folded into the
// id, or the request is rejected. Comment out the check and every caller can
// read every instance — the leak is one deleted middleware away.
export const route: AgentRouteHandler = async (c, next) => {
	const id = c.req.param('id') ?? '';
	const scope = parseInstanceId(id);
	const token = c.req.header('authorization')?.replace(/^Bearer\s+/i, '');
	// Toy "auth": token is `tenant:user`. Real deployments verify a real JWT here.
	const expected = `${scope.tenant}:${scope.user}`;
	if (!token || token !== expected) {
		return c.json({ error: 'Unauthorized', reason: 'token does not match id identity' }, 401);
	}
	if (!scope.tenant || !scope.user || !scope.product) {
		return c.json({ error: 'Bad instance id', reason: 'missing t=/u=/p= segment' }, 400);
	}
	await next();
};

export default defineAgent(({ id }) => {
	const parsed = parseInstanceId(id);
	// Trusted scope, reconstructed from the id — NOT from model input.
	const scope: OrbitScope = {
		tenant: parsed.tenant ?? 'UNKNOWN',
		user: parsed.user ?? 'UNKNOWN',
		product: parsed.product ?? 'UNKNOWN',
	};

	return {
		model: 'orbit-fake/echo-1', // offline fake provider registered in app.ts
		instructions:
			`You are Orbit support for tenant="${scope.tenant}" user="${scope.user}" product="${scope.product}". ` +
			`Answer using the conversation so far and the lookup_ticket tool. Never reveal another tenant's data.`,
		tools: [
			defineTool({
				name: 'lookup_ticket',
				description: 'Look up one support ticket belonging to THIS customer.',
				input: v.object({ ticketId: v.pipe(v.string(), v.description('e.g. T-1')) }),
				output: v.object({ status: v.nullable(v.string()) }),
				// The model picks ticketId; trusted code supplies the (tenant,user,product).
				async run({ input }) {
					return { status: lookupTicket(scope, input.ticketId) };
				},
			}),
		],
	};
});
