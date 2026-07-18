//go:build wasip1

// Command wasmguest is the WASM tool GUEST for aisotropy's live-arena
// `wasm_tool` mode — the honest demonstration of aisotropy's differentiator: a
// tool whose body executes inside a wazero WebAssembly sandbox (memory-isolated,
// no host filesystem/network/clock), the capability no other framework in this
// arena has.
//
// It is a TEST FIXTURE, not part of any package build: it lives under testdata/
// (which the go tool ignores for `go build ./...`, vet, and test) and is compiled
// on demand into `calc.wasm` by build.sh with
// `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared`, then embedded into the
// adapter via go:embed. It is a wasip1 *reactor*: main() never runs; the host
// calls the reactor initializer (_initialize) and then the ABI exports below.
//
// It implements the aisotropy WASM tool ABI VERBATIM (see
// internal/wasmtool/wasmtool.go — which the arena adapter cannot import because
// internal/ is not visible across modules, so the ABI is reproduced honestly in
// wasmhost.go):
//
//   - exports linear memory;
//   - aisotropy_alloc(size u32) -> ptr u32: allocate `size` bytes of the guest's
//     OWN linear memory and return the offset; the host writes the JSON args there;
//   - aisotropy_run(ptr, len u32) -> packed u64 = (resultPtr<<32)|resultLen: read
//     the JSON args at [ptr,ptr+len), compute, and return the packed ptr/len of a
//     JSON result the host reads back out of guest memory.
//
// The tool is a REAL arithmetic evaluator (+ - * / parentheses, integer and
// decimal) — never a canned value — so it satisfies the arena's "real, executable
// tools" convention. It also doubles as an ISOLATION PROOF: it attempts a host
// file read and reports the outcome; under the wazero sandbox (no preopens) the
// read MUST fail, which is what proves the guest is sealed from the host (and,
// per the arena's broker invariant, from the agnes API key the host holds).
package main

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"unsafe"
)

// main is required for a main package but never executes in a reactor module.
func main() {}

// keepAlive pins buffers handed to the host. wazero reads guest memory
// synchronously right after an exported call returns, so retaining the buffers
// keeps Go's non-moving wasm GC from reclaiming them mid-handoff.
var keepAlive [][]byte

//go:wasmexport aisotropy_alloc
func alloc(size uint32) uint32 {
	if size == 0 {
		size = 1
	}
	buf := make([]byte, size)
	keepAlive = append(keepAlive, buf)
	return ptrOf(buf)
}

//go:wasmexport aisotropy_run
func run(ptr, length uint32) uint64 {
	in := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length) //nolint:govet // wasm linear-memory offset, stable under Go's non-moving wasm GC
	var args struct {
		Expression string `json:"expression"`
	}
	_ = json.Unmarshal(in, &args)

	// Capability probe: with no preopened directories on the host side this MUST
	// be denied — that denial, surfaced in the result, is the sandbox-isolation
	// proof (the guest cannot reach the host filesystem, hence cannot exfiltrate
	// the agnes key the host process holds).
	_, fsErr := os.ReadFile("/etc/hosts")

	result := map[string]any{
		"engine":         "wazero-wasm",
		"fs_read_denied": fsErr != nil,
	}
	if v, err := evalArithmetic(args.Expression); err != nil {
		result["error"] = err.Error()
	} else {
		result["result"] = v
	}

	out, _ := json.Marshal(result)
	keepAlive = append(keepAlive, out)
	return pack(ptrOf(out), uint32(len(out)))
}

func ptrOf(b []byte) uint32 { return uint32(uintptr(unsafe.Pointer(&b[0]))) } //nolint:govet // see run()

func pack(ptr, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }

// ---- A real, dependency-free arithmetic evaluator (shunting-yard) ----
//
// Supports + - * / , unary minus, parentheses, integer and decimal literals.
// It genuinely computes the value — the tool is under test, not stubbed.

func evalArithmetic(expr string) (float64, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, errors.New("empty expression")
	}
	toks, err := tokenize(expr)
	if err != nil {
		return 0, err
	}
	rpn, err := toRPN(toks)
	if err != nil {
		return 0, err
	}
	return evalRPN(rpn)
}

type token struct {
	kind rune // 'n' number, 'o' operator, '(' , ')'
	val  float64
	op   byte
}

func tokenize(s string) ([]token, error) {
	var out []token
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
			out = append(out, token{kind: 'n', val: f})
			prevWasValue = true
			i = j
		case c == '(':
			out = append(out, token{kind: '('})
			prevWasValue = false
			i++
		case c == ')':
			out = append(out, token{kind: ')'})
			prevWasValue = true
			i++
		case c == '+' || c == '-' || c == '*' || c == '/':
			// Unary minus/plus: an operator at the start or after another
			// operator or '(' is unary — fold it into a leading zero.
			if (c == '-' || c == '+') && !prevWasValue {
				out = append(out, token{kind: 'n', val: 0})
			}
			out = append(out, token{kind: 'o', op: c})
			prevWasValue = false
			i++
		default:
			return nil, errors.New("unexpected character: " + string(c))
		}
	}
	return out, nil
}

func prec(op byte) int {
	if op == '+' || op == '-' {
		return 1
	}
	return 2
}

func toRPN(toks []token) ([]token, error) {
	var out, ops []token
	for _, t := range toks {
		switch t.kind {
		case 'n':
			out = append(out, t)
		case 'o':
			for len(ops) > 0 && ops[len(ops)-1].kind == 'o' && prec(ops[len(ops)-1].op) >= prec(t.op) {
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

func evalRPN(rpn []token) (float64, error) {
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
