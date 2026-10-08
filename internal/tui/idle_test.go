package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/agent"
	"github.com/rajasatyajit/ternly/internal/tools"
)

// Idle costs ~0% CPU (ADR 023): once nothing animates, the ticker stops;
// it starts again when a turn or a tool does.
func TestTickerStopsWhenIdle(t *testing.T) {
	m := testModel(t)
	m.discovering = false
	m.ticking = true
	m.Update(tickMsg(time.Now()))
	if m.ticking {
		t.Fatal("idle: the ticker rescheduled itself")
	}
	m.busy = true
	m.Update(agentMsg(agent.Event{Kind: agent.EvToolStart, Tool: "bash", ToolID: "1", Text: "go test"}))
	if !m.ticking {
		t.Fatal("a running tool should restart the ticker")
	}
	m.busy = false
	m.Update(agentMsg(agent.Event{Kind: agent.EvToolEnd, Tool: "bash", ToolID: "1", OK: true}))
	m.Update(tickMsg(time.Now())) // the last frame
	m.Update(tickMsg(time.Now()))
	if m.ticking {
		t.Fatal("after the tool ended and the turn finished, the ticker kept running")
	}
}

// While a permission dialog waits, nothing looks busy: no ticker, no
// spinner, and the activity line says it's the user's turn.
func TestWaitingIsStill(t *testing.T) {
	m := testModel(t)
	m.discovering, m.busy, m.activity = false, true, "Running Bash"
	m.Update(agentMsg(agent.Event{Kind: agent.EvToolStart, Tool: "bash", ToolID: "1", Text: "go test ./... || true"}))
	m.Update(permMsg{tool: "bash", summary: "go test ./... || true", reply: make(chan tools.Decision, 1)})
	m.ticking = false
	m.Update(tickMsg(time.Now()))
	if m.ticking {
		t.Fatal("a pending permission dialog kept the ticker running")
	}
	v := m.View().Content
	if !strings.Contains(v, "Waiting for your answer") || strings.Contains(v, "Running Bash…") {
		t.Fatalf("activity line while waiting:\n%s", v)
	}
	f1 := m.View().Content
	m.frame += 3
	m.refresh(false)
	if m.View().Content != f1 {
		t.Fatal("the screen changes while waiting for the user")
	}
}
