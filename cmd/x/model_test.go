package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// taskIndex returns the registry position of key, failing the test if it is
// not registered. Several tests below need to select a FAST, deterministic
// task (the "tasks" leaf, which just cats a temp file) via the cursor,
// rather than a slow real one like "build" or "check".
func taskIndex(t *testing.T, key string) int {
	t.Helper()
	for i, task := range Tasks() {
		if task.Key == key {
			return i
		}
	}
	t.Fatalf("task %q not found in registry", key)
	return -1
}

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
	for range len(m.tasks) + 3 {
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

// --- glyph ---

func TestGlyphRunningUsesSpinnerView(t *testing.T) {
	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	got := glyph(StatusRunning, sp)
	if got != sp.View() {
		t.Fatalf("glyph(StatusRunning) = %q, want the spinner's own View() %q", got, sp.View())
	}
}

func TestGlyphEveryOtherStatus(t *testing.T) {
	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	tests := []struct {
		name string
		s    Status
		want string
	}{
		{"pass", StatusPass, passStyle.Render("✓")},
		{"fail", StatusFail, failStyle.Render("✗")},
		{"unavailable", StatusUnavailable, unavailableStyle.Render("⊘")},
		{"idle-default", StatusIdle, idleStyle.Render("○")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := glyph(tt.s, sp); got != tt.want {
				t.Errorf("glyph(%v) = %q, want %q", tt.s, got, tt.want)
			}
		})
	}
}

// --- Update: WindowSizeMsg clamping + ready-vs-resize, and the unhandled
// default case ---

func TestUpdateWindowSizeClampsSmallDimensions(t *testing.T) {
	m := NewModel(context.Background())
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 5, Height: 5})
	m2 := newModel.(Model)
	if !m2.ready {
		t.Fatal("expected ready=true after the first WindowSizeMsg")
	}
	if m2.viewport.Width != 10 {
		t.Fatalf("viewport.Width = %d, want clamped to 10", m2.viewport.Width)
	}
	if m2.viewport.Height != 3 {
		t.Fatalf("viewport.Height = %d, want clamped to 3", m2.viewport.Height)
	}
}

func TestUpdateWindowSizeResizeAfterReady(t *testing.T) {
	m := sized(t, NewModel(context.Background())) // first resize: ready becomes true
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	m2 := newModel.(Model)
	if !m2.ready {
		t.Fatal("expected ready to remain true")
	}
	wantW := 120 - listWidth - 3
	if m2.viewport.Width != wantW {
		t.Fatalf("viewport.Width = %d, want %d (resize branch, not re-created)", m2.viewport.Width, wantW)
	}
}

func TestUpdateUnhandledMessageTypeIsNoop(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	type customMsg struct{}
	newModel, cmd := m.Update(customMsg{})
	if cmd != nil {
		t.Fatal("expected a nil command for an unhandled message type")
	}
	if _, ok := newModel.(Model); !ok {
		t.Fatal("expected Update to still return a Model")
	}
}

// --- applyResult branches ---

func TestApplyResultMarksFailedStep(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.running = true
	m.runKey = "sometask"
	m.events = make(chan Event)

	newModel, _ := m.Update(resultMsg(Result{
		Pass:  false,
		Steps: []StepResult{{Key: "sometask", ExitCode: 7, Pass: false}},
	}))
	m2 := newModel.(Model)
	if m2.statuses["sometask"] != StatusFail {
		t.Fatalf("statuses[sometask] = %v, want StatusFail", m2.statuses["sometask"])
	}
}

func TestApplyResultSetsRunKeyStatusWhenAbsentFromSteps(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.running = true
	m.runKey = "check" // composite: runKey itself never appears in res.Steps
	m.events = make(chan Event)

	newModel, _ := m.Update(resultMsg(Result{
		Pass:  true,
		Steps: []StepResult{{Key: "build", Pass: true}, {Key: "fmt", Pass: true}},
	}))
	m2 := newModel.(Model)
	if m2.statuses["check"] != StatusPass {
		t.Fatalf("statuses[check] = %v, want StatusPass (derived from overall res.Pass)", m2.statuses["check"])
	}
}

func TestApplyResultSetsRunKeyFailWhenAbsentFromStepsAndOverallFailed(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.running = true
	m.runKey = "check"
	m.events = make(chan Event)

	newModel, _ := m.Update(resultMsg(Result{
		Pass:  false,
		Steps: []StepResult{{Key: "build", Pass: false}},
	}))
	m2 := newModel.(Model)
	if m2.statuses["check"] != StatusFail {
		t.Fatalf("statuses[check] = %v, want StatusFail (derived from overall res.Pass=false)", m2.statuses["check"])
	}
}

// --- refreshViewport branches ---

func TestRefreshViewportNoopWhenNotReady(t *testing.T) {
	m := NewModel(context.Background()) // not sized: ready=false
	newModel, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	m2 := newModel.(Model)
	if m2.ready {
		t.Fatal("model should remain not ready without a WindowSizeMsg")
	}
	if m2.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (navigation itself must still work)", m2.cursor)
	}
}

func TestRefreshViewportShowsOutputForSelectedTask(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.cursor = taskIndex(t, "tasks")
	m.running = true
	m.runKey = "tasks"
	m.events = make(chan Event, 1)

	newModel, _ := m.Update(lineMsg{Step: "tasks", Stream: streamStdout, Text: "hello"})
	m2 := newModel.(Model)

	if !strings.Contains(m2.viewport.View(), "hello") {
		t.Fatalf("viewport.View() = %q, want it to contain the appended line", m2.viewport.View())
	}
}

// --- waitForEvent ---

func TestWaitForEventLine(t *testing.T) {
	ch := make(chan Event, 1)
	ch <- Event{Line: &Line{Step: "fmt", Stream: streamStdout, Text: "hello"}}

	msg := waitForEvent(ch)()
	lm, ok := msg.(lineMsg)
	if !ok {
		t.Fatalf("waitForEvent produced %T, want lineMsg", msg)
	}
	if lm.Text != "hello" || lm.Step != "fmt" {
		t.Fatalf("lineMsg = %+v, want Step=fmt Text=hello", lm)
	}
}

func TestWaitForEventResult(t *testing.T) {
	ch := make(chan Event, 1)
	ch <- Event{Result: &Result{Pass: true, Steps: []StepResult{{Key: "fmt", Pass: true}}}}

	msg := waitForEvent(ch)()
	rm, ok := msg.(resultMsg)
	if !ok {
		t.Fatalf("waitForEvent produced %T, want resultMsg", msg)
	}
	if !rm.Pass {
		t.Fatal("resultMsg.Pass = false, want true")
	}
}

func TestWaitForEventClosedChannelReturnsFinished(t *testing.T) {
	ch := make(chan Event)
	close(ch)

	msg := waitForEvent(ch)()
	if _, ok := msg.(runFinishedMsg); !ok {
		t.Fatalf("waitForEvent on a closed channel produced %T, want runFinishedMsg", msg)
	}
}

// --- start / mustLookup ---

func TestMustLookupKnownAndUnknown(t *testing.T) {
	task := mustLookup("fmt")
	if task.Key != "fmt" {
		t.Fatalf("mustLookup(\"fmt\").Key = %q, want \"fmt\"", task.Key)
	}

	zero := mustLookup("does-not-exist")
	if zero.Key != "" {
		t.Fatalf("mustLookup(unknown) = %+v, want the zero Task", zero)
	}
}

func TestModelStartLaunchesTaskAndSetsRunningState(t *testing.T) {
	m := sized(t, NewModel(context.Background()))

	newModel, cmd := m.start("tasks")
	m2 := newModel.(Model)

	if !m2.running {
		t.Fatal("expected running=true after start")
	}
	if m2.runKey != "tasks" {
		t.Fatalf("runKey = %q, want \"tasks\"", m2.runKey)
	}
	if m2.statuses["tasks"] != StatusRunning {
		t.Fatalf("statuses[tasks] = %v, want StatusRunning", m2.statuses["tasks"])
	}
	if m2.lastKey != "tasks" {
		t.Fatalf("lastKey = %q, want \"tasks\"", m2.lastKey)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil waitForEvent command")
	}

	// Drain to let the real (fast, local) task finish, then stop cleanly.
	go func() {
		for range m2.events {
		}
	}()
	if m2.runCancel != nil {
		m2.runCancel()
	}
}

func TestModelStartNoopWhenAlreadyRunning(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.running = true
	m.runKey = "some-other-task"

	newModel, cmd := m.start("tasks")
	if cmd != nil {
		t.Fatal("expected a nil command when a task is already running")
	}
	m2 := newModel.(Model)
	if m2.runKey != "some-other-task" {
		t.Fatalf("runKey changed to %q; start() must be a no-op while running", m2.runKey)
	}
}

func TestModelStartUnknownKeyIsNoop(t *testing.T) {
	m := sized(t, NewModel(context.Background()))

	newModel, cmd := m.start("does-not-exist")
	if cmd != nil {
		t.Fatal("expected a nil command for an unknown task key")
	}
	m2 := newModel.(Model)
	if m2.running {
		t.Fatal("an unknown task key must not flip running=true")
	}
}

// --- handleKey ---

func TestHandleKeyEnterStartsSelectedTask(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.cursor = taskIndex(t, "tasks")

	newModel, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := newModel.(Model)

	if !m2.running || m2.runKey != "tasks" {
		t.Fatalf("expected running=true runKey=tasks, got running=%v runKey=%q", m2.running, m2.runKey)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil command")
	}

	go func() {
		for range m2.events {
		}
	}()
	if m2.runCancel != nil {
		m2.runCancel()
	}
}

func TestHandleKeyEnterTogglesRunningTaskOff(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.cursor = taskIndex(t, "tasks")
	m.running = true
	m.runKey = "tasks"

	var cancelled bool
	m.runCancel = func() { cancelled = true }

	newModel, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("expected a nil command when toggling a running task off")
	}
	if !cancelled {
		t.Fatal("expected runCancel to be invoked")
	}
	// Toggle-off returns m unchanged (still "running" in the model's own
	// bookkeeping) until the runFinishedMsg/resultMsg arrives; assert it at
	// least didn't spuriously restart with a new run.
	if got := newModel.(Model).runKey; got != "tasks" {
		t.Fatalf("runKey = %q, want unchanged \"tasks\"", got)
	}
}

func TestHandleKeyAStartsCheck(t *testing.T) {
	m := sized(t, NewModel(context.Background()))

	newModel, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m2 := newModel.(Model)

	if m2.runKey != "check" {
		t.Fatalf("runKey = %q, want \"check\" (the \"a\" keybinding)", m2.runKey)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil command")
	}

	// "check" is the slow, real quality-gate composite: cancel immediately
	// and drain so this test doesn't actually wait out a full build+test run.
	go func() {
		for range m2.events {
		}
	}()
	if m2.runCancel != nil {
		m2.runCancel()
	}
}

func TestHandleKeyRWithNoLastKeyIsNoop(t *testing.T) {
	m := sized(t, NewModel(context.Background()))

	newModel, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd != nil {
		t.Fatal("expected a nil command when lastKey is empty")
	}
	if newModel.(Model).running {
		t.Fatal("expected running=false")
	}
}

func TestHandleKeyRRepeatsLastKey(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.lastKey = "tasks"

	newModel, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m2 := newModel.(Model)

	if m2.runKey != "tasks" {
		t.Fatalf("runKey = %q, want \"tasks\" (repeated via r)", m2.runKey)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil command")
	}

	go func() {
		for range m2.events {
		}
	}()
	if m2.runCancel != nil {
		m2.runCancel()
	}
}

// --- Update: lineMsg / runFinishedMsg ---

func TestUpdateLineMsgAppendsOutput(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.running = true
	m.runKey = "tasks"
	m.events = make(chan Event, 1)

	newModel, cmd := m.Update(lineMsg{Step: "tasks", Stream: streamStdout, Text: "hello"})
	m2 := newModel.(Model)

	if len(m2.outputs["tasks"]) != 1 || m2.outputs["tasks"][0] != "hello" {
		t.Fatalf("outputs[tasks] = %v, want [\"hello\"]", m2.outputs["tasks"])
	}
	if cmd == nil {
		t.Fatal("expected Update to re-issue waitForEvent")
	}
}

func TestUpdateLineMsgFromSubStepGetsPrefixed(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.running = true
	m.runKey = "check"
	m.events = make(chan Event, 1)

	newModel, _ := m.Update(lineMsg{Step: "build", Stream: streamStdout, Text: "compiling"})
	m2 := newModel.(Model)

	want := "[build] compiling"
	if len(m2.outputs["check"]) != 1 || m2.outputs["check"][0] != want {
		t.Fatalf("outputs[check] = %v, want [%q]", m2.outputs["check"], want)
	}
}

func TestUpdateRunFinishedMsgClearsRunningState(t *testing.T) {
	m := sized(t, NewModel(context.Background()))
	m.running = true
	m.runKey = "tasks"
	m.events = make(chan Event)
	m.runCancel = func() {}

	newModel, cmd := m.Update(runFinishedMsg{})
	m2 := newModel.(Model)

	if m2.running {
		t.Fatal("expected running=false after runFinishedMsg")
	}
	if m2.events != nil {
		t.Fatal("expected events to be cleared")
	}
	if m2.runCancel != nil {
		t.Fatal("expected runCancel to be cleared")
	}
	if cmd != nil {
		t.Fatal("expected a nil command")
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
