package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// jschema is the subset of JSON Schema that tool calls are checked against.
// Keywords outside it ($ref, oneOf, format, …) are ignored, never rejected, so
// an exotic MCP schema can only make validation weaker, not wrong.
type jschema struct {
	Type       json.RawMessage     `json:"type"` // "string" or ["string","null"]
	Properties map[string]*jschema `json:"properties"`
	Required   []string            `json:"required"`
	AddProps   json.RawMessage     `json:"additionalProperties"` // only `false` is enforced
	Enum       []any               `json:"enum"`
	Items      json.RawMessage     `json:"items"` // object form only; tuple form ignored
	Minimum    *float64            `json:"minimum"`
	Maximum    *float64            `json:"maximum"`
	MinLength  *int                `json:"minLength"`
	MaxLength  *int                `json:"maxLength"`
	MinItems   *int                `json:"minItems"`
	MaxItems   *int                `json:"maxItems"`

	types  []string
	items  *jschema
	closed bool
}

// compileSchema returns nil when the schema can't be understood (validation is then skipped).
func compileSchema(raw json.RawMessage) *jschema {
	var s jschema
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return nil
	}
	s.prepare()
	return &s
}

func (s *jschema) prepare() {
	var one string
	if json.Unmarshal(s.Type, &one) == nil && one != "" {
		s.types = []string{one}
	} else {
		_ = json.Unmarshal(s.Type, &s.types)
	}
	s.closed = bytes.Equal(bytes.TrimSpace(s.AddProps), []byte("false"))
	if len(s.Items) > 0 && s.Items[0] == '{' {
		var it jschema
		if json.Unmarshal(s.Items, &it) == nil {
			it.prepare()
			s.items = &it
		}
	}
	for _, p := range s.Properties {
		if p != nil {
			p.prepare()
		}
	}
}

const maxSchemaErrs = 6

// validate decodes args and returns model-readable violations (empty = valid).
// When valid args spell whole numbers as 1.0 or 1e3 (integers in JSON Schema),
// fixed holds the args with them rewritten as 1 / 1000 so Go decoding into int
// succeeds too; otherwise fixed is nil.
func (s *jschema) validate(args json.RawMessage) (errs []string, fixed json.RawMessage) {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return []string{"arguments are not valid JSON: " + err.Error()}, nil
	}
	s.check(v, "", &errs)
	if len(errs) == 0 {
		if v, changed := canonInts(v); changed {
			fixed, _ = json.Marshal(v)
		}
	}
	return errs, fixed
}

func canonInts(v any) (any, bool) {
	changed := false
	switch x := v.(type) {
	case json.Number:
		if isInt(x) && strings.ContainsAny(string(x), ".eE") {
			f, _ := x.Float64()
			if math.Abs(f) < 1<<53 {
				return json.Number(strconv.FormatInt(int64(f), 10)), true
			}
		}
	case map[string]any:
		for k, e := range x {
			var c bool
			if x[k], c = canonInts(e); c {
				changed = true
			}
		}
	case []any:
		for i, e := range x {
			var c bool
			if x[i], c = canonInts(e); c {
				changed = true
			}
		}
	}
	return v, changed
}

func (s *jschema) check(v any, path string, errs *[]string) {
	if len(*errs) >= maxSchemaErrs {
		return
	}
	add := func(format string, a ...any) {
		if len(*errs) < maxSchemaErrs {
			*errs = append(*errs, fmt.Sprintf(format, a...))
		}
	}
	at := "arguments"
	if path != "" {
		at = "property " + quote(path)
	}
	got := jsonType(v)
	if len(s.types) > 0 && !typeOK(s.types, got) {
		add("%s: expected %s, got %s%s", at, strings.Join(s.types, " or "), got, sample(v))
		return
	}
	if len(s.Enum) > 0 && !inEnum(s.Enum, v) {
		add("%s: must be one of %s", at, compact(s.Enum))
	}
	switch x := v.(type) {
	case map[string]any:
		for _, r := range s.Required {
			if _, ok := x[r]; !ok {
				add("missing required property %s", quote(join(path, r)))
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ps, ok := s.Properties[k]; ok && ps != nil {
				ps.check(x[k], join(path, k), errs)
			} else if !ok && s.closed {
				msg := "unknown property " + quote(join(path, k))
				if near := closest(k, s.propNames()); near != "" {
					msg += " (did you mean " + quote(near) + "?)"
				}
				add("%s; allowed: %s", msg, strings.Join(s.propNames(), ", "))
			}
		}
	case []any:
		if s.MinItems != nil && len(x) < *s.MinItems {
			add("%s: needs at least %d items, got %d", at, *s.MinItems, len(x))
		}
		if s.MaxItems != nil && len(x) > *s.MaxItems {
			add("%s: allows at most %d items, got %d", at, *s.MaxItems, len(x))
		}
		if s.items != nil {
			for i, it := range x {
				s.items.check(it, fmt.Sprintf("%s[%d]", path, i), errs)
			}
		}
	case string:
		n := len([]rune(x))
		if s.MinLength != nil && n < *s.MinLength {
			add("%s: must be at least %d characters", at, *s.MinLength)
		}
		if s.MaxLength != nil && n > *s.MaxLength {
			add("%s: must be at most %d characters", at, *s.MaxLength)
		}
	case json.Number:
		f, _ := x.Float64()
		if s.Minimum != nil && f < *s.Minimum {
			add("%s: must be >= %v, got %v", at, *s.Minimum, x)
		}
		if s.Maximum != nil && f > *s.Maximum {
			add("%s: must be <= %v, got %v", at, *s.Maximum, x)
		}
	}
}

func (s *jschema) propNames() []string {
	out := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func jsonType(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		if isInt(x) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func isInt(n json.Number) bool {
	f, err := n.Float64()
	return err == nil && f == math.Trunc(f) && !math.IsInf(f, 0)
}

func typeOK(want []string, got string) bool {
	for _, t := range want {
		if t == got || (t == "number" && got == "integer") {
			return true
		}
	}
	return false
}

func inEnum(enum []any, v any) bool {
	b, _ := json.Marshal(v)
	for _, e := range enum {
		if eb, _ := json.Marshal(e); bytes.Equal(b, eb) {
			return true
		}
		if n, ok := v.(json.Number); ok { // 1 vs 1.0
			if f, ok2 := e.(float64); ok2 {
				if g, err := n.Float64(); err == nil && g == f {
					return true
				}
			}
		}
	}
	return false
}

// sample shows a short preview of a mistyped value so the model sees what it sent.
func sample(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 40 {
		b = append(b[:37], "..."...)
	}
	return " (" + string(b) + ")"
}

func compact(v any) string { b, _ := json.Marshal(v); return string(b) }

func quote(s string) string { return `"` + s + `"` }

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

// closest suggests the most likely intended name: same letters ignoring case
// and separators, containment (file_path → path), or a small edit distance.
func closest(name string, cands []string) string {
	norm := func(s string) string { return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(s)) }
	n := norm(name)
	best, bestD := "", 1<<30
	for _, c := range cands {
		cn := norm(c)
		d := levenshtein(n, cn)
		switch {
		case cn == n:
			d = 0
		case strings.Contains(n, cn) || strings.Contains(cn, n) || subseq(n, cn) || subseq(cn, n):
			d = min(d, 1) // file_path → path, cmd → command
		}
		if d < bestD {
			best, bestD = c, d
		}
	}
	if bestD <= max(2, len(n)/3) {
		return best
	}
	return ""
}

// subseq reports whether a's letters appear in order in b (an abbreviation).
func subseq(a, b string) bool {
	if len(a) < 2 || len(a) >= len(b) {
		return false
	}
	i := 0
	for j := 0; j < len(b) && i < len(a); j++ {
		if a[i] == b[j] {
			i++
		}
	}
	return i == len(a)
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
