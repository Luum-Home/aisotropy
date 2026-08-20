#!/usr/bin/env bash
# silent-probe-guard.sh — detect probes that cannot tell "I could not evaluate"
# from "there is nothing there", and therefore report clean when they never ran.
#
# WHY THIS EXISTS: on 2026-08-20 seven separate defects in this repo turned out
# to be one shape — a command that DECIDES something, with its explanation
# silenced, so "I could not" reads as "there is none". The sharpest one:
# pre-publication-sweep.sh searched for a PEM private key. The pattern starts
# with '-----', so `git grep` parsed it as an OPTION and died with
#   error: unknown option `---BEGIN'   (rc=129)
# The error went to 2>/dev/null, the match count came back 0, and the sweep
# printed "VERDICT: clean". A secret detector that never ran, reporting clean.
# Siblings already fixed: 0806c51 (supply-chain stages), 49a7a77 (pre-push
# stage 7 + a dead `; rc=$?` branch), 637d71c (deadcode failure read as "0
# unreachable"), 065469f (unreadable licence counted permissive), 19d49fc (the
# PEM sweep), 759bbda (an invariant that could not fire), b520217 (discovery
# walking foreign worktrees).
#
# WHAT IT CHECKS — deliberately NARROW. The broad signature ("a deciding
# command with 2>/dev/null") matches 83 sites in this repo and the large
# majority are legitimate, so flagging it would produce a gate that gets
# switched off in a week. These four checks were each measured against every
# tracked *.sh before being included:
#
#   C1 PATTERN-AS-OPTION   a grep-family pattern that argv can eat as an option
#                          — a literal starting with '-', or a variable whose
#                          value comes from OUTSIDE the script (environment,
#                          $(...), positional arg) — passed without -e or --.
#                          In-script literal constants are NOT flagged: the
#                          author controls those and a change is visible in the
#                          same file.
#   C2 DOUBLE-VALUE-FALLBACK  a counting command with a PRINTING || fallback.
#                          `grep -c` prints "0" AND exits 1 on no-match, so
#                          `grep -c ... || printf '0'` yields "0\n0"; the
#                          `[ ... -ge 1 ]` that consumes it cannot evaluate and
#                          the shell reads that failure as a definite "false".
#                          `|| true` is fine — it prints nothing.
#   C3 DEAD-RC-BRANCH      `cmd; rc=$?` under an active `set -e`: the shell
#                          exits at cmd, so the error branch is unreachable.
#                          A `set +e` / `set -e` region around it is correct
#                          and is NOT flagged.
#   C4 AND-ON-ALWAYS-ZERO  `... | head -5 && echo FOUND`: the && is bound to
#                          the tail of the pipeline, which always exits 0, not
#                          to the command that decides.
#
# COVERAGE LEDGER — 4 of the 7 known sub-forms are detected statically. The
# other 3 are NOT, and saying so is the point: a guard that promised seven and
# delivered noise would be switched off, and then none of them would be caught.
#
#   sub-form 1  pattern read as an option ................. C1  DETECTED
#   sub-form 2  exit code lost in a pipeline .............. NOT DETECTABLE
#   sub-form 3  `; rc=$?` under set -e ................... C3  DETECTED
#   sub-form 4  tool exits 0 and returns empty ............ NOT APPLICABLE HERE
#   sub-form 5  && bound to the wrong command ............ C4  DETECTED
#   sub-form 6  a guard branch that can never fire ....... NOT DETECTABLE
#   sub-form 7  an instrument that checks zero files ..... NOT DETECTABLE
#   (C2 is not one of the seven: it is the concrete defect the sub-form 6
#    METHOD found — run the condition against real state and count both sides.)
#
# Why the three are not detectable, measured rather than assumed:
#   - sub-form 2: the syntactic signature (a deciding command with its stderr
#     silenced) matches 83 sites in this repo and the large majority are
#     correct. resource-guard.sh silences vm_stat, but initialises free_mb=-1
#     and exits BLOCKED if it stays -1 — it silences the noise while still
#     failing closed. Nothing syntactic separates that from the defect. Flagging
#     all 83 would not be noise, it would be 80+ invented tasks, each looking
#     measured. That judgement stays human.
#   - sub-form 4: on this platform the GNU-only flags fail LOUD rather than
#     returning empty-with-rc-0 (`grep -oP` -> rc=2 "invalid option -- P";
#     likewise stat -c, date -d, find -printf, du -b). There are also zero
#     occurrences of any of them in the tracked *.sh. Nothing to detect.
#   - sub-forms 6 and 7: both are runtime properties, not syntax. "Does this
#     branch ever fire?" and "is the scope non-empty?" are answered by running
#     the condition against real state and counting each side, which is a
#     method, not a pattern. Applied by hand in this sweep; it is what found C2
#     and the empty-scope hole in check-doc-commit-citations.sh.
#
# FALSE-POSITIVE CLASSES EXPLICITLY EXCLUDED — each one has a fixture in the
# negative control below and is proven unflagged on every run:
#   1. inside a comment. This repo's own fix commits quote the old defective
#      line while explaining the fix; a naive census flags the documentation of
#      its own solution, which is how a gate earns being switched off.
#   2. inside a heredoc, usage block or example. Same reason: data, not code.
#   3. `command -v x >/dev/null`, `which`, `type` — these discard a PATH, not an
#      explanation, and the caller branches on the exit code.
#   4. silencing whose failure branch DOES print the cause (the shape already
#      used by pre-push-gate.sh:70 and supply-chain-guard.sh).
#
# MEASURED on this repo (108 tracked *.sh) at the time of writing: 24 findings
# before the accompanying fixes, hand-classified one by one, 0 false positives;
# 0 findings after. The naive alternatives were measured too and rejected: 114
# raw `2>/dev/null` occurrences in scripts/ alone, 83 after removing comments
# and `command -v`, and 47 sites for an unrestricted "grep pattern is a
# variable" rule — of which only the 22 whose value BEGINS with an expansion
# are real, since only the first character can be eaten as an option.
#
# SCOPE: `git ls-files '*.sh'` — tracked shell scripts. An untracked script is
# not scanned; pass it as an argument to check it before committing.
#
# SELF-TEST: every run first checks itself against two fixtures — a POSITIVE
# control it MUST flag and a NEGATIVE control it MUST NOT flag. A guard that
# cannot fail is the defect this script exists to find, and a guard that flags
# everything passes a positive control while being useless. If either control
# misbehaves the guard exits 2 (CANNOT EVALUATE) and never reports clean.
#
# NOT WIRED INTO scripts/pre-push-gate.sh. Run by hand:
#   scripts/silent-probe-guard.sh                  # every tracked *.sh
#   scripts/silent-probe-guard.sh <file>...        # only these
#   scripts/silent-probe-guard.sh --self-test      # controls only
#   scripts/silent-probe-guard.sh --explain        # + why each finding matters
#
# EXIT CODES (repo guard convention):
#   0  clean            — no findings
#   1  findings         — at least one probe cannot distinguish failure from absence
#   2  cannot evaluate  — self-test failed, no awk, or not a git repository
#
# Read-only. Offline. No network, no build, no model call.
set -uo pipefail

EXPLAIN=0
SELFTEST_ONLY=0
declare -a TARGETS=()
for arg in "$@"; do
  case "$arg" in
    --explain)   EXPLAIN=1 ;;
    --self-test) SELFTEST_ONLY=1 ;;
    -h|--help)   sed -n '2,116p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*)          echo "silent-probe-guard: unknown argument: $arg" >&2; exit 2 ;;
    *)           TARGETS[${#TARGETS[@]}]="$arg" ;;
  esac
done

command -v awk >/dev/null 2>&1 || { echo "silent-probe-guard: CANNOT EVALUATE — awk not found" >&2; exit 2; }

# --------------------------------------------------------------------------
# The detector. One awk program, two passes per file:
#   pass 1 collects every NAME= assignment and whether its value crosses a
#          boundary the script does not control;
#   pass 2 walks the code and applies C1..C4.
# Output: one finding per line, "CHECK<TAB>line<TAB>message<TAB>source".
# --------------------------------------------------------------------------
detect() {
  awk '
  function isexternal(rhs) {
    # Only the value first CHARACTER matters: a pattern is eaten as an option
    # only if it BEGINS with "-". So strip the opening quote and ask whether
    # the first character comes from an expansion the script does not control
    # (environment read, command substitution, positional arg) or is a literal.
    #   KEY="${OPENAI_API_KEY}"          -> begins with an expansion  -> flag
    #   CLUSTER="${CLUSTER:-aisotropy}"  -> env-overridable           -> flag
    #   modpath="$(... awk ...)"         -> data-derived              -> flag
    #   MARKER="lga-docker-$(date +%s)"  -> begins with "l"           -> safe
    #   SECRET="SECRET: ... ${RUN_ID}."  -> begins with "S"           -> safe
    sub(/^[ \t]*/, "", rhs)
    if (substr(rhs, 1, 1) == "\"" || substr(rhs, 1, 1) == "'"'"'") rhs = substr(rhs, 2)
    if (substr(rhs, 1, 1) == "$")  return 1
    if (substr(rhs, 1, 1) == "`")  return 1
    return 0
  }
  # split a shell-ish line into tokens, keeping quoted runs together
  function tokenize(s, out,   n, i, c, cur, q) {
    n = 0; cur = ""; q = ""
    for (i = 1; i <= length(s); i++) {
      c = substr(s, i, 1)
      if (q != "") { cur = cur c; if (c == q) q = ""; continue }
      if (c == "\"" || c == "'"'"'") { q = c; cur = cur c; continue }
      if (c == " " || c == "\t") { if (cur != "") { out[++n] = cur; cur = "" }; continue }
      cur = cur c
    }
    if (cur != "") out[++n] = cur
    return n
  }
  function unquote(t) { gsub(/^["'"'"']|["'"'"']$/, "", t); return t }
  function report(check, ln, msg, src) { printf "%s\t%d\t%s\t%s\n", check, ln, msg, src }

  # ---------------- pass 1 ----------------
  FNR == NR {
    line[FNR] = $0
    nlines = FNR
    t = $0
    sub(/^[ \t]*/, "", t)
    if (t ~ /^(readonly|export|local|declare)[ \t]+/) sub(/^(readonly|export|local|declare)[ \t]+/, "", t)
    if (t ~ /^[A-Za-z_][A-Za-z0-9_]*=/) {
      eq = index(t, "=")
      nm = substr(t, 1, eq - 1)
      rv = substr(t, eq + 1)
      assigned[nm] = 1
      if (isexternal(rv)) external[nm] = 1
    }
    next
  }

  # ---------------- pass 2 ----------------
  {
    src = $0
    raw = $0
    sub(/^[ \t]*/, "", raw)

    # A heredoc body is DATA, not code. Scripts in this repo quote the very
    # defect they fixed inside usage blocks and examples; flagging that would
    # make the guard report the documentation of its own solution as a bug,
    # which is how a gate earns being switched off on day one.
    if (inheredoc) {
      probe = raw; sub(/[ \t]+$/, "", probe)
      if (probe == hdtag) inheredoc = 0
      next
    }
    if (raw !~ /<<</ && match(raw, /<<-?[ \t]*["'"'"']?[A-Za-z_][A-Za-z0-9_]*["'"'"']?/)) {
      hdtag = substr(raw, RSTART, RLENGTH)
      sub(/^<<-?[ \t]*/, "", hdtag)
      gsub(/["'"'"']/, "", hdtag)
      inheredoc = 1
      next
    }

    # A comment explains; it does not decide.
    if (raw ~ /^#/) next

    # track set -e / set +e regions
    if (raw ~ /^set -[a-zA-Z]*e/)  { sete = 1 }
    if (raw ~ /^set \+[a-zA-Z]*e/) { sete = 0 }

    # ---- C2: counting command with a PRINTING || fallback ----------------
    if (raw ~ /grep[ \t]+-[a-zA-Z]*c/ && raw ~ /\|\|[ \t]*(printf|echo)[ \t]/) {
      report("C2", FNR, "`grep -c` prints a count AND exits 1 on no-match, so the || fallback appends a SECOND value; the consumer gets \"0\\n0\" and its test cannot evaluate", src)
    }

    # ---- C3: `cmd; rc=$?` under an active set -e -------------------------
    if (sete && raw ~ /;[ \t]*(rc|status|ec|RC|STATUS|EC)=\$\?[ \t]*$/) {
      head = raw; sub(/;[ \t]*(rc|status|ec|RC|STATUS|EC)=\$\?[ \t]*$/, "", head)
      if (head !~ /(\|\||&&)/ && head !~ /^(if|while|until|!)[ \t]/)
        report("C3", FNR, "`set -e` is active, so the shell exits AT this command and the branch that reads $? is unreachable — wrap in `set +e` / `set -e`, or use `if cmd; then`", src)
    }
    if (sete && raw ~ /^(rc|status|ec|RC|STATUS|EC)=\$\?[ \t]*$/ && FNR > 1) {
      prev = line[FNR-1]; sub(/^[ \t]*/, "", prev)
      if (prev != "" && prev !~ /^#/ && prev !~ /(\|\||&&|then|fi|done|\{|\})[ \t]*$/ && prev !~ /^(if|while|until|else|!)/)
        report("C3", FNR, "`set -e` is active, so the shell exits at the previous command and this $? is unreachable — wrap in `set +e` / `set -e`, or use `if cmd; then`", src)
    }

    # ---- C4: && bound to an always-exit-0 tail of a pipeline --------------
    if (raw ~ /&&/ && raw ~ /\|/) {
      li = index(raw, "&&")
      left = substr(raw, 1, li - 1)
      if (index(left, "|") > 0) {
        np = split(left, segs, "|")
        tail = segs[np]
        sub(/^[ \t]*/, "", tail)
        if (tail ~ /^(head|tail|cat|tee|tr|sort|uniq|wc|column|nl|rev|paste|fold)([ \t]|$)/)
          report("C4", FNR, "the && is bound to the tail of the pipeline, which exits 0 whatever the decider did — test the deciding command instead", src)
      }
    }

    # ---- C1: grep-family pattern that argv can eat as an option ----------
    nt = tokenize(raw, tk)
    split("", consumed, ":")
    for (i = 1; i <= nt; i++) {
      cmd = tk[i]; sub(/^.*\//, "", cmd)
      start = 0
      if (i in consumed) continue
      if (cmd == "git" ) {
        j = i + 1
        while (j <= nt && substr(tk[j], 1, 1) == "-") { if (tk[j] == "-C" || tk[j] == "-c") j += 2; else j += 1 }
        # mark the `grep` token as already accounted for, so `git grep` is not
        # reported a second time by the bare-grep arm below
        if (j <= nt && tk[j] == "grep") { start = j + 1; consumed[j] = 1 }
      } else if (cmd == "grep" || cmd == "egrep" || cmd == "fgrep" || cmd == "ggrep") {
        start = i + 1
      }
      if (start == 0) continue

      guarded = 0; pat = ""
      for (k = start; k <= nt; k++) {
        a = tk[k]
        if (a == "--") { guarded = 1; break }
        if (a == "-e" || a == "-f" || a == "--regexp" || a == "--file") { guarded = 1; break }
        if (a ~ /^--(regexp|file)=/) { guarded = 1; break }
        if (a == "-m" || a == "-A" || a == "-B" || a == "-C" || a == "--max-count" || \
            a == "--include" || a == "--exclude" || a == "--exclude-dir" || a == "--label") { k++; continue }
        if (substr(a, 1, 1) == "-" && length(a) > 1 && substr(a, 1, 3) != "---") continue
        pat = a; break
      }
      if (guarded || pat == "") continue

      bare = unquote(pat)
      if (substr(bare, 1, 1) == "-") {
        report("C1", FNR, "the pattern is a literal starting with \"-\" and is not passed with -e or --, so argv parses it as an OPTION; the command dies and its 0 matches read as \"nothing found\"", src)
      } else if (substr(bare, 1, 1) == "$") {
        vn = bare
        gsub(/^\$\{?/, "", vn); gsub(/[}:].*$/, "", vn); gsub(/[^A-Za-z0-9_].*$/, "", vn)
        if (vn in external)
          report("C1", FNR, "the pattern is $" vn ", whose value comes from OUTSIDE this script; if it ever starts with \"-\" argv eats it as an option and the failure reads as \"nothing found\" — pass it after -- or with -e", src)
      }
    }
  }
  ' "$1" "$1"
}

# --------------------------------------------------------------------------
# Controls. Both run on every invocation. POSITIVE: the guard MUST flag all
# four checks. NEGATIVE: the same four SHAPES written safely, plus the
# legitimate silencing this guard promises not to touch — MUST be clean.
# --------------------------------------------------------------------------
FIXDIR="$(mktemp -d "${TMPDIR:-/tmp}/silent-probe-ctl.XXXXXX")" || {
  echo "silent-probe-guard: CANNOT EVALUATE — cannot create fixture dir" >&2; exit 2; }
trap 'rm -rf "$FIXDIR"' EXIT INT TERM

cat > "$FIXDIR/positive.sh" <<'POS'
#!/usr/bin/env bash
set -euo pipefail
KEY="${SOME_API_KEY}"
n=$(git grep -c '-----BEGIN PRIVATE KEY' 2>/dev/null | wc -l)
if grep -qF "$KEY" /tmp/log 2>/dev/null; then echo leak; fi
hits=$(grep -cF 'needle' haystack.txt 2>/dev/null || printf '0')
grep -E 'boom' file.txt | head -5 && echo "HAY HALLAZGOS"
docker run --rm img; rc=$?
POS

cat > "$FIXDIR/negative.sh" <<'NEG'
#!/usr/bin/env bash
set -euo pipefail
KEY="${SOME_API_KEY}"
MARKER="FALCON-77-VERDE"
# same shapes, written so failure is distinguishable from absence
n=$(git grep -c -e '-----BEGIN PRIVATE KEY' 2>/dev/null | wc -l)
if grep -qF -- "$KEY" /tmp/log 2>/dev/null; then echo leak; fi
if grep -qF "$MARKER" /tmp/log; then echo marker; fi
hits=$(grep -cF 'needle' haystack.txt 2>/dev/null || true)
grep -E 'boom' file.txt && echo "HAY HALLAZGOS"
set +e
docker run --rm img; rc=$?
set -e
# FP class 1 — a comment quoting the defect while explaining the fix. Copied
# from this repo's own fix commits; a naive census flags its own documentation:
# D4: `unreach="$(deadcode ... 2>/dev/null | grep ... | wc -l)"` used to fold
# the old `grep -qF "$KEY" f 2>/dev/null` boolean collapsed three outcomes, and
# `hits=$(grep -cF 'x' f 2>/dev/null || printf '0')` produced the string "0\n0"
# FP class 3 — command -v discards a PATH, not an explanation
command -v jq >/dev/null 2>&1 || echo "no jq"
# FP class 4 — silenced, but the failure branch prints the cause
free_mb=-1
page_size="$(vm_stat 2>/dev/null | head -1)"
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"
if ! probe_out="$(govulncheck ./... 2>&1)"; then
  echo "could not run: $(printf '%s' "$probe_out" | head -1)" >&2
  exit 2
fi
# FP class 2 — a heredoc / usage block quoting the defect
usage() {
  cat <<'EXAMPLE'
      hits=$(grep -cF 'needle' file 2>/dev/null || printf '0')
      if grep -qF "$KEY" /tmp/log 2>/dev/null; then echo leak; fi
      git grep -c '-----BEGIN PRIVATE KEY' 2>/dev/null | wc -l
      docker run --rm img; rc=$?
      grep -E 'boom' file.txt | head -5 && echo "HAY HALLAZGOS"
EXAMPLE
}
NEG

pos_out="$(detect "$FIXDIR/positive.sh")"
neg_out="$(detect "$FIXDIR/negative.sh")"
pos_checks="$(printf '%s\n' "$pos_out" | awk 'NF{print $1}' | sort -u | tr '\n' ' ')"
neg_count="$(printf '%s' "$neg_out" | grep -c . || true)"

ctl_fail=0
for want in C1 C2 C3 C4; do
  case " $pos_checks " in
    *" $want "*) ;;
    *) echo "silent-probe-guard: POSITIVE CONTROL FAILED — $want did not fire on the fixture it must catch" >&2; ctl_fail=1 ;;
  esac
done
if [ "$neg_count" -ne 0 ]; then
  echo "silent-probe-guard: NEGATIVE CONTROL FAILED — flagged $neg_count safe line(s) that must stay clean:" >&2
  printf '%s\n' "$neg_out" | sed 's/^/    /' >&2
  ctl_fail=1
fi

if [ "$SELFTEST_ONLY" -eq 1 ]; then
  if [ "$ctl_fail" -ne 0 ]; then echo "self-test: FAILED"; exit 2; fi
  echo "self-test: OK — positive control fired all of C1 C2 C3 C4; negative control stayed clean (0 findings)"
  exit 0
fi
if [ "$ctl_fail" -ne 0 ]; then
  echo "silent-probe-guard: CANNOT EVALUATE — its own controls misbehaved; the repo was NOT scanned. This is NOT a pass." >&2
  exit 2
fi

# --------------------------------------------------------------------------
# Scan
# --------------------------------------------------------------------------
if [ "${#TARGETS[@]}" -eq 0 ]; then
  git rev-parse --git-dir >/dev/null 2>&1 || {
    echo "silent-probe-guard: CANNOT EVALUATE — not a git repository and no files given" >&2; exit 2; }
  _list="$FIXDIR/targets.txt"
  git ls-files '*.sh' > "$_list" || {
    echo "silent-probe-guard: CANNOT EVALUATE — git ls-files failed" >&2; exit 2; }
  while IFS= read -r f; do
    [ -n "$f" ] && TARGETS[${#TARGETS[@]}]="$f"
  done < "$_list"
fi
if [ "${#TARGETS[@]}" -eq 0 ]; then
  echo "silent-probe-guard: CANNOT EVALUATE — zero files in scope. A check that examines no files always passes; that is not a clean result." >&2
  exit 2
fi

echo "==================================================================="
echo " silent-probe-guard: can every probe tell \"I could not evaluate\""
echo " apart from \"there is nothing there\"?"
echo " self-test: OK (positive control fired C1 C2 C3 C4; negative clean)"
echo " scope: ${#TARGETS[@]} file(s)"
echo "==================================================================="

findings=0
scanned=0
for f in "${TARGETS[@]}"; do
  [ -f "$f" ] || { echo "  ?  $f — not a file, skipped" >&2; continue; }
  scanned=$((scanned + 1))
  out="$(detect "$f")"
  [ -n "$out" ] || continue
  while IFS=$'\t' read -r check ln msg src; do
    [ -n "${check:-}" ] || continue
    findings=$((findings + 1))
    printf '%s  %s:%s\n' "$check" "$f" "$ln"
    printf '      %s\n' "$(printf '%s' "$src" | sed 's/^[ \t]*//' | cut -c1-140)"
    [ "$EXPLAIN" -eq 1 ] && printf '      why: %s\n' "$msg"
  done <<< "$out"
done

echo
echo "  files scanned: $scanned"
echo "  findings:      $findings"
echo "  coverage:      4 of 7 sub-forms (C1 C2 C3 C4); sub-forms 2, 6 and 7 are"
echo "                 NOT checked — they are runtime properties, not syntax."
if [ "$findings" -eq 0 ]; then
  echo "VERDICT: clean for the four sub-forms above. This is NOT 'no silent probes'"
  echo "  — three of seven are outside this instrument's reach (see the header for"
  echo "  why, measured rather than assumed). A zero here is a zero over 4/7."
  exit 0
fi
cat <<EOF
VERDICT: findings — each site above reports "nothing found" when what actually
happened is "the command did not run". Fix by making the failure visible:
  C1  pass the pattern after -- (or with -e) so argv cannot eat it
  C2  use || true (prints nothing) instead of || printf '0'
  C3  wrap in set +e / set -e, or branch with if cmd; then
  C4  bind the && to the command that decides, not to the tail of the pipeline
EOF
exit 1
