package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

func sized(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return updated.(Model)
}

func TestModelInitialState(t *testing.T) {
	m := NewModel(context.Background())
	if m.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.cursor)
	}
	if m.running {
		t.Fatal("model should not be running before any task starts")
	}
	if len(m.tasks) == 0 {
		t.Fatal("model should carry the task registry")
	}
}

func TestModelWindowSizeMakesItReady(t *testing.T) {
	m := NewModel(context.Background())
	if m.ready {
		t.Fatal("model should not be ready before the first WindowSizeMsg")
	}
	m = sized(t, m)
	if !m.ready {
		t.Fatal("model should be ready after a WindowSizeMsg")
	}
}

func TestModelNavigation(t *testing.T) {
	m := sized(t, NewModel(context.Background()))

	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newModel.(Model)
	if m.cursor != 1 {
		t.Fatalf("cursor after down = %d, want 1", m.cursor)
	}

	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = newModel.(Model)
	if m.cursor != 0 {
		t.Fatalf("cursor after up = %d, want 0", m.cursor)
	}

	// Clamped: pressing up at the top must not go negative.
	newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = newModel.(Model)
	if m.cursor != 0 {
		t.Fatalf("cursor after up-at-top = %d, want 0 (clamped)", m.cursor)
	}

	// Clamped: pressing down at the bottom must not exceed len(tasks)-1.
	last := len(m.tasks) - 1
	for i := 0; i < len(m.tasks)+3; i++ {
		newModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = newModel.(Model)
	}
	if m.cursor != last {
		t.Fatalf("cursor after overshoot down = %d, want %d (clamped)", m.cursor, last)
	}
}

func TestModelQuitReturnsQuitCmd(t *testing.T) {
	m := sized(t, NewModel(context.Background()))

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("expected a non-nil command from pressing q")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", cmd())
	}
}

func TestModelResultMsgSetsStatus(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.running = true
	m.runKey = "fmt"

	newModel, _ := m.Update(resultMsg(Result{
		Pass:  true,
		Steps: []StepResult{{Key: "fmt", Title: "fmt", ExitCode: 0, Pass: true}},
	}))
	m = newModel.(Model)

	if m.running {
		t.Fatal("model should no longer be running after a resultMsg")
	}
	if m.statuses["fmt"] != StatusPass {
		t.Fatalf("statuses[fmt] = %v, want StatusPass", m.statuses["fmt"])
	}
}

func TestModelResultMsgMarksUnavailable(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.running = true
	m.runKey = "lint"

	newModel, _ := m.Update(resultMsg(Result{
		Pass:  false,
		Steps: []StepResult{{Key: "lint", Title: "lint", Unavailable: true, Hint: "install it"}},
	}))
	m = newModel.(Model)

	if m.statuses["lint"] != StatusUnavailable {
		t.Fatalf("statuses[lint] = %v, want StatusUnavailable", m.statuses["lint"])
	}
	if m.hints["lint"] != "install it" {
		t.Fatalf("hints[lint] = %q, want %q", m.hints["lint"], "install it")
	}
}

// TestTUISmoke drives the real Bubbletea event loop via teatest: it renders
// the initial task list, moves the selection, and quits cleanly — all
// without touching a real subprocess.
func TestTUISmoke(t *testing.T) {
	m := NewModel(context.Background())
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("Tasks")) && bytes.Contains(out, []byte("build"))
	}, teatest.WithDuration(3*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})

	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	final := tm.FinalModel(t).(Model)
	if final.cursor != 1 {
		t.Fatalf("final cursor = %d, want 1 after one down press", final.cursor)
	}
}
