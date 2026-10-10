package rootfs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// direct are the calls that read the filesystem without confinement.
var direct = map[string]bool{"os.ReadFile": true, "os.Open": true, "os.OpenFile": true, "os.ReadDir": true,
	"filepath.WalkDir": true, "filepath.Walk": true, "filepath.Glob": true, "fs.ReadFile": true, "fs.WalkDir": true, "os.DirFS": true}

// allowed lists every function that may read directly, and why. Anything
// that loads workspace or plugin content must use this package instead; a
// new direct read fails TestOnlyConfinedReads until it is either routed
// through rootfs or added here with a reason a reviewer can check.
var allowed = map[string]string{
	// ternly's own state (config, data and cache directories)
	"main.go run":                                        "~/.config/ternly/config.json",
	"internal/plugins/runtime.go contentSum":             "hashes watched files to detect changes; the contents are never used",
	"internal/mcpauth/store.go FileStore.load":           "remote MCP credentials (0600, links refused)",
	"internal/mcpauth/store.go Indexed.index":            "remote MCP credential index",
	"internal/mcpauth/store.go writeAtomic":              "writes ternly's credential files",
	"internal/mcpremote/remote.go Manager.grants":        "remote MCP network grants",
	"internal/capability/catalog.go Catalog.open":        "catalog cache",
	"internal/capability/rank.go Suggester.load":         "suggestion state",
	"internal/capability/validate.go Outcomes.load":      "outcome log",
	"internal/capability/validate.go Outcomes.Precision": "outcome log",
	"internal/capability/validate.go Outcomes.Record":    "outcome log",
	"internal/capability/validate.go Outcomes.Report":    "outcome log",
	"internal/checkpoint/checkpoint.go lockFile":         "checkpoint lock",
	"internal/checkpoint/checkpoint.go Repo.GC":          "checkpoint store",
	"internal/checkpoint/checkpoint.go Repo.seedIndex":   "checkpoint store",
	"internal/checkpoint/checkpoint.go Repo.Size":        "checkpoint store",
	"internal/discover/discover.go loadCatalog":          "model catalog cache",
	"internal/discover/discover.go LoadKeys":             "keys file",
	"internal/discover/discover.go stale":                "model catalog cache",
	"internal/discover/speed.go OpenSpeeds":              "measured model speeds (ADR 018)",
	"internal/discover/hardware.go memAvailable":         "/proc/meminfo",
	"internal/discover/hardware.go amdVRAM":              "the amdgpu sysfs VRAM size",
	"internal/bgeval/bgeval.go OpenLedger":               "background-evaluation ledger (ADR 018)",
	"routing_config.go backgroundEvals":                  "writes a background evaluation's log under the data directory",
	"internal/llm/cache.go cachedClient.Stream":          "ternly's own response cache (ADR 029)",
	"internal/eval/record.go load":                       "capability records",
	"internal/graph/build.go newBuilderFrom":             "compiler export data in GOCACHE",
	"internal/graph/deps.go loadDeps":                    "dependency graph cache",
	"internal/graph/foreign.go newForeign":               "tag cache",
	"internal/graph/mem.go availableMemory":              "/proc/meminfo",
	"internal/graph/store.go lockWait":                   "graph lock",
	"internal/graph/store.go Service.load":               "graph shards",
	"internal/graph/store.go Service.save":               "graph shards",
	"internal/graph/store.go hasGo":                      "names only",
	"internal/logstore/logstore.go Open":                 "session and memory logs",
	"internal/logstore/shared.go Log.Tail":               "session and memory logs",
	"internal/logstore/shared.go Log.write":              "session and memory logs",
	"internal/logstore/shared.go OpenShared":             "session and memory logs",
	"internal/logstore/shared.go rewrite":                "session and memory logs",
	"internal/session/store.go lockFile":                 "session lock",
	"internal/session/store.go OpenProject":              "session store",
	"internal/session/store.go Project.List":             "session store",
	"internal/session/store.go Project.Records":          "session store",
	"internal/plugins/store.go OpenStore":                "plugin store index",
	"internal/plugins/marketplace.go Store.Marketplaces": "plugin store index",
	"internal/tui/commands.go Model.cmdEditor":           "the temp file the user's editor wrote",

	// the tool layer: already confined (os.Root)
	"internal/tools/tools.go Registry.builtin": "fs.WalkDir over the registry's os.Root",
	"internal/tools/tools.go Registry.grep":    "fs.WalkDir over the registry's os.Root",
	"internal/tui/commands.go Model.cmdAdd":    "glob lists names; files are read through Registry.ReadFile",

	// walks that only list names (symlinks aren't followed); contents go through rootfs
	"internal/graph/build.go syntaxGraph":             "names; contents via readSource",
	"internal/graph/foreign.go foreign.update":        "names; contents via readSource",
	"internal/graph/foreign.go HasSources":            "names only",
	"internal/graph/store.go Service.walk":            "names; contents via readSource",
	"internal/graph/watch_linux.go watcher.add":       "directories to watch",
	"internal/plugins/runtime.go Runtime.fingerprint": "names and sizes, for change detection",
	"internal/plugins/runtime.go skillFiles":          "names of a skill's files",

	// copies ternly made itself, or the user's own installs elsewhere
	"internal/plugins/store.go copyTree":                   "copies a local plugin; non-regular files (links) are skipped",
	"internal/plugins/store.go SurfaceOf":                  "hashes ternly's installed copy (no links: copied or git core.symlinks=false)",
	"internal/plugins/marketplace.go Store.AddMarketplace": "ternly's fetched copy (git core.symlinks=false)",
	"internal/plugins/discover.go CodexMCP":                "the user's own ~/.codex/config.toml",
	"internal/plugins/discover.go FoundElsewhere":          "names of the user's own installs for other harnesses",
	"internal/memory/memory.go gitHead":                    "git internals (worktrees point elsewhere); only a hex commit id is kept (headCommit)",

	// the eval's own throwaway workspaces and logs
	"eval_cmd.go trapRecords":             "the trap's session logs",
	"internal/eval/eval.go Result.file":   "the trap's own workspace, judged afterwards",
	"internal/eval/eval.go Result.grepWS": "the trap's own workspace, judged afterwards",
}

// TestOnlyConfinedReads fails when non-test code reads the filesystem
// directly from a function not in allowed.
func TestOnlyConfinedReads(t *testing.T) {
	root, _ := filepath.Abs("../..")
	var found []string
	used := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "bench" || d.Name() == "testdata" || p == filepath.Join(root, "internal", "rootfs")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			key := rel + " " + funcName(fn)
			ast.Inspect(fn, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
					if x, ok := sel.X.(*ast.Ident); ok && direct[x.Name+"."+sel.Sel.Name] {
						if _, ok := allowed[key]; ok {
							used[key] = true
						} else {
							found = append(found, key+": "+x.Name+"."+sel.Sel.Name)
						}
					}
				}
				return true
			})
		}
		return nil
	})
	sort.Strings(found)
	for _, f := range found {
		t.Errorf("direct filesystem read in %s — read workspace/plugin content through rootfs, or add the function to allowed with a reason", f)
	}
	for k := range allowed {
		if !used[k] {
			t.Errorf("allowed entry %q no longer reads directly: remove it", k)
		}
	}
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	t := fn.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}
