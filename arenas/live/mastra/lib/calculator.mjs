// A real, executable arithmetic evaluator for the `tools` mode's `calculate` tool.
//
// Deliberately NOT `eval()` / `new Function()` on model-controlled input: this is a
// small recursive-descent parser over a strict grammar (numbers, + - * / (), unary
// minus) so the tool actually computes a result without any code-execution surface.
//
//   expr   := term (('+' | '-') term)*
//   term   := factor (('*' | '/') factor)*
//   factor := ('-' | '+')? (number | '(' expr ')')
//   number := digits ('.' digits)?

class ParseError extends Error {}

function tokenize(source) {
  const tokens = [];
  let i = 0;
  while (i < source.length) {
    const ch = source[i];
    if (/\s/.test(ch)) {
      i += 1;
      continue;
    }
    if ('+-*/()'.includes(ch)) {
      tokens.push({ type: ch, value: ch });
      i += 1;
      continue;
    }
    if (/[0-9.]/.test(ch)) {
      let j = i;
      let sawDot = false;
      while (j < source.length && /[0-9.]/.test(source[j])) {
        if (source[j] === '.') {
          if (sawDot) {
            throw new ParseError(`malformed number near position ${j}`);
          }
          sawDot = true;
        }
        j += 1;
      }
      const raw = source.slice(i, j);
      const value = Number(raw);
      if (!Number.isFinite(value)) {
        throw new ParseError(`invalid number literal "${raw}"`);
      }
      tokens.push({ type: 'number', value });
      i = j;
      continue;
    }
    throw new ParseError(`unexpected character "${ch}" at position ${i}`);
  }
  return tokens;
}

function makeParser(tokens) {
  let pos = 0;

  function peek() {
    return tokens[pos];
  }

  function next() {
    const t = tokens[pos];
    pos += 1;
    return t;
  }

  function parseFactor() {
    const t = peek();
    if (!t) {
      throw new ParseError('unexpected end of expression');
    }
    if (t.type === '-' || t.type === '+') {
      next();
      const value = parseFactor();
      return t.type === '-' ? -value : value;
    }
    if (t.type === 'number') {
      next();
      return t.value;
    }
    if (t.type === '(') {
      next();
      const value = parseExpr();
      const closing = next();
      if (!closing || closing.type !== ')') {
        throw new ParseError('missing closing parenthesis');
      }
      return value;
    }
    throw new ParseError(`unexpected token "${t.value}"`);
  }

  function parseTerm() {
    let value = parseFactor();
    while (peek() && (peek().type === '*' || peek().type === '/')) {
      const op = next();
      const rhs = parseFactor();
      if (op.type === '*') {
        value *= rhs;
      } else {
        if (rhs === 0) {
          throw new ParseError('division by zero');
        }
        value /= rhs;
      }
    }
    return value;
  }

  function parseExpr() {
    let value = parseTerm();
    while (peek() && (peek().type === '+' || peek().type === '-')) {
      const op = next();
      const rhs = parseTerm();
      value = op.type === '+' ? value + rhs : value - rhs;
    }
    return value;
  }

  return { parseExpr, isAtEnd: () => pos >= tokens.length };
}

/**
 * Evaluate a real arithmetic expression (+, -, *, /, parentheses, unary +/-) and
 * return the numeric result. Throws a ParseError with a descriptive message on any
 * malformed input -- never silently returns NaN.
 */
export function evaluateArithmetic(expression) {
  if (typeof expression !== 'string' || expression.trim().length === 0) {
    throw new ParseError('expression must be a non-empty string');
  }
  const tokens = tokenize(expression);
  const parser = makeParser(tokens);
  const value = parser.parseExpr();
  if (!parser.isAtEnd()) {
    throw new ParseError('trailing tokens after a complete expression');
  }
  return value;
}
