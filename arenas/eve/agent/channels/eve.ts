import { eveChannel } from "eve/channels/eve";
import { localDev, none } from "eve/channels/auth";

// The channel is eve's front door and the ONLY place identity is verified.
// For this offline arena we run the HTTP surface open on localhost:
//   * localDev() admits `eve dev` / local requests
//   * none()     admits the smoke's plain curl/fetch (public demo posture)
//
// NOTE for the isolation story: even a real verifier here (vercelOidc(),
// httpBasic(), oidc()) authenticates the *caller* and gates the *route* — it
// does not attach a tenant_id/user_id/product_id to the durable memory layer.
// Scoping memory by those axes remains the developer's job (see tools/orbit_memory.ts).
export default eveChannel({
  auth: [localDev(), none()],
});
