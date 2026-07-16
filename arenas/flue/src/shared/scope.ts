// Orbit's ONLY isolation axis on Flue is the single agent-instance `id`
// (the `<id>` path segment in `POST /agents/orbit/<id>`). Flue binds NO
// tenant/user/product concept of its own — "It's up to the developer to decide
// what `id` means" (docs: guide/building-agents §"Agent ID"). So Orbit folds all
// four aisotropy scope axes into ONE opaque string and parses them back out here.
//
// This is CONVENTION, not construction: nothing in the framework validates the
// shape, checks the segments, or stops a caller reaching another instance's `id`.
// The route guard in app.ts is the only thing standing between a caller and any
// instance they can name. Drop a segment (e.g. forget `p=`) and two products
// collapse onto one canonical conversation stream — the multi-product leak.

export interface OrbitScope {
	tenant: string;
	user: string;
	product: string;
}

/** Encode a scope into the single instance id Flue addresses. */
export function encodeInstanceId(s: OrbitScope): string {
	return `t=${s.tenant};u=${s.user};p=${s.product}`;
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
