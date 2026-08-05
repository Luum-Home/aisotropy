package orbit;

import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Isolation smoke — tenant/user axis, on the real Spring AI 2.0.0 memory plane. Two postures, both
 * executed, shown at EQUAL weight.
 *
 * <p><b>Part A — the naive-convention path.</b> Spring AI keys chat memory by a single opaque
 * {@code conversationId} String with no framework-enforced principal binding. If the developer lets
 * a caller supply or reuse that string directly, the wrong caller reads the wrong memory. This is
 * the same under-authorized-key property ANY keyed store has when no identity layer sits above it —
 * aisotropy's own BadgerDB/rqlite/Qdrant included. It is a developer bug, not a Spring AI defect.
 *
 * <p><b>Part B — the correct identity-derived path.</b> A competent dev derives the
 * {@code conversationId} from the VERIFIED principal (never from caller input) and reads through a
 * facade that recomputes the key server-side. A wrong/forged key is then unrepresentable: the
 * cross-tenant read is a clean miss by construction. This is plain application code — the same
 * boundary aisotropy ships by default as an unforgeable Scope.
 *
 * <p>SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md — Spring AI graded L1 (required,
 * throwing, single-axis {@code conversationId}); L1 means "partitions if passed correctly", so the
 * principal binding is the developer's layer. Covers both paragraphs of this javadoc.
 *
 * <p>The honest, narrower point that remains true: Spring AI's {@code conversationId} is a
 * developer-owned string with no framework-enforced identity binding — isolation here is a
 * convention the developer must construct, not a default the framework guarantees.
 *
 * <p>Exit 0 = harness healthy AND both postures behaved as claimed (Part A reproduces the naive
 * cross-read; Part B closes it). Exit 1 otherwise.
 */
public final class SmokeMain {

    static final String SECRET = "ORBIT-SECRET-acme-42-refund-approved";

    public static void main(String[] args) {
        boolean ok = true;
        ok &= partA_naiveConvention();
        System.out.println();
        ok &= partB_identityDerived();

        System.out.println();
        System.out.println("Isolation model: Spring AI's conversationId is a developer-owned string with no");
        System.out.println("framework-enforced identity binding. Naive convention (Part A) lets a wrong key");
        System.out.println("cross-read; identity-derived construction (Part B) makes it unrepresentable.");
        System.out.println("Both are application-boundary properties — aisotropy ships Part B by default.");
        System.out.println(ok
                ? "RESULT: exit 0 — harness healthy, both postures behaved as claimed (real memory plane)."
                : "RESULT: exit 1 — harness broken or a posture did not behave as claimed.");
        System.exit(ok ? 0 : 1);
    }

    /** PART A — naive convention: caller-supplied/reused conversationId, no identity layer. */
    private static boolean partA_naiveConvention() {
        System.out.println("---- PART A: naive convention (caller-controlled conversationId) ----");
        Orbit orbit = new Orbit();
        boolean ok = true;

        String acmeKey = Orbit.convId("acme", "alice", "billing", "s1");
        orbit.chat(acmeKey, "Please note our refund policy secret: " + SECRET);

        // Positive control: same key re-reads its own conversation.
        boolean posOk = orbit.chat(acmeKey, "what did I just tell you?").contains(SECRET);
        System.out.println("[positive control] acme/alice re-reads own convo -> secret present: " + posOk);
        ok &= posOk;

        // Naive cross-read: a different tenant that the dev accidentally maps to the SAME string
        // reads acme's memory. The key binds to no principal, so nothing rejects it.
        boolean crossRead = orbit.chat(acmeKey, "globex caller reusing acme's conversationId").contains(SECRET);
        System.out.println("[naive cross-read] globex caller on acme's key sees secret: " + crossRead
                + "  <- developer bug: any keyed store leaks under an unauthorized key");
        ok &= crossRead;

        // The admin/repo enumeration API is NOT the request path. findConversationIds() is a
        // maintenance/admin surface (like a DB "list all rows"); a request handler should never
        // call it with a foreign id. Shown for completeness, labelled as the admin surface.
        List<String> allIds = orbit.repository().findConversationIds();
        System.out.println("[admin API, not request path] repository.findConversationIds() lists "
                + allIds.size() + " id(s) — an admin/maintenance surface, not a per-request read");

        // Honesty control: a DIFFERENT, correctly-composed key has its own empty memory.
        boolean honesty = !orbit.chat(Orbit.convId("globex", "alice", "billing", "s1"),
                "correctly-scoped globex session").contains(SECRET);
        System.out.println("[honesty control] a correctly-composed globex key does NOT see acme secret: " + honesty);
        ok &= honesty;
        return ok;
    }

    /**
     * PART B — correct construction: conversationId derived from the VERIFIED principal, read through
     * a facade that recomputes the key server-side. Caller input never becomes the key.
     */
    private static boolean partB_identityDerived() {
        System.out.println("---- PART B: identity-derived conversationId (correct construction) ----");
        Orbit orbit = new Orbit();
        IdentityBoundMemory bound = new IdentityBoundMemory(orbit);
        boolean ok = true;

        // Two authenticated principals. The dev NEVER lets either supply a conversationId; it is
        // derived from the verified identity the auth layer produced.
        Principal acme = new Principal("acme", "alice", "billing", "s1");
        Principal globex = new Principal("globex", "alice", "billing", "s1");

        bound.write(acme, "refund policy secret: " + SECRET);

        boolean pos = bound.read(acme, "recall").contains(SECRET);
        System.out.println("[positive control] acme principal reads its own derived-key memory: " + pos);
        ok &= pos;

        // globex is a DIFFERENT verified principal -> derives a different key -> clean miss.
        boolean isolated = !bound.read(globex, "what do you have?").contains(SECRET);
        System.out.println("[by construction] globex principal cannot reach acme's memory (derived key differs): " + isolated);
        ok &= isolated;

        // An attacker who tries to smuggle acme's raw key is rejected: the facade recomputes the key
        // from the caller's OWN verified identity and refuses a mismatched foreign id.
        boolean rejected;
        try {
            bound.readRaw(globex, Orbit.convId("acme", "alice", "billing", "s1"));
            rejected = false;
        } catch (SecurityException e) {
            rejected = true;
        }
        System.out.println("[by construction] a smuggled foreign conversationId is refused server-side: " + rejected);
        ok &= rejected;
        return ok;
    }

    /** A verified principal produced by an auth layer (JWT/Spring Security) — not caller input. */
    record Principal(String tenant, String user, String product, String session) {
        String conversationId() {
            return Orbit.convId(tenant, user, product, session);
        }
    }

    /**
     * The identity-binding facade — the {@code @auth}-style layer Spring AI leaves to the developer.
     * It derives the conversationId from the caller's verified {@link Principal} and refuses any
     * caller-supplied id that does not belong to that principal. Plain app code over the OSS memory.
     */
    static final class IdentityBoundMemory {
        private final Orbit orbit;
        private final Map<String, Boolean> owned = new ConcurrentHashMap<>();

        IdentityBoundMemory(Orbit orbit) { this.orbit = orbit; }

        String write(Principal p, String text) {
            String id = p.conversationId();
            owned.put(id, Boolean.TRUE);
            return orbit.chat(id, text);
        }

        /** Key is derived from the principal — the caller never names it. */
        String read(Principal p, String text) {
            return orbit.chat(p.conversationId(), text);
        }

        /** Guards a raw-id read: the id must equal the caller-principal's own derived key. */
        String readRaw(Principal p, String requestedId) {
            if (!p.conversationId().equals(requestedId)) {
                throw new SecurityException("conversationId " + requestedId
                        + " does not belong to principal " + p);
            }
            return orbit.chat(requestedId, "authorized raw read");
        }
    }
}
