package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// evalArithmetic evaluates a plain arithmetic expression (+ - * / and
// parentheses, decimal numbers, unary minus) and returns the numeric
// result. This is a small self-contained recursive-descent parser — no
// third-party expression-evaluation dependency was introduced for one
// arena tool.
func evalArithmetic(expr string) (float64, error) {
	p := &arithParser{input: []rune(strings.TrimSpace(expr))}
	p.skipSpace()
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.pos != len(p.input) {
		return 0, fmt.Errorf("unexpected trailing input at position %d: %q", p.pos, string(p.input[p.pos:]))
	}
	return v, nil
}

type arithParser struct {
	input []rune
	pos   int
}

func (p *arithParser) skipSpace() {
	for p.pos < len(p.input) && unicode.IsSpace(p.input[p.pos]) {
		p.pos++
	}
}

func (p *arithParser) peek() (rune, bool) {
	if p.pos >= len(p.input) {
		return 0, false
	}
	return p.input[p.pos], true
}

// parseExpr handles + and - (lowest precedence).
func (p *arithParser) parseExpr() (float64, error) {
	v, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		c, ok := p.peek()
		if !ok || (c != '+' && c != '-') {
			return v, nil
		}
		p.pos++
		rhs, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if c == '+' {
			v += rhs
		} else {
			v -= rhs
		}
	}
}

// parseTerm handles * and / (higher precedence than +/-).
func (p *arithParser) parseTerm() (float64, error) {
	v, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		c, ok := p.peek()
		if !ok || (c != '*' && c != '/') {
			return v, nil
		}
		p.pos++
		rhs, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		if c == '*' {
			v *= rhs
		} else {
			if rhs == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			v /= rhs
		}
	}
}

// parseUnary handles a leading unary +/- before a factor.
func (p *arithParser) parseUnary() (float64, error) {
	p.skipSpace()
	if c, ok := p.peek(); ok && (c == '+' || c == '-') {
		p.pos++
		v, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		if c == '-' {
			return -v, nil
		}
		return v, nil
	}
	return p.parseFactor()
}

// parseFactor handles a parenthesized sub-expression or a number literal.
func (p *arithParser) parseFactor() (float64, error) {
	p.skipSpace()
	c, ok := p.peek()
	if !ok {
		return 0, fmt.Errorf("unexpected end of expression")
	}
	if c == '(' {
		p.pos++
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		p.skipSpace()
		c, ok := p.peek()
		if !ok || c != ')' {
			return 0, fmt.Errorf("expected ')' at position %d", p.pos)
		}
		p.pos++
		return v, nil
	}
	start := p.pos
	for p.pos < len(p.input) && (unicode.IsDigit(p.input[p.pos]) || p.input[p.pos] == '.') {
		p.pos++
	}
	if p.pos == start {
		return 0, fmt.Errorf("expected a number at position %d, got %q", p.pos, string(p.input[p.pos:]))
	}
	v, err := strconv.ParseFloat(string(p.input[start:p.pos]), 64)
	if err != nil {
		return 0, fmt.Errorf("parsing number %q: %w", string(p.input[start:p.pos]), err)
	}
	return v, nil
}
