# ADR 007 — Local code graph (Go first), dependency cache, incremental updates, graph tools

Status: accepted (M3)

## Problem
The agent finds code with grep and reads whole regions to learn who calls what. That costs tokens
on every question and still misses semantic links: method calls through interfaces, references
behind aliases. Requirement 5: build or load a graph first, keep it current incrementally, cite
`file:line`, expose it as tools, and route the agent to them before grep/read.

## Options for Go analysis
| Option | Precision | Cost | Dependency |
|---|---|---|---|
| `golang.org/x/tools/go/packages` | full types | runs `go list -export` internally | x/tools: large, tracks Go releases |
| **`go list -export -deps` + stdlib `go/parser`, `go/types`, `go/importer.ForCompiler("gc", lookup)`** | full types | same `go list`; we control parallelism and memory | **none** |
| syntax only (`go/parser`, name matching) | no method or interface resolution | cheapest | none |

**Decision: the second row**, with syntax-only as an automatic fallback when `go list` fails (no
toolchain, broken module). With export data for every dependency, each workspace package
type-checks **independently**, so all of them run in parallel on a worker pool of `GOMAXPROCS`
with no topological scheduling. An external test package (`p_test`) is checked in the same job right
after its `p`, importing the in-memory `p`.

Other languages are M7. The evaluation it needs (tree-sitter grammars compiled to WASM on wazero,
SCIP/LSIF indexers, language servers) is deferred there. M3's storage and tool layers are
language-neutral (symbols, spans, edges).

## Model
- **Nodes:** packages, files (with content hash), and symbols: funcs, methods, types, interfaces,
  struct fields, package-level vars and consts. IDs are `pkg.Name`, `pkg.Recv.Name`, `pkg.Type.Field`.
  Every symbol has a span (`file:line`–end line) and a type or signature string.
- **Edges:**
  - **imports:** package → package.
  - **ref:** a use of a symbol, recorded on the enclosing top-level declaration, with its position.
  - **call:** a ref whose use is a call's function expression. Static calls, and interface method
    calls (to the interface method). Calls through func values can't be resolved statically.
  - **test-of:** calls from `Test*`, `Benchmark*`, `Fuzz*` and `Example*` functions in `_test.go` files
    to non-test symbols, plus `foo_test.go` → `foo.go`.
- **implements** is computed at query time, not stored. Each type and interface keeps its method
  set as qualified `Name(signature)` strings, and a method-name index narrows candidates. This needs
  no live `go/types` objects after the build, costs nothing at build time, and covers dependency
  interfaces (`io.Writer`) through the dependency cache.

## Storage (shared by all sessions of a project, like the checkpoint store)
- **Project graph:** `~/.cache/ternly/graphs/projects/<project-key>/`, with one gob shard per
  package and a `manifest` mapping each package to its shard, file hashes and export hash. Shards
  are written under content-addressed names, then the manifest is swapped atomically (temp file and
  rename). A crash at any point leaves the previous graph intact. Unreferenced shards are deleted
  after the swap. A `build.lock` flock means one process builds while others wait and load.
- **Dependency graphs:** `~/.cache/ternly/graphs/go/<module>@<version>/<content-hash>/`, with symbols
  and method sets built from export data. Reused only when ecosystem, module, exact version and
  content hash (go.sum `h1:`) all match. Modules with no sum (replaced or local) hash their source
  files. The standard library is `std@<go version>`.
- Everything is under `~/.cache/ternly`, which sandboxed commands can't see (ADR 003).

## Running `go list`
`go list` compiles (cgo included) and may run a toolchain the repo asks for, so it runs in the
bubblewrap sandbox like any model-run command. It uses `GOTOOLCHAIN=local`: no silent toolchain
downloads. A version mismatch falls back to syntax-only, with a note.

## Incremental updates
1. **Change detection.**
   - On Linux, an inotify watcher marks files dirty as they change.
   - Elsewhere, or past the inotify watch limit, a stat scan runs before graph queries and at turn
     boundaries. It hashes only files whose mtime or size changed.
   - Content hashes decide what really changed (touch and checkout without changes cost nothing).
2. Changed packages are re-listed (`go list -export` for just those, recompiling only them), then
   re-parsed and re-checked. If a package's export data changed, workspace packages that import it
   are re-checked too.
3. Each re-checked package's shard is replaced and the manifest swapped (step-level transactions).
   In memory, its old symbols and edges are removed from every index and the new ones added under
   the graph lock, so a query never sees a half-patched package.

`git diff` was named in the requirement. The measurement below decides whether it adds anything over
the stat-plus-hash scan: both are complete, and git misses untracked files unless asked.

## Tools and routing
`find_symbol`, `references`, `callers`, `callees`, `implementations`, `related_files` and `impact`
(transitive reverse references and calls, plus the tests that reach them). All are read-only, framed
as untrusted output, and cite `file:line`. The system prompt tells the model to use them before
grep/read for Go code, then read only the cited lines.

## Changes made while measuring (all found by the kubernetes runs)
- **Shared importer.** Per-worker export-data importers each decoded kubernetes' whole dependency
  set: **31.7 GB peak heap**. Now there is one importer for all workers. Imports are serialised, and
  each imported package is resolved eagerly under the lock (`go/types` resolves imported objects
  lazily). Workers therefore only read fully built objects, which the race detector confirms on a
  real repo. Peak is now about 5 GB, with 600 MB resident after GC.
- **Incremental without compiling.** Re-running `go list -export` recompiled the changed package
  and everything downstream: 1 m 44 s for an API change.
  - Changed packages are now type-checked from source, against the export data recorded at the last
    full build (kept in the manifest). Importers are re-checked against the fresh in-memory package
    only when our **API hash** (exported signatures and method sets) changes. The compiler's export
    data also changes with inlinable bodies, which caused needless importer re-checks.
  - Cost: importers can mix the new package with older dependencies' view of it. On kubernetes,
    counted type errors rose from 265 to 746 after an API change. **Reference edges were identical in
    count to a fresh full build (2,119,324).** The next full build is exact.
- **Files no package owns.** 365 `.go` files in kubernetes belong to no workspace package (nested
  non-workspace modules and similar). Scans reported them as new every time, triggering needless
  updates. They are now tracked with their hashes.
- **Watcher race.** Watches were registered after the first build, so an edit right then was missed.
  Watches are now registered before building. Each refresh first drains the inotify queue
  synchronously (the kernel queues events before the changing syscall returns), so no change made
  before a query can be missed.
- **`git diff`.** A full stat scan costs the same as `git status` on kubernetes (76 ms against 77 ms)
  and sees untracked files without extra flags, so git adds nothing. With the watcher, a no-change
  refresh costs ~50 µs.

## Measurement (2026-10-04, Ryzen 7 6800H, 16 threads, NVMe ext4, go1.27.1, bubblewrap sandbox)
Kubernetes (shallow clone; Go workspace of 30 modules; 13.1k files in 3,051 workspace packages):

| | Result |
|---|---|
| Cold full build (empty Go build cache) | 3 m 33 s, of which `go list -export` compiling is 3 m 22 s (12 GB of build cache); type-checking 8.9 s |
| Warm full build | 12.0–14.1 s: `go list` ~2 s, parse and type-check 8.4–10.2 s on 16 workers, save ~1.2 s |
| Graph size | 205,218 symbols, 2,119,324 reference edges, 0 untyped packages. 485 MB on disk; ~600 MB resident; ~5 GB peak while building |
| Dependency cache | 1,113 dependency packages' tables in 728 ms (background); a second project reusing the cache: all hits |
| Load from cache | 668–753 ms |
| Refresh, nothing changed | ~50 µs (watcher); 76 ms full stat scan |
| Body-only edit (leaf package) | **111–120 ms**, 1 package re-checked (was 5.2 s with re-compiling) |
| API change (`k8s.io/apimachinery/pkg/util/sets`, 807 importing files) | **6.1–6.7 s**, 349 packages re-checked (was 1 m 50 s) |

**Token cost**, deterministic: 20 sampled symbols per question type. The graph tool's output is
compared with the cheapest sensible grep + read_file sequence, using ternly's own tools and caps.

| Question | ternly repo | kubernetes |
|---|---|---|
| where is X defined | 1.9× fewer tokens | 4.1× fewer |
| who calls X | 8.8× fewer | 12.9× fewer |
| what implements I | 5.7× fewer (n=2) | 16.2× fewer |

**Real model** (local qwen3.6, n=1 each): asked for every call site of `Agent.setModel` in
ternly, both runs found all 4 correctly. With the graph tools: 14,122 input and 909 output tokens.
Without them: 15,529 and 1,142 (−9% / −20%). The model used `find_symbol` and `callers`, then read
the call sites anyway. On a small repo with a unique name, grep is already precise. The large gains
need ambiguous names or large repos.

**Limits.**
- On first entering a large repo with a cold Go build cache, the graph needs minutes. Tools wait
  20 s, then tell the model to use grep. A syntax-only first pass would cover that window; not built.
- Building needs `GOCACHE` under a writable cache the sandbox exposes (the default `~/.cache/go-build`
  is). A `GOCACHE` in `/tmp` is invisible outside the sandbox's private `/tmp`.
- Generic types are not matched by `implementations`. Calls through function values are not edges.
