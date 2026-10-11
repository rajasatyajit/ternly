package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rajasatyajit/ternly/internal/capability"
	"github.com/rajasatyajit/ternly/internal/discover"
	"github.com/rajasatyajit/ternly/internal/idle"
)

// Background work (ADR 031): evaluations and the catalog's refresh run only
// while the session is idle, on AC power, and — for evaluations — with quota
// to spare; evaluations are opt-in, asked once.

// balanceTTL: Ollama's balance is asked at most every 10 minutes.
const balanceTTL = 10 * time.Minute

// quotaGate is the scheduler's quota check: a model's quota has no room when
// routing has it out (a 429 this session), when any ternly process recorded
// a hit on its quota group recently (the quota log), or — for Ollama Cloud
// with OLLAMA_API_KEY and the network allowed — when the documented balance
// says the allowance is spent. Purchased credit is spending, so it doesn't
// count as room: background work stays at $0 unless the user set a budget.
func quotaGate(log *discover.QuotaLog, router *discover.Router, balance func(context.Context) (*discover.Quota, error)) func(*discover.Model) (bool, string) {
	var mu sync.Mutex
	var cached *discover.Quota
	var at time.Time
	return func(m *discover.Model) (bool, string) {
		g := discover.QuotaGroup(m)
		if g == "" {
			return true, ""
		}
		now := time.Now()
		if t := router.ExhaustedUntil(m.Key()); !t.IsZero() {
			return false, "quota exhausted (rate limited until " + t.Format("15:04") + ")"
		}
		if ex, q := log.Exhausted(g, now); ex {
			return false, fmt.Sprintf("%s quota exhausted (hit %s by %s); checked again after %s", g, q.Seen.Format("2006-01-02 15:04"), q.Model, q.Reset().Format("2006-01-02 15:04"))
		}
		if balance == nil || !m.Cloud || m.Provider == nil || m.Provider.Kind != "ollama" {
			return true, ""
		}
		mu.Lock()
		defer mu.Unlock()
		if cached == nil || now.Sub(at) >= balanceTTL {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			q, err := balance(ctx)
			cancel()
			cached, at = q, now
			if err != nil {
				cached = nil // unknown: the quota log alone decides
			}
		}
		if q := cached; q != nil && q.Limit > 0 && q.Used >= q.Limit {
			_ = log.Mark(g, m.Key(), now, q.ResetsAt, "balance: "+q.Source)
			return false, fmt.Sprintf("%s allowance spent (%s); resets %s", g, q.Source, q.ResetsAt.Format("2006-01-02"))
		}
		return true, ""
	}
}

// activity wraps the UI's model so every key press or paste tells the idle
// gate the user is there; the UI itself is untouched.
type activity struct {
	tea.Model
	touch func()
}

func (a activity) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.PasteMsg:
		a.touch()
	}
	m, c := a.Model.Update(msg)
	a.Model = m
	return a, c
}

// refreshCatalog refreshes the catalog when it's due, only while gate
// allows: it waits for idle before starting and between requests, so the
// user's return pauses it after the request in flight; it goes on from
// there (each source's state is saved as it completes).
func refreshCatalog(ctx context.Context, cat *capability.Catalog, gate *idle.Gate, every time.Duration) {
	allowed := func() bool { ok, _ := gate.Allowed(); return ok }
	for ctx.Err() == nil {
		if cat.Due(every) {
			if gate.Wait(ctx) != nil {
				return
			}
			_ = cat.RefreshIdle(ctx, allowed)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour): // a session left open for days still refreshes daily
		}
	}
}

// askBackgroundEval is the first-run question. It's asked when the config
// has no routing.background_eval.enabled, on an interactive start (stdin and
// stderr are terminals; not a harness, CI or an evaluation's own runs), and
// the answer is saved; no answer (EOF) saves nothing and leaves it off.
func askBackgroundEval(cfgPath string, in io.Reader, out io.Writer) (on, saved bool, err error) {
	fmt.Fprint(out, "ternly can measure newly found models in the background, with its bundled test traps (never your code),\n"+
		"while you're idle and on AC power, within your free quota. Run background capability evaluations when idle? [y/N] ")
	var line []byte
	b := make([]byte, 1)
	for { // byte by byte: nothing after the line is taken from the UI
		n, rerr := in.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				break
			}
			line = append(line, b[0])
		}
		if rerr != nil {
			fmt.Fprintln(out)
			return false, false, nil // no answer: off, asked again next time
		}
	}
	a := strings.ToLower(strings.TrimSpace(string(line)))
	on = a == "y" || a == "yes"
	if err := setBackgroundEval(cfgPath, on); err != nil {
		return on, false, err
	}
	fmt.Fprintf(out, "saved: routing.background_eval.enabled = %v in %s (change it there any time)\n", on, cfgPath)
	return on, true, nil
}

// shouldAsk reports whether the first-run question applies.
func shouldAsk(fc fileConfig, getenv func(string) string, interactive bool) bool {
	if fc.Routing.BackgroundEval.Enabled != nil {
		return false // an explicit value is never asked about or changed
	}
	for _, k := range []string{"TERNLY_HARNESS", "TERNLY_BACKGROUND_EVAL", "TERNLY_EVAL", "CI"} {
		if getenv(k) != "" {
			return false
		}
	}
	return interactive
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// setBackgroundEval writes routing.background_eval.enabled into the config
// file, keeping every other setting (a string "routing" becomes
// {"version": …}). The file is created (0600) if missing.
func setBackgroundEval(cfgPath string, on bool) error {
	if p, err := filepath.EvalSymlinks(cfgPath); err == nil {
		cfgPath = p // a config kept in a dotfiles repository stays a link
	}
	top := map[string]json.RawMessage{}
	if b, err := os.ReadFile(cfgPath); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &top); err != nil {
			return fmt.Errorf("%s: %w (not changed)", cfgPath, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	routing := map[string]json.RawMessage{}
	if raw, ok := top["routing"]; ok {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			routing["version"] = raw
		} else if err := json.Unmarshal(raw, &routing); err != nil {
			return fmt.Errorf("%s: routing: %w (not changed)", cfgPath, err)
		}
	}
	be := map[string]json.RawMessage{}
	if raw, ok := routing["background_eval"]; ok {
		if err := json.Unmarshal(raw, &be); err != nil {
			return fmt.Errorf("%s: routing.background_eval: %w (not changed)", cfgPath, err)
		}
	}
	be["enabled"], _ = json.Marshal(on)
	routing["background_eval"], _ = json.Marshal(be)
	top["routing"], _ = json.Marshal(routing)
	b, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cfgPath), ".config-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), cfgPath)
}
