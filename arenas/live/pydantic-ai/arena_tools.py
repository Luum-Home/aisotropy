"""A real, actually-executed calculator for the `tools` mode.

Not a stub that returns a canned value: `safe_eval_expr` genuinely parses and
evaluates the arithmetic expression via Python's `ast` module, restricted to
a small whitelist of numeric operators (no `eval()`, no attribute/name
access, no calls) so it is safe to expose to model-controlled input.
"""

from __future__ import annotations

import ast
import operator

_BIN_OPS = {
    ast.Add: operator.add,
    ast.Sub: operator.sub,
    ast.Mult: operator.mul,
    ast.Div: operator.truediv,
    ast.FloorDiv: operator.floordiv,
    ast.Mod: operator.mod,
    ast.Pow: operator.pow,
}

_UNARY_OPS = {
    ast.USub: operator.neg,
    ast.UAdd: operator.pos,
}


def safe_eval_expr(expression: str) -> float:
    """Evaluate a numeric expression like `847 * 39 - 12` and return the real result.

    Supports + - * / // % ** and parentheses over int/float literals only.
    Raises ValueError for anything outside that grammar (names, calls,
    attribute access, strings, etc.) -- deliberately cannot execute arbitrary
    code even though the expression text comes from model output.
    """
    try:
        node = ast.parse(expression, mode="eval").body
    except SyntaxError as exc:
        raise ValueError(f"not a valid arithmetic expression: {expression!r}") from exc
    return _eval_node(node)


def _eval_node(node: ast.AST) -> float:
    if isinstance(node, ast.Constant) and isinstance(node.value, (int, float)) and not isinstance(
        node.value, bool
    ):
        return node.value
    if isinstance(node, ast.BinOp) and type(node.op) in _BIN_OPS:
        return _BIN_OPS[type(node.op)](_eval_node(node.left), _eval_node(node.right))
    if isinstance(node, ast.UnaryOp) and type(node.op) in _UNARY_OPS:
        return _UNARY_OPS[type(node.op)](_eval_node(node.operand))
    raise ValueError(f"unsupported expression node: {ast.dump(node)}")
