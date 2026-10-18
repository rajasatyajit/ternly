package eval

import (
	"os"
	"path/filepath"
	"testing"
)

// A stopped evaluation's finished traps come back on the next run; a journal
// from another version, model or run count doesn't.
func TestJournalResumes(t *testing.T) {
	dir := t.TempDir()
	j, done, err := OpenJournal(dir, "ollama/granite4.2:latest", 3)
	if err != nil || len(done.Outcomes) != 0 {
		t.Fatalf("fresh: %v %v", done, err)
	}
	a := Outcome{Trap: "phantom-api", Kind: "fabrication", Score: Checked, Ms: 1200}
	b := Outcome{Trap: "stale-note", Kind: "memory", Score: Fabricated, Why: "used the stale note"}
	if err := j.Add("ollama/granite4.2:latest", 3, 1, "ollama/granite4.2:latest", a); err != nil {
		t.Fatal(err)
	}
	if err := j.Add("ollama/granite4.2:latest", 3, 1, "ollama/granite4.2:latest", b); err != nil {
		t.Fatal(err)
	}
	j.Close() // the process stops here

	// a kill mid-write leaves a cut line: ignored, that trap runs again
	f, _ := os.OpenFile(j.Path(), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"version":"` + Version + `","model":"ollama/gra`)
	f.Close()

	j2, done, err := OpenJournal(dir, "ollama/granite4.2:latest", 3)
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := done.Has(1, "phantom-api"); !ok || o != a {
		t.Errorf("phantom-api: %+v %v", o, ok)
	}
	if o, ok := done.Has(1, "stale-note"); !ok || o.Score != Fabricated || o.Why != b.Why {
		t.Errorf("stale-note: %+v %v", o, ok)
	}
	if _, ok := done.Has(2, "phantom-api"); ok {
		t.Error("run 2 wasn't run")
	}
	if done.Key != "ollama/granite4.2:latest" {
		t.Errorf("key %q", done.Key)
	}
	j2.Close()

	// another run count: a different measurement, so the journal starts afresh
	j3, done, _ := OpenJournal(dir, "ollama/granite4.2:latest", 1)
	if len(done.Outcomes) != 0 {
		t.Errorf("runs 1 kept a runs-3 journal: %v", done.Outcomes)
	}
	j3.Close()
	if j4, done, _ := OpenJournal(dir, "ollama/granite4.2:latest", 3); len(done.Outcomes) != 0 {
		t.Errorf("the runs-1 restart should have replaced it: %v", done.Outcomes)
	} else {
		if err := j4.Remove(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "ollama_granite4.2_latest.partial.jsonl")); !os.IsNotExist(err) {
			t.Errorf("Remove left the journal: %v", err)
		}
	}
}

// Another eval version's journal doesn't count.
func TestJournalOtherVersion(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.partial.jsonl")
	_ = os.WriteFile(p, []byte(`{"version":"0","model":"m","runs":1,"run":1,"outcome":{"trap":"x","kind":"fabrication","score":"checked","ms":1}}`+"\n"), 0o644)
	_, done, err := OpenJournal(dir, "m", 1)
	if err != nil || len(done.Outcomes) != 0 {
		t.Fatalf("kept another version's outcomes: %v %v", done.Outcomes, err)
	}
}
