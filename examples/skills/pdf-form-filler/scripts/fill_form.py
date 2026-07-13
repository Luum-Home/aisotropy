#!/usr/bin/env python3
"""fill_form.py — PoC stub for the pdf-form-filler skill's executable behavior.

This is the *code* half of an Agent Skill (agentskills.io): a script under
`scripts/`. In aisotropy it does NOT run as ambient shell behavior — it is
surfaced as an explicit, scope-gated aisotropy tool (ADR-0022 rule 2). See
docs/guides/importing-agent-skills.md for how its declared inputs
(source_path, answers_json, flatten) map to a typed tool's argument schema or
an MCP tool schema.

Contract (stdin -> stdout, both JSON), so it is wrappable by either an MCP
stdio server or a typed ToolExecutor without changing this file:

    in : {"source_path": "...", "answers_json": "{...}", "flatten": true}
    out: {"filled_path": "..."}
"""
import json
import sys


def fill_form(source_path: str, answers_json: str, flatten: bool) -> str:
    # PoC stub: a real implementation would open the AcroForm, set each field
    # from json.loads(answers_json), optionally flatten, and write a new PDF.
    # Here we only validate inputs and echo a deterministic output path.
    answers = json.loads(answers_json)
    if not source_path:
        raise ValueError("source_path is required")
    _ = (answers, flatten)
    return source_path.replace(".pdf", ".filled.pdf")


def main() -> int:
    req = json.load(sys.stdin)
    filled = fill_form(
        req["source_path"],
        req["answers_json"],
        bool(req.get("flatten", False)),
    )
    json.dump({"filled_path": filled}, sys.stdout)
    return 0


if __name__ == "__main__":
    sys.exit(main())
