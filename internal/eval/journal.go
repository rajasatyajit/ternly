package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Journal keeps a model's trap outcomes as each one finishes, so an
// evaluation that stops part-way (killed, crashed, or stopped by a quota)
// resumes where it stopped instead of starting over. Before it, a model's
// record was written only when the whole model finished: granite4.2's run,
// stopped 43 minutes in, left nothing (Phase C, bench/phasec/STATE.md).
//
// The journal is <dir>/<model>.partial.jsonl, one line per finished trap. A
// journal written by another eval version, or for another number of runs,
// is ignored and replaced: its outcomes don't belong to this measurement.
// The caller removes it once the model's record is saved.
type Journal struct {
	path string
	f    *os.File
}

type journalLine struct {
	Version string  `json:"version"`
	Model   string  `json:"model"`
	Runs    int     `json:"runs"`
	Run     int     `json:"run"`
	Key     string  `json:"key,omitempty"` // the model's resolved key, as the record is saved under
	Outcome Outcome `json:"outcome"`
}

// Done is what a journal already holds: outcomes by run and trap, and the
// model's resolved key if a trap recorded one.
type Done struct {
	Outcomes map[string]Outcome
	Key      string
}

// Has reports the recorded outcome of run's trap.
func (d Done) Has(run int, trap string) (Outcome, bool) {
	o, ok := d.Outcomes[doneKey(run, trap)]
	return o, ok
}

func doneKey(run int, trap string) string { return fmt.Sprintf("%d/%s", run, trap) }

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// OpenJournal opens model's journal in dir for an evaluation of runs runs,
// returning what it already holds (nothing if it was written for another
// version, model or run count, in which case it starts afresh).
func OpenJournal(dir, model string, runs int) (*Journal, Done, error) {
	done := Done{Outcomes: map[string]Outcome{}}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, done, err
	}
	path := filepath.Join(dir, unsafeName.ReplaceAllString(model, "_")+".partial.jsonl")
	fresh := false
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			var l journalLine
			if json.Unmarshal(sc.Bytes(), &l) != nil {
				continue // a line cut short by a kill: that trap runs again
			}
			if l.Version != Version || l.Model != model || l.Runs != runs {
				fresh = true
				break
			}
			done.Outcomes[doneKey(l.Run, l.Outcome.Trap)] = l.Outcome
			if l.Key != "" {
				done.Key = l.Key
			}
		}
		f.Close()
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fresh {
		done = Done{Outcomes: map[string]Outcome{}}
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, done, err
	}
	return &Journal{path: path, f: f}, done, nil
}

// Add records run's outcome, on disk before it returns.
func (j *Journal) Add(model string, runs, run int, key string, o Outcome) error {
	b, err := json.Marshal(journalLine{Version: Version, Model: model, Runs: runs, Run: run, Key: key, Outcome: o})
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return j.f.Sync()
}

// Remove deletes the journal: the model's record is saved.
func (j *Journal) Remove() error {
	j.f.Close()
	return os.Remove(j.path)
}

// Close closes the journal and keeps it, for a later resume.
func (j *Journal) Close() error { return j.f.Close() }

// Path is where the journal is.
func (j *Journal) Path() string { return j.path }
