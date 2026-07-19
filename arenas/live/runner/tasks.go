package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// arithmeticTasks is the built-in tools-mode input set: real arithmetic with a
// known numeric answer, so the runner can score BOTH that the tool was actually
// executed (tool_calls[].result present) and that the numeric answer is correct.
var arithmeticTasks = []struct {
	id       string
	question string
	expected string // the correct numeric answer, comma-free
}{
	{"tool-mul", "What is 47 multiplied by 89? Use the calculator tool.", "4183"},
	{"tool-expr", "What is (48 + 12) * 3 / 2? Use the calculator tool.", "90"},
	{"tool-add", "What is 1234 plus 5678? Use the calculator tool.", "6912"},
}

const toolSystem = "You are an assistant that uses tools for arithmetic. When the user asks for a calculation, call the calculate tool with the expression, then report the numeric result."

func loadSuite(path string) ([]task, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tasks []task
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var t task
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return nil, fmt.Errorf("suite line %d: %w", i+1, err)
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func loadRagQuestions(path string) ([]ragQuestion, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var qs []ragQuestion
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var q ragQuestion
		if err := json.Unmarshal([]byte(line), &q); err != nil {
			return nil, fmt.Errorf("rag line %d: %w", i+1, err)
		}
		qs = append(qs, q)
	}
	return qs, nil
}

// buildWork produces the scored work items for one framework × mode, capping to
// maxTasks per mode when maxTasks > 0.
func buildWork(framework, mode string, tasks []task, rag []ragQuestion, maxTasks int) []workItem {
	switch mode {
	case "qa", "structured", "stream":
		return buildQAStyle(framework, mode, filterKind(tasks, "single_shot_qa"), maxTasks)
	case "memory":
		return buildMemory(framework, filterKind(tasks, "multi_turn_memory"), maxTasks)
	case "tools":
		return buildTools(framework, maxTasks)
	case "rag":
		return buildRAG(framework, rag, maxTasks)
	default:
		return nil
	}
}

// buildQAStyle covers qa, structured and stream: the same single-shot QA tasks,
// only the mode differs (and therefore the adapter behavior + scored signal).
func buildQAStyle(framework, mode string, qa []task, maxTasks int) []workItem {
	qa = capTasks(qa, maxTasks)
	out := make([]workItem, 0, len(qa))
	for _, t := range qa {
		out = append(out, workItem{
			Framework:   framework,
			Mode:        mode,
			ID:          t.ID,
			Kind:        mode, // qa|structured|stream
			Invocations: []adapterInput{{Mode: mode, System: t.System, Messages: t.Messages}},
			Question:    lastUser(t.Messages),
			Reference:   t.Reference,
			Answer:      t.Answer,
		})
	}
	return out
}

// buildMemory turns each multi_turn_memory task into a two-invocation persist→
// recall check on a shared session_id: invocation 1 carries the history up to
// (but excluding) the final user question; invocation 2 — a completely separate
// process spawn — asks only the recall question. The fact must survive.
func buildMemory(framework string, mem []task, maxTasks int) []workItem {
	mem = capTasks(mem, maxTasks)
	out := make([]workItem, 0, len(mem))
	for _, t := range mem {
		idx := lastUserIndex(t.Messages)
		if idx < 0 {
			continue
		}
		persist := t.Messages[:idx]
		recall := []inMessage{t.Messages[idx]}
		session := fmt.Sprintf("arena-mem-%s-%s", framework, t.ID)
		invs := []adapterInput{}
		if len(persist) > 0 {
			invs = append(invs, adapterInput{Mode: "memory", System: t.System, Messages: persist, SessionID: session})
		}
		invs = append(invs, adapterInput{Mode: "memory", Messages: recall, SessionID: session})
		out = append(out, workItem{
			Framework:   framework,
			Mode:        "memory",
			ID:          t.ID,
			Kind:        "memory",
			Invocations: invs,
			Question:    t.Messages[idx].Content,
			Reference:   t.Reference,
			Answer:      t.Answer,
		})
	}
	return out
}

func buildTools(framework string, maxTasks int) []workItem {
	set := arithmeticTasks
	if maxTasks > 0 && maxTasks < len(set) {
		set = set[:maxTasks]
	}
	out := make([]workItem, 0, len(set))
	for _, a := range set {
		out = append(out, workItem{
			Framework: framework,
			Mode:      "tools",
			ID:        a.id,
			Kind:      "tools",
			Invocations: []adapterInput{{
				Mode:     "tools",
				System:   toolSystem,
				Messages: []inMessage{{Role: "user", Content: a.question}},
			}},
			Question:       a.question,
			ExpectedNumber: a.expected,
		})
	}
	return out
}

// buildRAG runs each rag question through rag mode. When capping, it keeps the
// grounded questions first but guarantees at least one trap is included so the
// trap-refusal metric is always exercised.
func buildRAG(framework string, rag []ragQuestion, maxTasks int) []workItem {
	var grounded, traps []ragQuestion
	for _, q := range rag {
		if q.Trap {
			traps = append(traps, q)
		} else {
			grounded = append(grounded, q)
		}
	}
	selected := rag
	if maxTasks > 0 && maxTasks < len(rag) {
		selected = nil
		nGround := maxTasks
		nTrap := 0
		if len(traps) > 0 && maxTasks >= 2 {
			nTrap = 1
			nGround = maxTasks - 1
		}
		if nGround > len(grounded) {
			nGround = len(grounded)
		}
		selected = append(selected, grounded[:nGround]...)
		if nTrap > 0 {
			selected = append(selected, traps[0])
		}
	}
	out := make([]workItem, 0, len(selected))
	for _, q := range selected {
		kind := "rag_grounded"
		if q.Trap {
			kind = "rag_trap"
		}
		out = append(out, workItem{
			Framework: framework,
			Mode:      "rag",
			ID:        q.ID,
			Kind:      kind,
			Invocations: []adapterInput{{
				Mode:     "rag",
				Messages: []inMessage{{Role: "user", Content: q.Question}},
				TopK:     4,
			}},
			Question:       q.Question,
			Reference:      q.Reference,
			SupportingDocs: q.SupportingDocs,
		})
	}
	return out
}

// ---- helpers ---------------------------------------------------------------

func filterKind(tasks []task, kind string) []task {
	var out []task
	for _, t := range tasks {
		if t.Kind == kind {
			out = append(out, t)
		}
	}
	return out
}

func capTasks(t []task, maxTasks int) []task {
	if maxTasks > 0 && maxTasks < len(t) {
		return t[:maxTasks]
	}
	return t
}

func lastUser(msgs []inMessage) string {
	if i := lastUserIndex(msgs); i >= 0 {
		return msgs[i].Content
	}
	return ""
}

func lastUserIndex(msgs []inMessage) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i
		}
	}
	return -1
}
