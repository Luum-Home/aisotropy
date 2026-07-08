package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Status is the last known outcome of a task shown in the task list.
type Status int

const (
	StatusIdle Status = iota
	StatusRunning
	StatusPass
	StatusFail
	StatusUnavailable
)

var (
	passStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	failStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	unavailableStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	idleStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selectedStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	headerStyle      = lipgloss.NewStyle().Bold(true).Underline(true)
	helpStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	listWidth        = 30
)

// glyph renders the status indicator shown to the left of a task's title.
func glyph(s Status, sp spinner.Model) string {
	switch s {
	case StatusRunning:
		return sp.View()
	case StatusPass:
		return passStyle.Render("✓")
	case StatusFail:
		return failStyle.Render("✗")
	case StatusUnavailable:
		return unavailableStyle.Render("⊘")
	default:
		return idleStyle.Render("○")
	}
}

// lineMsg and resultMsg are the two messages a running Task's Events channel
// can produce; runFinishedMsg guards against reading a closed channel.
type lineMsg Line
type resultMsg Result
type runFinishedMsg struct{}

// Model is the Bubbletea model for the interactive developer console.
type Model struct {
	rootCtx context.Context

	tasks    []Task
	cursor   int
	statuses map[string]Status
	hints    map[string]string
	outputs  map[string][]string

	spinner  spinner.Model
	viewport viewport.Model
	ready    bool

	running   bool
	runKey    string
	runCancel context.CancelFunc
	events    chan Event

	lastKey  string
	width    int
	height   int
	quitting bool
}

// NewModel builds the initial Model. rootCtx is the parent context for every
// task the TUI launches; cancelling it (e.g. on quit) stops any running task.
func NewModel(rootCtx context.Context) Model {
	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	return Model{
		rootCtx:  rootCtx,
		tasks:    Tasks(),
		statuses: make(map[string]Status),
		hints:    make(map[string]string),
		outputs:  make(map[string][]string),
		spinner:  sp,
	}
}

func (m Model) Init() tea.Cmd {
	return m.spinner.Tick
}

// waitForEvent blocks on the current run's Events channel and turns the next
// event into a tea.Msg. Only one such command is ever in flight at a time
// (Update re-issues it after each event), so there is a single reader per
// channel and no risk of the UI racing itself over run state.
func waitForEvent(events chan Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return runFinishedMsg{}
		}
		if ev.Line != nil {
			return lineMsg(*ev.Line)
		}
		return resultMsg(*ev.Result)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		vpWidth := msg.Width - listWidth - 3
		if vpWidth < 10 {
			vpWidth = 10
		}
		vpHeight := msg.Height - 4
		if vpHeight < 3 {
			vpHeight = 3
		}
		if !m.ready {
			m.viewport = viewport.New(vpWidth, vpHeight)
			m.ready = true
		} else {
			m.viewport.Width = vpWidth
			m.viewport.Height = vpHeight
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case lineMsg:
		prefix := ""
		if msg.Step != m.runKey {
			prefix = "[" + msg.Step + "] "
		}
		m.outputs[m.runKey] = append(m.outputs[m.runKey], prefix+msg.Text)
		m.refreshViewport()
		return m, waitForEvent(m.events)

	case resultMsg:
		res := Result(msg)
		m.applyResult(m.runKey, res)
		m.running = false
		m.events = nil
		m.runCancel = nil
		m.refreshViewport()
		return m, nil

	case runFinishedMsg:
		m.running = false
		m.events = nil
		m.runCancel = nil
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

// applyResult records the terminal status for every step of a finished run.
// For a leaf task res.Steps has exactly one entry (keyed by runKey itself);
// for a composite task each sub-task's own status is also recorded so the
// list reflects e.g. "lint" as unavailable even when driven via "check".
func (m *Model) applyResult(runKey string, res Result) {
	for _, s := range res.Steps {
		status := StatusPass
		switch {
		case s.Unavailable:
			status = StatusUnavailable
		case !s.Pass:
			status = StatusFail
		}
		m.statuses[s.Key] = status
		m.hints[s.Key] = s.Hint
	}
	if _, ok := m.statuses[runKey]; !ok {
		if res.Pass {
			m.statuses[runKey] = StatusPass
		} else {
			m.statuses[runKey] = StatusFail
		}
	}
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		if m.runCancel != nil {
			m.runCancel()
		}
		m.quitting = true
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.refreshViewport()
		}
		return m, nil

	case "down", "j":
		if m.cursor < len(m.tasks)-1 {
			m.cursor++
			m.refreshViewport()
		}
		return m, nil

	case "enter":
		selected := m.tasks[m.cursor].Key
		if m.running && m.runKey == selected {
			// Toggle: enter on the currently running task stops it (this is
			// how a long-running task like "run" is cancelled without
			// quitting the whole console).
			if m.runCancel != nil {
				m.runCancel()
			}
			return m, nil
		}
		return m.start(selected)

	case "a":
		return m.start("check")

	case "r":
		if m.lastKey != "" {
			return m.start(m.lastKey)
		}
		return m, nil
	}

	return m, nil
}

// start launches a task by key, unless another one is already running.
func (m Model) start(key string) (tea.Model, tea.Cmd) {
	if m.running {
		return m, nil
	}
	if _, ok := Lookup(key); !ok {
		return m, nil
	}

	ctx, cancel := context.WithCancel(m.rootCtx)
	run := StartTask(ctx, mustLookup(key))

	m.running = true
	m.runKey = key
	m.runCancel = cancel
	m.events = run.Events
	m.lastKey = key
	m.statuses[key] = StatusRunning
	m.outputs[key] = nil
	m.refreshViewport()

	return m, waitForEvent(run.Events)
}

func mustLookup(key string) Task {
	t, _ := Lookup(key)
	return t
}

// refreshViewport recomputes the viewport content for the currently selected
// task (which live-updates while it is the one running).
func (m *Model) refreshViewport() {
	if !m.ready {
		return
	}
	key := m.tasks[m.cursor].Key
	lines := m.outputs[key]
	if len(lines) == 0 {
		m.viewport.SetContent(helpStyle.Render("(no output yet — press enter to run)"))
		return
	}
	m.viewport.SetContent(strings.Join(lines, "\n"))
	m.viewport.GotoBottom()
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if !m.ready {
		return "initializing…"
	}

	var list strings.Builder
	list.WriteString(headerStyle.Render("Tasks") + "\n")
	for i, t := range m.tasks {
		g := glyph(m.statuses[t.Key], m.spinner)
		row := fmt.Sprintf("%s %-8s", g, t.Key)
		if i == m.cursor {
			row = selectedStyle.Render("> " + row)
		} else {
			row = "  " + row
		}
		list.WriteString(row + "\n")
	}

	left := lipgloss.NewStyle().Width(listWidth).Render(list.String())
	right := m.viewport.View()
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	help := helpStyle.Render("↑/↓ or j/k navigate • enter run/stop • a check • r re-run • q quit")
	return lipgloss.JoinVertical(lipgloss.Left, body, help)
}
