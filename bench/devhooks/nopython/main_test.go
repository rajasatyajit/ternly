package main

import "testing"

func TestRunsPython(t *testing.T) {
	block := []string{
		`python3 -c 'print(1)'`,
		`python script.py`,
		`/usr/bin/python3 -V`,
		`python3.12 -m json.tool x.json`,
		`pypy3 a.py`,
		`cd /tmp && python3 x.py`,
		`ls; python3 x.py`,
		`cat a | python3 -m json.tool`,
		`true || python3 x.py`,
		"echo $(python3 -c 'print(1)')",
		"echo `python3 -V`",
		`echo "$(python3 -V)"`,
		`(python3 x.py)`,
		`FOO=1 python3 x.py`,
		`env FOO=1 python3 x.py`,
		`env -i PATH=/usr/bin python3 x.py`,
		`sudo -u nobody python3 x.py`,
		`timeout 10 python3 x.py`,
		`timeout -s KILL 10 python3 x.py`,
		`nice -n 5 python3 x.py`,
		`nohup python3 x.py &`,
		`flock -o /tmp/l python3 x.py`,
		`find . -name '*.json' | xargs python3 -m json.tool`,
		`bash -c "python3 x.py"`,
		`sh -lc 'cd /tmp && python3 x.py'`,
		`eval "python3 x.py"`,
		`uv run x.py`,
		`uvx ruff`,
		"ls\npython3 --version",
		`python3 --version >/dev/null 2>&1; true`, // the slip this hook exists for
	}
	allow := []string{
		`go test ./...`,
		`grep -n python main.go`,
		`echo "python3 is not used here"`,
		`git log --grep python`,
		`rg 'python3' docs/`,
		`ls /usr/lib/python3.12`,
		`cat python.txt`,
		`./pythonish-tool`,
		`echo hi # python3 in a comment`,
		`python3 -V # owner-approved-python`,
		``,
	}
	for _, c := range block {
		if _, ok := runsPython(c); !ok {
			t.Errorf("not blocked: %q", c)
		}
	}
	for _, c := range allow {
		if w, ok := runsPython(c); ok {
			t.Errorf("blocked (%q): %q", w, c)
		}
	}
}
