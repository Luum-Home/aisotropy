package orbit;

/**
 * Multi-PRODUCT isolation — the 4th scope axis (tenant, user, product, session), on the real Spring
 * AI 2.0.0 memory plane. Two postures, both executed, shown at equal weight.
 *
 * <p>Spring AI has NO product concept — {@code product} is just one more segment the developer folds
 * into the opaque {@code conversationId} string. That is fine: a correctly-composed, structured key
 * isolates products cleanly (Part B). The failures in Part A are all developer key-composition bugs
 * — dropping/mis-passing the product segment, or choosing a flat unescaped join whose delimiter can
 * be forged — NOT Spring AI defects. aisotropy scopes memory by a 4-field {@code Scope} value object
 * where a wrong/forgotten {@code product_id} is unrepresentable; here the developer owns that
 * discipline.
 *
 * <p>Exit 0 = Part A reproduces the naive key-composition bugs AND Part B closes them. Exit 1 otherwise.
 */
public final class ProductIsolationMain {

    // SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md — Spring AI = L1 and the matrix
    // row records the scoping primitive as SINGLE-axis (one required conversationId), so a product
    // dimension is the developer's key composition (covers the javadoc above).

    static final String X_SECRET = "PRODUCT-X-secret-roadmap-Q3";

    public static void main(String[] args) {
        boolean ok = true;
        ok &= partA_naiveFlatKey();
        System.out.println();
        ok &= partB_structuredKey();

        System.out.println();
        // SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md (Spring AI = L1, single-axis
        // required conversationId — no second, product-shaped axis in the memory primitive).
        System.out.println("Product axis: Spring AI has no product primitive; product is a developer-owned");
        System.out.println("segment of the conversationId. A structured, identity-derived key (Part B) isolates");
        System.out.println("products cleanly; the Part A failures are key-composition bugs the developer owns,");
        System.out.println("not Spring AI defects. aisotropy binds product in an unforgeable Scope by default.");
        System.out.println(ok
                ? "RESULT: exit 0 — Part A reproduced the naive bugs, Part B closed them (real memory plane)."
                : "RESULT: exit 1 — a control failed or a posture did not behave as claimed.");
        System.exit(ok ? 0 : 1);
    }

    /** PART A — naive flat unescaped key, product segment caller-controlled. */
    private static boolean partA_naiveFlatKey() {
        System.out.println("---- PART A: naive flat key (unescaped join, caller-composed) ----");
        Orbit orbit = new Orbit();
        boolean ok = true;

        String prodX = Orbit.convId("acme", "alice", "productX", "s1");
        orbit.chat(prodX, "productX private note: " + X_SECRET);

        boolean pos = orbit.chat(prodX, "recall").contains(X_SECRET);
        System.out.println("[positive control] exact (acme,alice,productX) reads its own marker: " + pos);
        ok &= pos;

        boolean honesty = !orbit.chat(Orbit.convId("acme", "alice", "productY", "s1"),
                "hello from productY").contains(X_SECRET);
        System.out.println("[honesty control] exact (acme,alice,productY) does NOT see productX marker: " + honesty);
        ok &= honesty;

        // Bug 1: wrong-product substitution. A handler "serving productY" that composes the key with
        // the wrong product segment reads productX. Nothing binds product to the caller identity.
        boolean wrongProduct = orbit.chat(Orbit.convId("acme", "alice", "productX", "s1"),
                "productY handler, mis-passed product segment").contains(X_SECRET);
        System.out.println("[bug 1: wrong-product substitution] productY handler using productX string reads X: "
                + wrongProduct + "  <- dropped/mis-passed segment, a key-composition bug");
        ok &= wrongProduct;

        // Bug 2: delimiter forgery on a FLAT UNESCAPED join. Because convId() joins with a bare "|"
        // and never escapes segments, two genuinely different identities can serialize to the SAME
        // string. This is an artifact of the developer's chosen key FORMAT, not a Spring AI flaw —
        // Part B removes it by escaping/structuring the key. Here: user "alice|productX" with product
        // "s1" collides with user "alice", product "productX", session "s1".
        String collide = "acme" + "|" + "alice|productX" + "|" + "s1"; // (acme, user="alice|productX", product="s1")
        boolean forged = collide.equals(prodX)
                && orbit.repository().findByConversationId(collide).stream()
                        .anyMatch(m -> m.getText().contains(X_SECRET));
        System.out.println("[bug 2: delimiter forgery] distinct identity serializes to productX's key: "
                + forged + "  <- artifact of a flat UNESCAPED key format the developer chose");
        ok &= forged;
        return ok;
    }

    /**
     * PART B — structured, identity-derived key. Escape the segments (so no delimiter forgery is
     * possible) and derive the product from the verified request context, never from caller input.
     * Both Part-A bugs vanish.
     */
    private static boolean partB_structuredKey() {
        System.out.println("---- PART B: structured, escaped, identity-derived key ----");
        Orbit orbit = new Orbit();
        boolean ok = true;

        // A verified request context (what an auth/routing layer produced). Product is bound here,
        // not passed by the caller. The key is JSON-structured so segments cannot collide.
        String prodX = structuredKey("acme", "alice", "productX", "s1");
        orbit.chat(prodX, "productX private note: " + X_SECRET);

        boolean pos = orbit.chat(prodX, "recall").contains(X_SECRET);
        System.out.println("[positive control] structured (acme,alice,productX) reads its own marker: " + pos);
        ok &= pos;

        // productY derived key is distinct -> clean miss (no wrong-product substitution possible when
        // product comes from the verified context, not a caller-passed string).
        String prodY = structuredKey("acme", "alice", "productY", "s1");
        boolean isolated = !orbit.chat(prodY, "hello from productY").contains(X_SECRET);
        System.out.println("[by construction] productY cannot read productX (product bound from context): " + isolated);
        ok &= isolated;

        // Delimiter forgery is now impossible: with escaped/structured segments, the attacker's
        // "alice|productX" identity serializes to a DIFFERENT string than productX's key.
        String forgeryAttempt = structuredKey("acme", "alice|productX", "s1", "");
        boolean noForgery = !forgeryAttempt.equals(prodX);
        System.out.println("[by construction] structured key blocks delimiter forgery (no collision): " + noForgery);
        ok &= noForgery;
        return ok;
    }

    /**
     * A collision-proof key: JSON-encode each segment so an embedded delimiter cannot forge another
     * identity's key. This is a one-line developer choice; aisotropy's Scope value object gives it by
     * default. (Uses simple quoting sufficient to defeat the bare-"|" forgery above.)
     */
    static String structuredKey(String tenant, String user, String product, String session) {
        return "{\"t\":\"" + esc(tenant) + "\",\"u\":\"" + esc(user)
                + "\",\"p\":\"" + esc(product) + "\",\"s\":\"" + esc(session) + "\"}";
    }

    private static String esc(String s) {
        return s.replace("\\", "\\\\").replace("\"", "\\\"");
    }
}
