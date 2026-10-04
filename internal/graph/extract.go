package graph

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

// extractor turns parsed (and, when available, type-checked) files into a
// Package's symbols and references.
type extractor struct {
	b     *builder
	p     *Package
	owner map[*types.Var]string // struct field → ID of the type that declares it
}

func newExtractor(b *builder, p *Package) *extractor {
	return &extractor{b: b, p: p, owner: map[*types.Var]string{}}
}

func (e *extractor) check(path string, files []*ast.File, imp types.Importer) *types.Package {
	if len(files) == 0 {
		return nil
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	conf := types.Config{Importer: imp, FakeImportC: true, Error: func(error) { e.p.Errors++ }}
	tpkg, _ := conf.Check(path, e.b.fset, files, info)
	for _, f := range files {
		e.declare(f, &typed{path: path, info: info})
	}
	for _, f := range files {
		e.refs(f, path, info)
	}
	return tpkg
}

// typed carries type information for declare (nil: syntax only).
type typed struct {
	path string
	info *types.Info
}

func qual(p *types.Package) string { return p.Path() }

func (e *extractor) pos(p token.Pos) Pos {
	x := e.b.fset.Position(p)
	return Pos{File: e.b.rel(x.Filename), Line: int32(x.Line), Col: int32(x.Column)}
}

func (e *extractor) endLine(p token.Pos) int32 { return int32(e.b.fset.Position(p).Line) }

func (e *extractor) add(s Symbol) {
	s.Test = strings.HasSuffix(s.Pos.File, "_test.go")
	if len(s.Sig) > 240 {
		s.Sig = s.Sig[:240] + "…"
	}
	e.p.Symbols = append(e.p.Symbols, s)
}

// declare records the file's top-level declarations.
func (e *extractor) declare(f *ast.File, t *typed) {
	pkg := e.p.Path
	if t != nil {
		pkg = t.path
	}
	def := func(id *ast.Ident) types.Object {
		if t == nil {
			return nil
		}
		return t.info.Defs[id]
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			s := Symbol{Name: d.Name.Name, Kind: KFunc, Pkg: pkg, Pos: e.pos(d.Name.Pos()), EndLine: e.endLine(d.End())}
			s.ID = pkg + "." + d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				s.Kind = KMethod
				s.ID = pkg + "." + recvName(d.Recv.List[0].Type) + "." + d.Name.Name
			}
			if obj := def(d.Name); obj != nil {
				s.Sig = types.TypeString(obj.Type(), qual)
			}
			e.add(s)
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					e.declareType(pkg, sp, def)
				case *ast.ValueSpec:
					kind := KVar
					if d.Tok == token.CONST {
						kind = KConst
					}
					for _, n := range sp.Names {
						if n.Name == "_" {
							continue
						}
						s := Symbol{ID: pkg + "." + n.Name, Name: n.Name, Kind: kind, Pkg: pkg, Pos: e.pos(n.Pos()), EndLine: e.endLine(sp.End())}
						if obj := def(n); obj != nil {
							s.Sig = types.TypeString(obj.Type(), qual)
						}
						e.add(s)
					}
				}
			}
		}
	}
}

func (e *extractor) declareType(pkg string, sp *ast.TypeSpec, def func(*ast.Ident) types.Object) {
	s := Symbol{ID: pkg + "." + sp.Name.Name, Name: sp.Name.Name, Kind: KType, Pkg: pkg, Pos: e.pos(sp.Name.Pos()), EndLine: e.endLine(sp.End())}
	if _, ok := sp.Type.(*ast.InterfaceType); ok {
		s.Kind = KInterface
	}
	if obj, ok := def(sp.Name).(*types.TypeName); ok {
		s.Sig = types.TypeString(obj.Type().Underlying(), qual)
		s.Methods = methodSet(obj.Type())
	}
	e.add(s)
	if it, ok := sp.Type.(*ast.InterfaceType); ok { // interface methods are call targets too
		for _, m := range it.Methods.List {
			for _, n := range m.Names {
				ms := Symbol{ID: s.ID + "." + n.Name, Name: n.Name, Kind: KMethod, Pkg: pkg, Pos: e.pos(n.Pos()), EndLine: e.endLine(m.End())}
				if obj := def(n); obj != nil {
					ms.Sig = types.TypeString(obj.Type(), qual)
				}
				e.add(ms)
			}
		}
		return
	}
	st, ok := sp.Type.(*ast.StructType)
	if !ok {
		return
	}
	for _, fld := range st.Fields.List {
		names := fld.Names
		if len(names) == 0 { // embedded: the field is named after its type
			if id := embeddedIdent(fld.Type); id != nil {
				names = []*ast.Ident{id}
			}
		}
		for _, n := range names {
			fs := Symbol{ID: s.ID + "." + n.Name, Name: n.Name, Kind: KField, Pkg: pkg, Pos: e.pos(n.Pos()), EndLine: e.endLine(fld.End())}
			if v, ok := def(n).(*types.Var); ok {
				fs.Sig = types.TypeString(v.Type(), qual)
				e.owner[v] = s.ID
			}
			e.add(fs)
		}
	}
}

// methodSet lists a named type's methods as "Name(params)(results)" without
// parameter names, so interfaces and implementations compare as strings. For
// non-interfaces it is the pointer method set. Generic types are skipped.
func methodSet(t types.Type) []string {
	if n, ok := t.(*types.Named); ok && n.TypeParams().Len() > 0 {
		return nil
	}
	var out []string
	if it, ok := t.Underlying().(*types.Interface); ok {
		for i := range it.NumMethods() {
			out = append(out, methodKey(it.Method(i)))
		}
		return out
	}
	ms := types.NewMethodSet(types.NewPointer(t))
	for i := range ms.Len() {
		if f, ok := ms.At(i).Obj().(*types.Func); ok {
			out = append(out, methodKey(f))
		}
	}
	return out
}

func methodKey(f *types.Func) string {
	sig := f.Type().(*types.Signature)
	var b strings.Builder
	b.WriteString(f.Name())
	tuple := func(t *types.Tuple, variadic bool) {
		b.WriteByte('(')
		for i := range t.Len() {
			if i > 0 {
				b.WriteByte(',')
			}
			ty := t.At(i).Type()
			if variadic && i == t.Len()-1 {
				b.WriteString("...")
				ty = ty.(*types.Slice).Elem()
			}
			b.WriteString(types.TypeString(ty, qual))
		}
		b.WriteByte(')')
	}
	tuple(sig.Params(), sig.Variadic())
	tuple(sig.Results(), false)
	return b.String()
}

func recvName(x ast.Expr) string {
	for {
		switch t := x.(type) {
		case *ast.StarExpr:
			x = t.X
		case *ast.ParenExpr:
			x = t.X
		case *ast.IndexExpr:
			x = t.X
		case *ast.IndexListExpr:
			x = t.X
		case *ast.Ident:
			return t.Name
		default:
			return "?"
		}
	}
}

func embeddedIdent(x ast.Expr) *ast.Ident {
	for {
		switch t := x.(type) {
		case *ast.StarExpr:
			x = t.X
		case *ast.SelectorExpr:
			return t.Sel
		case *ast.IndexExpr:
			x = t.X
		case *ast.IndexListExpr:
			x = t.X
		case *ast.Ident:
			return t
		default:
			return nil
		}
	}
}

// refs records, for each top-level declaration, the package-level symbols,
// methods and fields it uses, and which uses are calls.
func (e *extractor) refs(f *ast.File, pkg string, info *types.Info) {
	calls := map[*ast.Ident]bool{}
	fieldOwner := map[*ast.Ident]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if id := callee(n.Fun); id != nil {
				calls[id] = true
			}
		case *ast.SelectorExpr:
			if sel := info.Selections[n]; sel != nil && sel.Kind() == types.FieldVal {
				if o := selectionOwner(sel); o != "" {
					fieldOwner[n.Sel] = o + "." + n.Sel.Name
				}
			}
		}
		return true
	})
	for _, d := range f.Decls {
		walk := func(from string, node ast.Node) {
			ast.Inspect(node, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				obj := info.Uses[id]
				if obj == nil {
					return true
				}
				to := fieldOwner[id]
				if to == "" {
					to = e.objID(obj)
				}
				if to != "" && to != from {
					e.p.Refs = append(e.p.Refs, Ref{From: from, To: to, Pos: e.pos(id.Pos()), Call: calls[id]})
				}
				return true
			})
		}
		switch d := d.(type) {
		case *ast.FuncDecl:
			from := pkg + "." + d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				from = pkg + "." + recvName(d.Recv.List[0].Type) + "." + d.Name.Name
			}
			walk(from, d)
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				from := ""
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					from = pkg + "." + sp.Name.Name
				case *ast.ValueSpec:
					if len(sp.Names) > 0 {
						from = pkg + "." + sp.Names[0].Name
					}
				}
				walk(from, sp)
			}
		}
	}
}

func callee(fn ast.Expr) *ast.Ident {
	for {
		switch t := fn.(type) {
		case *ast.ParenExpr:
			fn = t.X
		case *ast.IndexExpr: // f[T](…)
			fn = t.X
		case *ast.IndexListExpr:
			fn = t.X
		case *ast.Ident:
			return t
		case *ast.SelectorExpr:
			return t.Sel
		default:
			return nil
		}
	}
}

// objID names a used object the way declarations are named; "" for locals,
// builtins, labels and package names.
func (e *extractor) objID(obj types.Object) string {
	if obj.Pkg() == nil {
		return ""
	}
	path := obj.Pkg().Path()
	switch o := obj.(type) {
	case *types.Func:
		o = o.Origin()
		if r := o.Type().(*types.Signature).Recv(); r != nil {
			if n := named(r.Type()); n != nil {
				return path + "." + n.Obj().Name() + "." + o.Name()
			}
			return ""
		}
		if o.Parent() == o.Pkg().Scope() {
			return path + "." + o.Name()
		}
	case *types.Var:
		if o.IsField() {
			if owner := e.owner[o.Origin()]; owner != "" {
				return owner + "." + o.Name()
			}
			return "" // field of a type declared elsewhere, used without a selector (e.g. a composite-literal key)
		}
		if o.Parent() == o.Pkg().Scope() {
			return path + "." + o.Name()
		}
	case *types.Const, *types.TypeName:
		if o.Parent() == o.Pkg().Scope() {
			return path + "." + o.Name()
		}
	}
	return ""
}

func named(t types.Type) *types.Named {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, _ := types.Unalias(t).(*types.Named)
	return n
}

// selectionOwner is the ID of the named struct that declares the selected
// field, following embedded fields along the selection path.
func selectionOwner(sel *types.Selection) string {
	t := sel.Recv()
	idx := sel.Index()
	for _, k := range idx[:len(idx)-1] {
		st, ok := deref(t).Underlying().(*types.Struct)
		if !ok {
			return ""
		}
		t = st.Field(k).Type()
	}
	n := named(t)
	if n == nil || n.Obj().Pkg() == nil {
		return ""
	}
	return n.Obj().Pkg().Path() + "." + n.Obj().Name()
}

func deref(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}
