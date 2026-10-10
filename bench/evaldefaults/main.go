// Command evaldefaults merges saved ternly --eval records (bench/run.sh
// fabrication → bench/results/fabrication) into internal/eval/defaults.json,
// the measurements ternly ships for models a user hasn't measured. An entry
// with the same model key is replaced; the others are kept (they still
// describe models other users have). Records of another eval version are
// refused.
//
//	go run ./bench/evaldefaults [-alias ollama/gemma4:latest=ollama/gemma4:e4b] bench/results/fabrication/ollama_*.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rajasatyajit/ternly/internal/eval"
)

type aliases []string

func (a *aliases) String() string     { return strings.Join(*a, ",") }
func (a *aliases) Set(v string) error { *a = append(*a, v); return nil }

func main() {
	out := flag.String("defaults", "internal/eval/defaults.json", "the defaults file to update")
	var al aliases
	flag.Var(&al, "alias", "from=to: also store the record of model `from` under model `to` (same weights, another tag); repeatable")
	flag.Parse()
	if err := run(*out, al, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "evaldefaults:", err)
		os.Exit(1)
	}
}

func run(path string, al aliases, files []string) error {
	var cur []eval.Record
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &cur); err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	byModel := map[string]eval.Record{}
	for _, r := range cur {
		byModel[r.Model] = r
	}
	added := map[string]eval.Record{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var r eval.Record
		if err := json.Unmarshal(b, &r); err != nil {
			return fmt.Errorf("%s: %v", f, err)
		}
		if !eval.Usable(r.Version) {
			return fmt.Errorf("%s: eval version %q, this ternly is %q", f, r.Version, eval.Version)
		}
		if r.Model == "" || r.Runs == 0 {
			return fmt.Errorf("%s: no model or no runs", f)
		}
		added[r.Model] = r
	}
	for _, a := range al {
		from, to, ok := strings.Cut(a, "=")
		r, have := added[from]
		if !ok || !have {
			return fmt.Errorf("alias %q: %q isn't among the records given", a, from)
		}
		r.Model = to
		added[to] = r
	}
	for m, r := range added {
		old, had := byModel[m]
		byModel[m] = r
		verb := "added"
		if had {
			verb = fmt.Sprintf("replaced (was v%s, %d run(s), T%d)", old.Version, old.Runs, old.Tier())
		}
		fmt.Printf("%-28s v%s %d run(s): T%d, trust lost=%v, memory %s — %s\n", m, r.Version, r.Runs, r.Tier(), r.Baitable(), r.Autonomy(), verb)
	}
	keys := make([]string, 0, len(byModel))
	for k := range byModel {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rs := make([]eval.Record, 0, len(keys))
	for _, k := range keys {
		rs = append(rs, byModel[k])
	}
	nb, _ := json.MarshalIndent(rs, "", "  ")
	return os.WriteFile(path, append(nb, '\n'), 0o644)
}
