package tui

import (
	"testing"
	"time"

	"github.com/rajasatyajit/ternly/internal/agent"
)

// Idle costs ~0% CPU (ADR 022): once nothing animates, the ticker stops;
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
