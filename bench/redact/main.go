// Command redact rewrites harness output in place before it is committed:
// literal replacements, applied in the order given, then this machine's home
// directory as $HOME. It then scans each file with bench/leakguard and exits
// 1 if anything that must not reach git is left, naming it.
//
//	go run ./bench/redact -map "$PWD=\$REPO" -map "/tmp/job=\$JOB_TMP" bench/phasec/data/**/*.jsonl
//
// Order matters: give the longer paths first (a worktree before the repo it
// sits in), since the home directory is replaced last.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/rajasatyajit/ternly/bench/leakguard"
)

type maps [][2]string

func (m *maps) String() string { return fmt.Sprint(*m) }

func (m *maps) Set(s string) error {
	from, to, ok := strings.Cut(s, "=")
	if !ok || from == "" {
		return fmt.Errorf("want from=to, got %q", s)
	}
	*m = append(*m, [2]string{from, to})
	return nil
}

func main() {
	var ms maps
	flag.Var(&ms, "map", "from=to: replace every from with to; repeatable, applied in order")
	check := flag.Bool("check", false, "only scan; change nothing")
	flag.Parse()
	if home, err := os.UserHomeDir(); err == nil && home != "/" {
		ms = append(ms, [2]string{home, "$HOME"})
	}
	bad, err := run(ms, flag.Args(), *check, leakguard.ForThisMachine())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, b := range bad {
		fmt.Println(b)
	}
	if len(bad) > 0 {
		os.Exit(1)
	}
}

// run applies ms to each file (unless check) and returns what the guard
// still finds, one "path: finding" per line.
func run(ms maps, files []string, check bool, g *leakguard.Guard) ([]string, error) {
	var bad []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return bad, err
		}
		s := string(b)
		if !check {
			for _, m := range ms {
				s = strings.ReplaceAll(s, m[0], m[1])
			}
			if s != string(b) {
				st, err := os.Stat(f)
				if err != nil {
					return bad, err
				}
				if err := os.WriteFile(f, []byte(s), st.Mode().Perm()); err != nil {
					return bad, err
				}
			}
		}
		for _, x := range g.Scan(s) {
			bad = append(bad, f+": "+x.String())
		}
	}
	return bad, nil
}
