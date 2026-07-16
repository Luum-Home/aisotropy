// Durable execution on Node: a file-backed SQLite adapter makes each agent
// instance's canonical conversation stream survive a full process restart.
// The isolation smoke exploits exactly this: turn 1 seeds a secret in one
// process, the process exits, and a SEPARATE process recalls it from the durable
// stream — proving durable, crash-safe CONVERSATION memory (not memory tiers).
//
// ORBIT_DB_PATH lets the smoke point at a temp file; default is in-memory
// (equivalent to omitting db.ts — lost on exit).
import { sqlite } from '@flue/runtime/node';

export default sqlite(process.env.ORBIT_DB_PATH || ':memory:');
