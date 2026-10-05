package graph

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/odvcencio/gotreesitter"
)

// Languages other than Go (ADR 012): Python, TypeScript/TSX, JavaScript,
// Rust and Java, parsed with gotreesitter (a pure-Go tree-sitter runtime) and
// its tags queries. Definitions keep their spans; calls are resolved by name
// — same file, then same directory, then a unique match — and marked
// approximate when several symbols share the name. Each directory and
// language is one package; files are re-tagged only when their content hash
// changes, and the tags are cached under the project's graph directory.

var foreignExt = map[string]string{
	".py": "python", ".ts": "typescript", ".tsx": "tsx", ".js": "javascript", ".jsx": "javascript",
	".mjs": "javascript", ".cjs": "javascript", ".rs": "rust", ".java": "java",
}

const (
	maxForeignFiles = 20000
	maxForeignSize  = 1 << 20    // larger files (bundles, generated code) aren't parsed
	foreignTimeout  = 10_000_000 // µs per parse: a pathological file can't stall the graph (2 s truncated large TS files under load)
)

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "target": true, "dist": true, "build": true,
	".venv": true, "venv": true, "__pycache__": true, ".next": true, ".gradle": true, "out": true, ".tox": true, "site-packages": true}

// fdef is a definition; fcall a call site. Offsets are bytes.
type fdef struct {
	Name, Kind  string
	Line, Col   int32
	End         int32
	Start, Stop uint32
}

type fcall struct {
	Name      string
	Line, Col int32
	At        uint32
}

type fileTags struct {
	Hash    string
	Size    int64
	MTime   int64
	Lang    string
	Defs    []fdef
	Calls   []fcall
	Imports []string // module paths this file imports, as slash paths ("app/store", "com/google/common/base")
}

// foreign is the non-Go part of a workspace graph.
type foreign struct {
	root  string
	file  string // cache
	files map[string]*fileTags
	ver   int // bumped on every change
}

func newForeign(root, cache string) *foreign {
	f := &foreign{root: root, file: cache, files: map[string]*fileTags{}}
	if b, err := os.ReadFile(cache); err == nil {
		_ = json.Unmarshal(b, &f.files)
	}
	return f
}

// update re-tags changed files and drops deleted ones; it reports whether
// anything changed.
func (f *foreign) update(ctx context.Context, workers int) bool {
	type job struct {
		rel, lang string
		fi        os.FileInfo
	}
	var jobs []job
	seen := map[string]bool{}
	n := 0
	_ = filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if d.IsDir() {
			if p != f.root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		lang := foreignExt[filepath.Ext(p)]
		if lang == "" && extraExt != nil && filepath.Ext(p) != ".go" {
			lang = extraExt(p)
		}
		if lang == "" || strings.HasSuffix(p, ".min.js") || strings.HasSuffix(p, ".d.ts") {
			return nil
		}
		if n++; n > maxForeignFiles {
			return filepath.SkipAll
		}
		fi, err := d.Info()
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxForeignSize {
			return nil
		}
		rel, _ := filepath.Rel(f.root, p)
		seen[rel] = true
		if old := f.files[rel]; old != nil && old.Size == fi.Size() && old.MTime == fi.ModTime().UnixNano() {
			return nil
		}
		jobs = append(jobs, job{rel, lang, fi})
		return nil
	})
	changed := false
	for rel := range f.files {
		if !seen[rel] {
			delete(f.files, rel)
			changed = true
		}
	}
	if len(jobs) > 0 {
		out := make([]*fileTags, len(jobs))
		ch := make(chan int)
		var wg sync.WaitGroup
		for range max(workers, 1) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				taggers := map[string]*gotreesitter.Tagger{} // a tagger owns a parser: one per worker and language
				for i := range ch {
					j := jobs[i]
					b, err := readSource(f.root, filepath.Join(f.root, j.rel))
					if err != nil {
						continue
					}
					h := hashBytes(b)
					if old := f.files[j.rel]; old != nil && old.Hash == h {
						out[i] = &fileTags{Hash: h, Size: j.fi.Size(), MTime: j.fi.ModTime().UnixNano(), Lang: old.Lang, Defs: old.Defs, Calls: old.Calls, Imports: old.Imports}
						continue
					}
					out[i] = tagFile(taggers, j.lang, b)
					out[i].Imports = imports(j.lang, j.rel, b)
					out[i].Hash, out[i].Size, out[i].MTime = h, j.fi.Size(), j.fi.ModTime().UnixNano()
				}
			}()
		}
		for i := range jobs {
			ch <- i
		}
		close(ch)
		wg.Wait()
		for i, j := range jobs {
			if out[i] == nil {
				continue
			}
			if old := f.files[j.rel]; old == nil || old.Hash != out[i].Hash {
				changed = true
			}
			f.files[j.rel] = out[i]
		}
	}
	if changed {
		f.ver++
		if b, err := json.Marshal(f.files); err == nil {
			_ = os.MkdirAll(filepath.Dir(f.file), 0o700)
			_ = os.WriteFile(f.file, b, 0o600)
		}
	}
	return changed
}

var (
	rePyImport   = regexp.MustCompile(`(?m)^\s*(?:from\s+(\.*[\w.]*)\s+import|import\s+([\w.]+))`)
	reJavaImport = regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([\w.]+?)(?:\.\*)?\s*;`)
	reJSImport   = regexp.MustCompile(`(?:from\s+|require\(\s*|import\s*\(\s*|import\s+)["']([^"']+)["']`)
	reRustUse    = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?use\s+((?:crate|super|self)?(?:::\w+)+|\w+(?:::\w+)+)`)
)

// imports extracts what a file imports, as slash paths relative to the
// workspace where they can be (relative JS imports, crate paths), else as
// dotted-module paths with slashes; resolution matches them as suffixes.
func imports(lang, rel string, src []byte) []string {
	dir := filepath.ToSlash(filepath.Dir(rel))
	var out []string
	switch family(lang) {
	case "python":
		for _, m := range rePyImport.FindAllStringSubmatch(string(src), -1) {
			mod := m[1] + m[2]
			if strings.HasPrefix(mod, ".") { // relative: from .x import y
				up := len(mod) - len(strings.TrimLeft(mod, "."))
				d := dir
				for range up - 1 {
					d = filepath.ToSlash(filepath.Dir(d))
				}
				mod = strings.TrimPrefix(d+"/"+strings.TrimLeft(mod, "."), "./")
			}
			out = append(out, strings.Trim(strings.ReplaceAll(mod, ".", "/"), "/"))
		}
	case "java":
		for _, m := range reJavaImport.FindAllStringSubmatch(string(src), -1) {
			out = append(out, strings.ReplaceAll(m[1], ".", "/"))
		}
	case "js":
		for _, m := range reJSImport.FindAllStringSubmatch(string(src), -1) {
			if p := m[1]; strings.HasPrefix(p, ".") {
				out = append(out, filepath.ToSlash(filepath.Clean(filepath.Join(dir, p))))
			}
		}
	case "rust":
		for _, m := range reRustUse.FindAllStringSubmatch(string(src), -1) {
			p := m[1]
			p = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(p, "crate::"), "self::"), "super::")
			out = append(out, strings.ReplaceAll(p, "::", "/"))
		}
	}
	return out
}

func tagFile(taggers map[string]*gotreesitter.Tagger, lang string, src []byte) *fileTags {
	ft := &fileTags{Lang: lang}
	tg := taggers[lang]
	if tg == nil {
		l, q := language(lang)
		if l == nil {
			return ft
		}
		var err error
		if tg, err = gotreesitter.NewTagger(l, q, gotreesitter.WithTaggerTimeoutMicros(foreignTimeout)); err != nil {
			return ft
		}
		taggers[lang] = tg
	}
	for _, t := range tg.Tag(src) {
		kind, isDef := strings.CutPrefix(t.Kind, "definition.")
		switch {
		case isDef:
			ft.Defs = append(ft.Defs, fdef{Name: t.Name, Kind: kind, Line: int32(t.NameRange.StartPoint.Row) + 1, Col: int32(t.NameRange.StartPoint.Column) + 1,
				End: int32(t.Range.EndPoint.Row) + 1, Start: t.Range.StartByte, Stop: t.Range.EndByte})
		case t.Kind == "reference.call":
			ft.Calls = append(ft.Calls, fcall{Name: t.Name, Line: int32(t.NameRange.StartPoint.Row) + 1, Col: int32(t.NameRange.StartPoint.Column) + 1, At: t.NameRange.StartByte})
		}
	}
	return ft
}

// family groups languages that call each other.
func family(lang string) string {
	switch lang {
	case "typescript", "tsx", "javascript":
		return "js"
	}
	return lang
}

var foreignKinds = map[string]Kind{"class": KType, "struct": KType, "type": KType, "enum": KType, "trait": KInterface,
	"interface": KInterface, "function": KFunc, "method": KMethod, "module": KVar, "constant": KConst, "macro": KFunc}

func isContainer(k string) bool {
	return k == "class" || k == "interface" || k == "struct" || k == "trait" || k == "enum" || k == "module" || k == "type"
}

// packages turns the tags into graph packages: one per directory and
// language. Symbol IDs are the file path without its extension plus the
// qualified name ("app/models.User.save").
func (f *foreign) packages() []*Package {
	byPkg := map[string]*Package{}
	ids := map[string][]string{}  // language family + name → symbol IDs
	qualOf := map[string]string{} // symbol ID → its qualified name
	inFile := map[string]map[string][]string{}
	encl := map[string][]struct {
		id          string
		start, stop uint32
	}{}
	rels := make([]string, 0, len(f.files))
	for rel := range f.files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		ft := f.files[rel]
		dir := filepath.Dir(rel)
		key := ft.Lang + ":" + dir
		p := byPkg[key]
		if p == nil {
			p = &Package{Path: key, Dir: dir}
			byPkg[key] = p
		}
		p.Files = append(p.Files, FileInfo{Name: rel, Hash: ft.Hash, Size: ft.Size, MTime: ft.MTime})
		mod := strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel))
		for i, d := range ft.Defs {
			qual := d.Name // qualified by the innermost container whose span holds it
			for j := i - 1; j >= 0; j-- {
				c := ft.Defs[j]
				if isContainer(c.Kind) && c.Start <= d.Start && d.Stop <= c.Stop && j != i {
					qual = c.Name + "." + d.Name
					break
				}
			}
			kind := foreignKinds[d.Kind]
			if kind == 0 {
				kind = KFunc
			}
			if kind == KFunc && qual != d.Name {
				kind = KMethod
			}
			id := mod + "." + qual
			qualOf[id] = qual
			p.Symbols = append(p.Symbols, Symbol{ID: id, Name: d.Name, Kind: kind, Pkg: key, Pos: Pos{File: rel, Line: d.Line, Col: d.Col}, EndLine: d.End,
				Test: strings.Contains(rel, "test")})
			ids[family(ft.Lang)+" "+d.Name] = append(ids[family(ft.Lang)+" "+d.Name], id)
			if inFile[rel] == nil {
				inFile[rel] = map[string][]string{}
			}
			inFile[rel][d.Name] = append(inFile[rel][d.Name], id)
			if kind == KFunc || kind == KMethod {
				encl[rel] = append(encl[rel], struct {
					id          string
					start, stop uint32
				}{id, d.Start, d.Stop})
			}
		}
	}
	dirOf := func(mod string) string { return filepath.ToSlash(filepath.Dir(mod)) }
	// Every suffix of every module path and of its directory, so an import
	// ("app/store", "com/google/common/base") is matched by lookup.
	suffixes := map[string][]string{}
	addSuffixes := func(path string) {
		for p := path; ; {
			suffixes[p] = append(suffixes[p], path)
			i := strings.Index(p, "/")
			if i < 0 {
				return
			}
			p = p[i+1:]
		}
	}
	seenPath := map[string]bool{}
	for _, rel := range rels {
		mod := strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel))
		for _, p := range []string{mod, dirOf(mod)} {
			if !seenPath[p] {
				seenPath[p] = true
				addSuffixes(p)
			}
		}
	}
	for _, rel := range rels {
		ft := f.files[rel]
		p := byPkg[ft.Lang+":"+filepath.Dir(rel)]
		here := filepath.ToSlash(filepath.Dir(rel))
		allowed := map[string]bool{} // modules and directories this file imports
		for _, im := range ft.Imports {
			for q := im; q != ""; { // "a/b/Name" may import a symbol of module a/b
				for _, m := range suffixes[q] {
					allowed[m] = true
				}
				i := strings.LastIndex(q, "/")
				if i < 0 {
					break
				}
				q = q[:i]
			}
		}
		resolved := map[string][]string{} // name → targets, for this file
		for _, c := range ft.Calls {
			from := ""
			var best uint32 = ^uint32(0)
			for _, e := range encl[rel] { // innermost function or method around the call
				if e.start <= c.At && c.At < e.stop && e.stop-e.start < best {
					from, best = e.id, e.stop-e.start
				}
			}
			targets, ok := resolved[c.Name]
			if !ok {
				targets = inFile[rel][c.Name]
				if len(targets) == 0 {
					named := ids[family(ft.Lang)+" "+c.Name] // only the caller's language family
					var same, imported []string
					for _, id := range named {
						mod := id[:len(id)-len(qualOf[id])-1] // the defining file, without its extension
						switch d := dirOf(mod); {
						case d == here:
							same = append(same, id)
						case allowed[mod] || allowed[d]:
							imported = append(imported, id)
						}
					}
					switch {
					case len(same) > 0:
						targets = same
					case len(imported) > 0:
						targets = imported
					case len(named) == 1:
						targets = named // the only definition of this name in the language
					}
				}
				resolved[c.Name] = targets
			}
			if len(targets) > 3 {
				continue // too many candidates to be a useful edge
			}
			for _, to := range targets {
				p.Refs = append(p.Refs, Ref{From: from, To: to, Pos: Pos{File: rel, Line: c.Line, Col: c.Col}, Call: true, ByName: true, Approx: len(targets) > 1})
			}
		}
	}
	out := make([]*Package, 0, len(byPkg))
	for _, p := range byPkg {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// HasSources reports whether root holds code the graph understands: a Go
// module, or source files in another supported language near the top.
func HasSources(root string) bool {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		return true
	}
	found, n := false, 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if found {
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		if n++; n > 5000 {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if p != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") || strings.Count(strings.TrimPrefix(p, root), string(filepath.Separator)) > 3) {
				return filepath.SkipDir
			}
			return nil
		}
		found = foreignExt[filepath.Ext(p)] != ""
		return nil
	})
	return found
}
