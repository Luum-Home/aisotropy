package orbit;

/**
 * Multi-PRODUCT isolation — the 4th scope axis (tenant, user, product, session).
 *
 * <p>aisotropy scopes memory by a 4-field {@code Scope} value object; a wrong/forgotten
 * {@code product_id} is unrepresentable. Spring AI has NO product concept — {@code product} is
 * just one more segment the developer stuffs into the flat conversationId string. This demo,
 * run against the real memory plane, reproduces three ways that leaks, all with the SAME
 * tenant+user.
 *
 * <p>Exit 0 = every expected leak reproduced + controls held. Exit 1 otherwise.
 */
public final class ProductIsolationMain {

    static final String X_SECRET = "PRODUCT-X-secret-roadmap-Q3";

    public static void main(String[] args) {
        Orbit orbit = new Orbit();
        boolean ok = true;

        // Same tenant+user (acme/alice) operating two products: X and Y.
        String prodX = Orbit.convId("acme", "alice", "productX", "s1");
        orbit.chat(prodX, "productX private note: " + X_SECRET);

        // --- Positive control: correct full key reads productX's own marker.
        boolean pos = orbit.chat(prodX, "recall").contains(X_SECRET);
        System.out.println("[positive control] exact (acme,alice,productX) reads its own marker: " + pos);
        ok &= pos;

        // --- Honesty control: correctly-composed productY key is exact-distinct -> does NOT see X.
        String prodY = Orbit.convId("acme", "alice", "productY", "s1");
        boolean honesty = !orbit.chat(prodY, "hello from productY").contains(X_SECRET);
        System.out.println("[honesty control] exact (acme,alice,productY) does NOT see productX marker: " + honesty);
        ok &= honesty;

        // --- LEAK 1: wrong-product substitution. A handler "serving productY" that composes the
        // key with the wrong product segment reads productX. Nothing binds product to the caller.
        String wrong = Orbit.convId("acme", "alice", "productX", "s1"); // productY handler, X string
        boolean leak1 = orbit.chat(wrong, "productY handler, mis-passed product segment").contains(X_SECRET);
        System.out.println("[LEAK 1: wrong-product substitution] productY handler using productX string leaks: " + leak1);
        ok &= leak1;

        // --- LEAK 2: org-wide enumeration crosses products too. No product filter exists.
        boolean leak2 = orbit.repository().findConversationIds().contains(prodX)
                && orbit.repository().findByConversationId(prodX).stream()
                        .anyMatch(m -> m.getText().contains(X_SECRET));
        System.out.println("[LEAK 2: org-wide scan] findConversationIds() + raw read crosses to productX: " + leak2);
        ok &= leak2;

        // --- LEAK 3: delimiter-injection collision. The flat composite key is UNESCAPED, so two
        // genuinely different identities map to the SAME string. Here user "alice|productX" with
        // product "s1" collides with user "alice", product "productX", session "s1":
        //   convId("acme","alice","productX","s1")            == "acme|alice|productX|s1"
        //   convId("acme","alice|productX","s1", <dropped>)   -> same prefix collides
        String collide = "acme" + "|" + "alice|productX" + "|" + "s1"; // attacker-shaped identity
        // (acme, user="alice|productX", product="s1") -> "acme|alice|productX|s1" == prodX above.
        boolean leak3 = collide.equals(prodX)
                && orbit.repository().findByConversationId(collide).stream()
                        .anyMatch(m -> m.getText().contains(X_SECRET));
        System.out.println("[LEAK 3: delimiter-injection collision] distinct identity maps to productX's key: " + leak3);
        ok &= leak3;

        System.out.println();
        System.out.println("Product axis: CONVENTION — no product primitive; product is an unescaped, "
                + "unvalidated segment of the opaque conversationId String.");
        System.out.println(ok
                ? "RESULT: exit 0 — controls held, all 3 cross-product leaks reproduced on the real memory plane."
                : "RESULT: exit 1 — a control failed or an expected leak did not reproduce.");
        System.exit(ok ? 0 : 1);
    }
}
