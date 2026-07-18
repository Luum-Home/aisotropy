// calc.go is a real, dependency-free arithmetic evaluator (shunting-yard) used
// host-side by the `tools` mode's registered `calculate` tool. It genuinely
// computes the value — the tool is under test, never a canned constant (the
// arena's "real, executable tools" convention). The identical algorithm runs
// inside the WASM sandbox for `wasm_tool` mode (testdata/wasmguest).
package main

import (
	"errors"
	"strconv"
	"strings"
)

func evalArithmetic(expr string) (float64, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, errors.New("empty expression")
	}
	toks, err := tokenizeExpr(expr)
	if err != nil {
		return 0, err
	}
	rpn, err := toRPN(toks)
	if err != nil {
		return 0, err
	}
	return evalRPN(rpn)
}

type calcToken struct {
	kind rune // 'n' number, 'o' operator, '(' , ')'
	val  float64
	op   byte
}

func tokenizeExpr(s string) ([]calcToken, error) {
	var out []calcToken
	i := 0
	prevWasValue := false
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c >= '0' && c <= '9' || c == '.':
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			f, err := strconv.ParseFloat(s[i:j], 64)
			if err != nil {
				return nil, errors.New("bad number: " + s[i:j])
			}
			out = append(out, calcToken{kind: 'n', val: f})
			prevWasValue = true
			i = j
		case c == '(':
			out = append(out, calcToken{kind: '('})
			prevWasValue = false
			i++
		case c == ')':
			out = append(out, calcToken{kind: ')'})
			prevWasValue = true
			i++
		case c == '+' || c == '-' || c == '*' || c == '/':
			if (c == '-' || c == '+') && !prevWasValue {
				out = append(out, calcToken{kind: 'n', val: 0})
			}
			out = append(out, calcToken{kind: 'o', op: c})
			prevWasValue = false
			i++
		default:
			return nil, errors.New("unexpected character: " + string(c))
		}
	}
	return out, nil
}

func precOf(op byte) int {
	if op == '+' || op == '-' {
		return 1
	}
	return 2
}

func toRPN(toks []calcToken) ([]calcToken, error) {
	var out, ops []calcToken
	for _, t := range toks {
		switch t.kind {
		case 'n':
			out = append(out, t)
		case 'o':
			for len(ops) > 0 && ops[len(ops)-1].kind == 'o' && precOf(ops[len(ops)-1].op) >= precOf(t.op) {
				out = append(out, ops[len(ops)-1])
				ops = ops[:len(ops)-1]
			}
			ops = append(ops, t)
		case '(':
			ops = append(ops, t)
		case ')':
			found := false
			for len(ops) > 0 {
				top := ops[len(ops)-1]
				ops = ops[:len(ops)-1]
				if top.kind == '(' {
					found = true
					break
				}
				out = append(out, top)
			}
			if !found {
				return nil, errors.New("mismatched parentheses")
			}
		}
	}
	for len(ops) > 0 {
		top := ops[len(ops)-1]
		ops = ops[:len(ops)-1]
		if top.kind == '(' {
			return nil, errors.New("mismatched parentheses")
		}
		out = append(out, top)
	}
	return out, nil
}

func evalRPN(rpn []calcToken) (float64, error) {
	var st []float64
	for _, t := range rpn {
		if t.kind == 'n' {
			st = append(st, t.val)
			continue
		}
		if len(st) < 2 {
			return 0, errors.New("malformed expression")
		}
		b := st[len(st)-1]
		a := st[len(st)-2]
		st = st[:len(st)-2]
		var r float64
		switch t.op {
		case '+':
			r = a + b
		case '-':
			r = a - b
		case '*':
			r = a * b
		case '/':
			if b == 0 {
				return 0, errors.New("division by zero")
			}
			r = a / b
		}
		st = append(st, r)
	}
	if len(st) != 1 {
		return 0, errors.New("malformed expression")
	}
	return st[0], nil
}

// formatNumber renders a float result without a trailing ".0" for integers.
func formatNumber(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
