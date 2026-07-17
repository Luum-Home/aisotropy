// Orbit's ONLY external scoping axis on Flue is the single agent-instance `id`
// (the `<id>` path segment in `POST /agents/orbit/<id>`). Flue deliberately binds
// no tenant/user/product concept of its own — "It's up to the developer to decide
// what `id` means" (docs: guide/building-agents). So Orbit folds all four aisotropy
// scope axes into ONE string and parses them back out here.
//
// The isolation that IS by-construction: two distinct ids get two distinct,
// isolated canonical conversation streams — Flue never crosses them. What is
// CONVENTION is the *binding* between a caller's verified identity and the id they
// reach: nothing in the framework composes the id for you or checks the segments.
// This module holds both halves of that convention, side by side:
//   - `deriveInstanceId` — the STRONG path: the server composes the id from an
//     already-verified identity (incl. product), so the caller never names a raw
//     id. Mirrors aisotropy composing a Scope from context.Context.
//   - `parseInstanceId` — the reverse, used by the route guard to authorize the
//     caller against the id they asked for.
// The naive path is to skip `deriveInstanceId` and let the client name the opaque
// string directly; then a dropped/typo'd segment routes to the wrong stream. That
// is a wrong-key developer bug (true of any id->instance map, incl. aisotropy's own
// BadgerDB if you compose the wrong Scope key), not Flue returning the wrong thing.

export interface OrbitScope {
	tenant: string;
	user: string;
	product: string;
}

/** Encode a scope into the single instance id Flue addresses. */
export function encodeInstanceId(s: OrbitScope): string {
	return `t=${s.tenant};u=${s.user};p=${s.product}`;
}

/**
 * STRONG-config primitive: derive the instance id from an ALREADY-VERIFIED
 * identity. In a real app the (tenant,user) come from the authenticated principal
 * and `product` from the request's verified context — never from a client-chosen
 * path segment. Composing the id this way makes the cross-product slip in the
 * naive path structurally unreachable: a billing request derives a `p=billing`
 * id and can only ever touch the billing stream. This is the TS analog of
 * aisotropy building an unforgeable Scope from context.Context.
 */
export function deriveInstanceId(
	principal: { tenant: string; user: string },
	product: string,
): string {
	return encodeInstanceId({ tenant: principal.tenant, user: principal.user, product });
}

/** Parse the instance id back into a scope. Best-effort — the string is opaque. */
export function parseInstanceId(id: string): Partial<OrbitScope> {
	const out: Partial<OrbitScope> = {};
	for (const part of id.split(';')) {
		const [k, v] = part.split('=');
		if (k === 't') out.tenant = v;
		if (k === 'u') out.user = v;
		if (k === 'p') out.product = v;
	}
	return out;
}
