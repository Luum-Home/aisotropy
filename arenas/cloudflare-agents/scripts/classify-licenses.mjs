// Reads `pnpm licenses list --json` from stdin and classifies the tree.
// Exits non-zero if any HARD-BLOCKER license is present.
let d = "";
process.stdin.on("data", (c) => (d += c)).on("end", () => {
  const j = JSON.parse(d);
  // WEAK is tested FIRST so that "LGPL" is not swallowed by the GPL branch of
  // HARD (LGPL/MPL/EPL/CDDL are weak-copyleft = CAUTION, not blockers).
  const WEAK = /^LGPL|^MPL|^EPL|^CDDL/i;
  const HARD = /AGPL|(?<![L])GPL-[23]|SSPL|BUSL|Business Source|Elastic-2|ELv2|Commons-Clause|^FSL/i;
  let total = 0;
  const hard = [];
  const weak = [];
  for (const lic of Object.keys(j)) {
    const pkgs = j[lic];
    total += pkgs.length;
    if (WEAK.test(lic)) weak.push(...pkgs.map((p) => `${lic} :: ${p.name}`));
    else if (HARD.test(lic)) hard.push(...pkgs.map((p) => `${lic} :: ${p.name}`));
  }
  const tally = Object.entries(j)
    .map(([l, p]) => [p.length, l])
    .sort((a, b) => b[0] - a[0]);
  console.log("TOTAL packages:", total);
  for (const [c, l] of tally) console.log(String(c).padStart(4), l);
  console.log("\nHARD BLOCKERS (GPL/AGPL/SSPL/BUSL/ELv2/Commons-Clause/FSL):", hard.length);
  hard.forEach((x) => console.log("  BLOCK", x));
  console.log("WEAK COPYLEFT (LGPL/MPL/EPL/CDDL) = CAUTION, not a blocker:", weak.length);
  weak.forEach((x) => console.log("  CAUTION", x));
  if (hard.length > 0) {
    console.error("\nFAIL: hard-blocker license in the tree");
    process.exit(1);
  }
  console.log("\nPASS: zero hard-blocker licenses.");
});
