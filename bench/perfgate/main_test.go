package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// abCSV is benchstat -format csv output for base.txt vs head.txt, in the
// shape benchstat v0.0.0-20260929162123 prints.
func abCSV(rows ...string) string {
	return "goos: linux\ngoarch: amd64\npkg: github.com/rajasatyajit/ternly/internal/session\ncpu: X\n" +
		",base.txt,,head.txt,,,\n,sec/op,CI,sec/op,CI,vs base,P\n" + strings.Join(rows, "\n") +
		"\ngeomean,1,,1,,+0.00%,\n\n"
}

func gate(t *testing.T, csv, size string, budgets map[string]budget) (bool, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ab.csv")
	if err := os.WriteFile(p, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := readRows(p)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	failed, err := check(&out, config{BudgetPct: 5, Alpha: 0.05, BinaryBudgetPct: 5, Budgets: budgets}, rows, size)
	if err != nil {
		t.Fatal(err)
	}
	return failed, out.String()
}

func TestGate(t *testing.T) {
	for _, c := range []struct {
		name, row, size string
		budgets         map[string]budget
		fail            bool
	}{
		{"significant regression beyond budget", "Record-16,1e-05,2%,1.2e-05,3%,+20.00%,p=0.000 n=10", "", nil, true},
		{"significant but within budget", "Record-16,1e-05,2%,1.04e-05,3%,+4.00%,p=0.001 n=10", "", nil, false},
		{"just over budget", "Record-16,1e-05,2%,1.051e-05,3%,+5.10%,p=0.001 n=10", "", nil, true},
		{"large but not significant", "Record-16,1e-05,40%,1.3e-05,50%,~,p=0.280 n=10", "", nil, false},
		{"improvement", "Record-16,1e-05,2%,5e-06,3%,-50.00%,p=0.000 n=10", "", nil, false},
		{"missing at head", "Record-16,1e-05,2%,,,,", "", nil, true},
		{"new at head", "Record-16,,,1e-05,2%,,", "", nil, false},
		{"new at head, trailing cells dropped", "Record-16,,,1e-05,2%", "", nil, false},
		{"missing at head, trailing cells dropped", "Record-16,1e-05,2%", "", nil, true},
		{"ADR budget allows it", "Record-16,1e-05,2%,1.2e-05,3%,+20.00%,p=0.000 n=10", "", map[string]budget{"BenchmarkRecord": {Pct: 25, ADR: "017"}}, false},
		{"binary grew beyond budget", "Record-16,1e-05,2%,1e-05,2%,~,p=0.9 n=10", "1000,1060", nil, true},
		{"binary within budget", "Record-16,1e-05,2%,1e-05,2%,~,p=0.9 n=10", "1000,1040", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if failed, out := gate(t, abCSV(c.row), c.size, c.budgets); failed != c.fail {
				t.Errorf("failed=%v, want %v\n%s", failed, c.fail, out)
			}
		})
	}
}

// Every unit is gated, not just time: an allocation regression fails too.
func TestGateAllocs(t *testing.T) {
	csv := abCSV("Record-16,1e-05,2%,1e-05,2%,~,p=0.9 n=10") +
		",base.txt,,head.txt,,,\n,allocs/op,CI,allocs/op,CI,vs base,P\nRecord-16,6,0%,9,0%,+50.00%,p=0.000 n=10\ngeomean,6,,9,,+50.00%,\n"
	if failed, out := gate(t, csv, "", nil); !failed || !strings.Contains(out, "allocs/op") {
		t.Errorf("allocation regression passed:\n%s", out)
	}
}

// A package only one side has: benchstat prints a one-column table.
func TestGateOneSidedTables(t *testing.T) {
	one := func(file string) string {
		return "pkg: github.com/rajasatyajit/ternly/internal/tui\n," + file + ",\n,sec/op,CI\nKeystroke-16,0.001,2%\ngeomean,0.001,\n"
	}
	if failed, out := gate(t, one("head.txt"), "", nil); failed || !strings.Contains(out, "new (no base)") {
		t.Errorf("a new package failed the gate:\n%s", out)
	}
	if failed, out := gate(t, one("base.txt"), "", nil); !failed || !strings.Contains(out, "missing at head") {
		t.Errorf("a vanished package passed the gate:\n%s", out)
	}
}

func TestConfigNeedsADRForBudget(t *testing.T) {
	p := filepath.Join(t.TempDir(), "perf.json")
	base := `{"budget_pct":5,"alpha":0.05,"count":10,"binary_budget_pct":5,"suites":[{"pkg":"x","bench":".","benchtime":"1x"}],`
	for cfg, ok := range map[string]bool{
		base + `"budgets":{}}`: true,
		base + `"budgets":{"BenchmarkX":{"pct":20,"adr":"017"}}}`:                              true,
		base + `"budgets":{"BenchmarkX":{"pct":20}}}`:                                          false,
		`{"budget_pct":5,"alpha":0.05,"count":2,"binary_budget_pct":5,"suites":[{"pkg":"x"}]}`: false, // too few runs for a test
	} {
		_ = os.WriteFile(p, []byte(cfg), 0o600)
		if _, err := loadConfig(p); (err == nil) != ok {
			t.Errorf("%s: err=%v, want ok=%v", cfg, err, ok)
		}
	}
}

// The committed config must load.
func TestCommittedConfig(t *testing.T) {
	if _, err := loadConfig("../perf.json"); err != nil {
		t.Fatal(err)
	}
}

func TestRetriesCounted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "retries.tsv")
	_ = os.WriteFile(p, []byte("base\tinternal/memory\t--- FAIL: BenchmarkSearch10k-4\nbase\tinternal/memory\t--- FAIL: BenchmarkSearch10k-4\nhead\tinternal/tui\tpanic: x\n"), 0o600)
	var out strings.Builder
	if err := retries(&out, p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Retries: base 2, head 1") || !strings.Contains(out.String(), "BenchmarkSearch10k") {
		t.Errorf("retry report:\n%s", out.String())
	}
	_ = os.WriteFile(p, nil, 0o600)
	out.Reset()
	if err := retries(&out, p); err != nil || !strings.Contains(out.String(), "Retries: base 0, head 0") {
		t.Errorf("empty retry file: %v %q", err, out.String())
	}
}
