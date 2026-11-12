package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ABM2205/werklang/internal/diag"
	"github.com/ABM2205/werklang/internal/lexer"
	"github.com/ABM2205/werklang/internal/parser"
)

type BuiltinSig struct {
	Name       string
	MinArgs    int
	MaxArgs    int // -1 for variadic
	ParamTypes []Type
	ReturnType Type
	Doc        string
}

var Builtins = map[string]*BuiltinSig{
	"sh": {
		Name:       "sh",
		MinArgs:    1,
		MaxArgs:    1,
		ParamTypes: []Type{String},
		ReturnType: Void,
		Doc:        "sh(cmd string) — Runs command via sh -c, streaming output and aborting on non-zero exit.",
	},
	"exec": {
		Name:       "exec",
		MinArgs:    2,
		MaxArgs:    2,
		ParamTypes: []Type{String, &SliceType{Elem: String}},
		ReturnType: Void,
		Doc:        "exec(prog string, args []string) — Executes binary directly without shell parsing.",
	},
	"capture": {
		Name:       "capture",
		MinArgs:    1,
		MaxArgs:    1,
		ParamTypes: []Type{String},
		ReturnType: String,
		Doc:        "capture(cmd string) -> string — Runs command via sh -c and returns trimmed stdout.",
	},
	"try_sh": {
		Name:       "try_sh",
		MinArgs:    1,
		MaxArgs:    1,
		ParamTypes: []Type{String},
		ReturnType: Int,
		Doc:        "try_sh(cmd string) -> int — Runs command via sh -c and returns its exit code without aborting.",
	},
	"rm": {
		Name:       "rm",
		MinArgs:    1,
		MaxArgs:    1,
		ParamTypes: []Type{String},
		ReturnType: Void,
		Doc:        "rm(path string) — Recursively removes a file or directory (via mage/sh.Rm).",
	},
	"mkdir": {
		Name:       "mkdir",
		MinArgs:    1,
		MaxArgs:    1,
		ParamTypes: []Type{String},
		ReturnType: Void,
		Doc:        "mkdir(path string) — Creates directory and parents (0755).",
	},
	"env": {
		Name:       "env",
		MinArgs:    1,
		MaxArgs:    2,
		ParamTypes: []Type{String, String},
		ReturnType: String,
		Doc:        "env(key string, fallback string = \"\") -> string — Reads environment variable with optional fallback.",
	},
	"glob": {
		Name:       "glob",
		MinArgs:    1,
		MaxArgs:    1,
		ParamTypes: []Type{String},
		ReturnType: &SliceType{Elem: String},
		Doc:        "glob(pattern string) -> []string — Expands file glob pattern (including **).",
	},
	"exists": {
		Name:       "exists",
		MinArgs:    1,
		MaxArgs:    1,
		ParamTypes: []Type{String},
		ReturnType: Bool,
		Doc:        "exists(path string) -> bool — Returns true if path exists on disk.",
	},
	"print": {
		Name:       "print",
		MinArgs:    1,
		MaxArgs:    -1,
		ParamTypes: []Type{Any},
		ReturnType: Void,
		Doc:        "print(val any) — Prints value(s) followed by newline.",
	},
	"fail": {
		Name:       "fail",
		MinArgs:    1,
		MaxArgs:    1,
		ParamTypes: []Type{String},
		ReturnType: Void,
		Doc:        "fail(msg string) — Stops target execution immediately with exit status 1.",
	},
}

type SymbolKind string

const (
	SymVar        SymbolKind = "var"
	SymField      SymbolKind = "field"
	SymConfig     SymbolKind = "config"
	SymTarget     SymbolKind = "target"
	SymModule     SymbolKind = "module"
	SymGoPkg      SymbolKind = "go_pkg"
	SymMagePkg    SymbolKind = "mage_pkg"
	SymGoFunc     SymbolKind = "go_func"
	SymMageTarget SymbolKind = "mage_target"
	SymBuiltin    SymbolKind = "builtin"
)

type Symbol struct {
	Name       string
	Kind       SymbolKind
	Type       Type
	IsSchema   bool // true when an imported .werk module still needs instantiation or has defaults
	Doc        string
	DefSpan    diag.Span
	GoFunc     *GoFuncSig
	MageTarget *MageTargetSig
}

type TargetInfo struct {
	Name         string
	GoFuncName   string
	Decl         *parser.TargetDecl
	Desc         string
	Deps         []string
	ParallelDeps []string
	InputsExpr   parser.Expr
	OutputsExpr  parser.Expr
	ExecStmts    []parser.Stmt
	DefSpan      diag.Span
}

// ImportHooks decouples types from concrete interop packages to avoid circular imports.
type ImportHooks struct {
	InspectPkl  func(baseDir, spec, alias, goTypePrefix string) (*ModuleType, string, error)
	InspectGo   func(workDir, spec, alias string) (*GoPackageType, error)
	InspectMage func(workDir, spec, alias string) (*MagePackageType, error)
}

type CheckOptions struct {
	WorkDir string
	Overlay map[string]string // optional in-memory file contents for LSP
	Hooks   ImportHooks
}

type CheckedProgram struct {
	Prog          *parser.Program
	SelfModule    *ModuleType
	BaseModule    *ModuleType // non-nil if `amends` is used
	WerkModules   []*ModuleType
	PklModules    []*ModuleType
	ConfigModules []*ModuleType
	GoImports     []*GoPackageType
	MageImports   []*MagePackageType
	Targets       map[string]*TargetInfo
	TargetOrder   []*TargetInfo
	TopScope      *Scope
	ExprTypes     map[parser.Expr]Type
	FieldTypes    map[*parser.FieldDecl]Type
	IdentSymbols  map[*parser.IdentExpr]*Symbol
	SelSymbols    map[*parser.SelectorExpr]*Symbol
	Diagnostics   []diag.Diagnostic
}

type Scope struct {
	parent  *Scope
	symbols map[string]*Symbol
}

func NewScope(parent *Scope) *Scope {
	return &Scope{
		parent:  parent,
		symbols: make(map[string]*Symbol),
	}
}

func (s *Scope) Define(sym *Symbol) {
	s.symbols[sym.Name] = sym
}

func (s *Scope) LookupLocal(name string) *Symbol {
	return s.symbols[name]
}

func (s *Scope) Lookup(name string) *Symbol {
	for cur := s; cur != nil; cur = cur.parent {
		if sym, ok := cur.symbols[name]; ok {
			return sym
		}
	}
	return nil
}

func (s *Scope) AllSymbols() []*Symbol {
	seen := make(map[string]bool)
	var out []*Symbol
	for cur := s; cur != nil; cur = cur.parent {
		for k, v := range cur.symbols {
			if !seen[k] {
				seen[k] = true
				out = append(out, v)
			}
		}
	}
	return out
}

type checker struct {
	opts        CheckOptions
	baseDir     string
	checked     *CheckedProgram
	loadingSet  map[string]bool
	loadedCache map[string]*CheckedProgram
	activeSibs  map[string]bool
	sibAllowed  map[string]*ModuleField
}

func CheckFile(path string, opts CheckOptions) *CheckedProgram {
	c := &checker{
		opts:        opts,
		loadingSet:  make(map[string]bool),
		loadedCache: make(map[string]*CheckedProgram),
	}
	return c.checkFileInternal(path, "")
}

func CheckSource(path, src string, opts CheckOptions) *CheckedProgram {
	if opts.Overlay == nil {
		opts.Overlay = make(map[string]string)
	}
	abs := path
	if p, err := filepath.Abs(path); err == nil {
		abs = p
	}
	opts.Overlay[abs] = src
	opts.Overlay[path] = src
	return CheckFile(abs, opts)
}

func (c *checker) readSource(path string) (string, error) {
	if c.opts.Overlay != nil {
		if src, ok := c.opts.Overlay[path]; ok {
			return src, nil
		}
		if abs, err := filepath.Abs(path); err == nil {
			if src, ok := c.opts.Overlay[abs]; ok {
				return src, nil
			}
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *checker) checkFileInternal(path, moduleAlias string) *CheckedProgram {
	absPath := path
	if p, err := filepath.Abs(path); err == nil {
		absPath = p
	}
	if cached, ok := c.loadedCache[absPath]; ok {
		return cached
	}

	src, err := c.readSource(absPath)
	if err != nil {
		cp := &CheckedProgram{
			Diagnostics: []diag.Diagnostic{{
				Span:     diag.Span{File: path},
				Severity: diag.SeverityError,
				Message:  fmt.Sprintf("cannot read %s: %v", path, err),
			}},
		}
		return cp
	}

	prog, parseDiags := parser.ParseFile(absPath, src)
	baseDir := filepath.Dir(absPath)
	workDir := c.opts.WorkDir
	if workDir == "" {
		workDir = baseDir
	}

	modName := moduleAlias
	if modName == "" {
		base := filepath.Base(absPath)
		modName = strings.TrimSuffix(base, filepath.Ext(base))
	}
	selfMod := NewModuleType(modName, "WerkMod_"+ExportGoIdent(modName), ModWerk, absPath)
	selfMod.Prog = prog

	cp := &CheckedProgram{
		Prog:         prog,
		SelfModule:   selfMod,
		Targets:      make(map[string]*TargetInfo),
		TopScope:     NewScope(nil),
		ExprTypes:    make(map[parser.Expr]Type),
		FieldTypes:   make(map[*parser.FieldDecl]Type),
		IdentSymbols: make(map[*parser.IdentExpr]*Symbol),
		SelSymbols:   make(map[*parser.SelectorExpr]*Symbol),
		Diagnostics:  append([]diag.Diagnostic{}, parseDiags...),
	}

	c.loadingSet[absPath] = true
	defer delete(c.loadingSet, absPath)

	sub := &fileChecker{
		root:    c,
		workDir: workDir,
		baseDir: baseDir,
		cp:      cp,
	}
	sub.run()
	c.loadedCache[absPath] = cp
	return cp
}

type fileChecker struct {
	root       *checker
	workDir    string
	baseDir    string
	cp         *CheckedProgram
	activeSibs map[string]bool
	sibFields  map[string]*ModuleField
}

func (fc *fileChecker) addError(span diag.Span, format string, args ...any) {
	fc.cp.Diagnostics = append(fc.cp.Diagnostics, diag.Diagnostic{
		Span:     span,
		Severity: diag.SeverityError,
		Message:  fmt.Sprintf(format, args...),
	})
}

func (fc *fileChecker) run() {
	prog := fc.cp.Prog
	scope := fc.cp.TopScope

	// Pre-populate built-ins in global scope
	for name, sig := range Builtins {
		scope.Define(&Symbol{
			Name: name,
			Kind: SymBuiltin,
			Type: sig.ReturnType,
			Doc:  sig.Doc,
		})
	}

	// 1. Resolve `amends` if present
	if prog.Amends != nil {
		fc.resolveAmends(prog.Amends, scope)
	}

	// 2. Resolve imports
	for _, imp := range prog.Imports {
		fc.resolveImport(imp, scope)
	}

	// 3. Pre-register target names so targets can reference later targets in deps
	usedGoTargetNames := make(map[string]string)
	for _, td := range prog.Targets {
		if _, exists := fc.cp.Targets[td.Name]; exists {
			fc.addError(td.NameSpan, "duplicate target %q", td.Name)
			continue
		}
		goName := ExportGoIdent(td.Name)
		if goName == "Default" {
			goName = "DefaultTarget"
		}
		if prev, clash := usedGoTargetNames[goName]; clash {
			fc.addError(td.NameSpan, "target %q collides with %q when exported to Mage (%s)", td.Name, prev, goName)
		}
		usedGoTargetNames[goName] = td.Name

		ti := &TargetInfo{
			Name:       td.Name,
			GoFuncName: goName,
			Decl:       td,
			DefSpan:    td.NameSpan,
		}
		fc.cp.Targets[td.Name] = ti
		fc.cp.TargetOrder = append(fc.cp.TargetOrder, ti)
		scope.Define(&Symbol{
			Name:    td.Name,
			Kind:    SymTarget,
			Type:    Void,
			DefSpan: td.NameSpan,
		})
	}

	// 4. Check config blocks, top-level fields, and top-level statements in source order
	fc.sibFields = fc.cp.SelfModule.FieldMap
	for _, item := range prog.Items {
		switch n := item.(type) {
		case *parser.ConfigDecl:
			fc.checkConfigDecl(n, scope)
		case *parser.FieldDecl:
			fc.checkModuleOrVarField(n, fc.cp.SelfModule, scope, true)
		case *parser.TargetDecl:
			// Checked in step 5 after all top-level configs & fields are resolved
		case parser.Stmt:
			fc.checkStmt(n, scope, false)
		}
	}
	fc.sibFields = nil

	// 5. Check target bodies & validate target DAG
	for _, ti := range fc.cp.TargetOrder {
		fc.checkTargetDecl(ti, scope)
	}
	fc.validateTargetDAG()
}

func defaultAliasFromSpec(spec string) string {
	s := spec
	for _, pfx := range []string{"go:", "mage:"} {
		s = strings.TrimPrefix(s, pfx)
	}
	if idx := strings.LastIndex(s, "@"); idx > 0 && !strings.HasPrefix(spec, "package://") {
		s = s[:idx]
	}
	if idx := strings.LastIndex(s, "#/"); idx >= 0 {
		s = s[idx+2:]
	}
	base := filepath.Base(s)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.ReplaceAll(base, "-", "_")
	if base == "" || base == "." {
		return "mod"
	}
	return base
}

func (fc *fileChecker) resolveAmends(am *parser.AmendsClause, scope *Scope) {
	spec := am.Path
	if strings.HasSuffix(spec, ".pkl") || strings.HasPrefix(spec, "package://") || strings.HasPrefix(spec, "https://") {
		if fc.root.opts.Hooks.InspectPkl == nil {
			fc.addError(am.PathSpan, "Pkl interop hook not configured")
			return
		}
		mod, _, err := fc.root.opts.Hooks.InspectPkl(fc.baseDir, spec, "base", "WerkPklBase")
		if err != nil {
			fc.addError(am.PathSpan, "%v", err)
			return
		}
		fc.cp.BaseModule = mod
		fc.cp.PklModules = append(fc.cp.PklModules, mod.Nested...)
		fc.cp.PklModules = append(fc.cp.PklModules, mod)
	} else {
		targetPath := spec
		if !filepath.IsAbs(targetPath) {
			targetPath = filepath.Join(fc.baseDir, targetPath)
		}
		abs, _ := filepath.Abs(targetPath)
		if fc.root.loadingSet[abs] {
			fc.addError(am.PathSpan, "cyclic module amends involving %q", spec)
			return
		}
		baseCP := fc.root.checkFileInternal(abs, "")
		fc.cp.Diagnostics = append(fc.cp.Diagnostics, baseCP.Diagnostics...)
		if baseCP.SelfModule == nil {
			return
		}
		fc.cp.BaseModule = baseCP.SelfModule
		fc.mergeTransitiveModules(baseCP)
	}

	// Inherit fields from BaseModule into SelfModule and top-level scope
	for _, bf := range fc.cp.BaseModule.Fields {
		cpField := *bf
		fc.cp.SelfModule.AddField(&cpField)
		scope.Define(&Symbol{
			Name:    bf.Name,
			Kind:    SymField,
			Type:    bf.Type,
			Doc:     bf.Doc,
			DefSpan: bf.DefSpan,
		})
	}
}

func (fc *fileChecker) mergeTransitiveModules(other *CheckedProgram) {
	seenWerk := make(map[string]bool)
	for _, m := range fc.cp.WerkModules {
		seenWerk[m.GoTypeName] = true
	}
	for _, m := range other.WerkModules {
		if !seenWerk[m.GoTypeName] {
			seenWerk[m.GoTypeName] = true
			fc.cp.WerkModules = append(fc.cp.WerkModules, m)
		}
	}
	if other.SelfModule != nil && !seenWerk[other.SelfModule.GoTypeName] {
		fc.cp.WerkModules = append(fc.cp.WerkModules, other.SelfModule)
	}
}

func (fc *fileChecker) resolveImport(imp *parser.ImportDecl, scope *Scope) {
	alias := imp.Alias
	if alias == "" {
		alias = defaultAliasFromSpec(imp.Path)
	}
	defSpan := imp.AliasSpan
	if defSpan.Start == (diag.Pos{}) && defSpan.End == (diag.Pos{}) {
		defSpan = imp.PathSpan
	}

	switch {
	case strings.HasPrefix(imp.Path, "go:"):
		if fc.root.opts.Hooks.InspectGo == nil {
			fc.addError(imp.PathSpan, "Go import hook not configured")
			return
		}
		goPkg, err := fc.root.opts.Hooks.InspectGo(fc.workDir, imp.Path, alias)
		if err != nil {
			fc.addError(imp.PathSpan, "%v", err)
			return
		}
		fc.cp.GoImports = append(fc.cp.GoImports, goPkg)
		scope.Define(&Symbol{
			Name:    alias,
			Kind:    SymGoPkg,
			Type:    goPkg,
			Doc:     fmt.Sprintf("Go package %q", goPkg.ImportPath),
			DefSpan: defSpan,
		})

	case strings.HasPrefix(imp.Path, "mage:"):
		if fc.root.opts.Hooks.InspectMage == nil {
			fc.addError(imp.PathSpan, "Mage import hook not configured")
			return
		}
		magePkg, err := fc.root.opts.Hooks.InspectMage(fc.workDir, imp.Path, alias)
		if err != nil {
			fc.addError(imp.PathSpan, "%v", err)
			return
		}
		fc.cp.MageImports = append(fc.cp.MageImports, magePkg)
		scope.Define(&Symbol{
			Name:    alias,
			Kind:    SymMagePkg,
			Type:    magePkg,
			Doc:     fmt.Sprintf("Mage target package %q", magePkg.ImportPath),
			DefSpan: defSpan,
		})

	case strings.HasSuffix(imp.Path, ".pkl") || strings.HasPrefix(imp.Path, "package://") || strings.HasPrefix(imp.Path, "https://"):
		if fc.root.opts.Hooks.InspectPkl == nil {
			fc.addError(imp.PathSpan, "Pkl import hook not configured")
			return
		}
		goPrefix := "WerkPkl_" + ExportGoIdent(alias)
		mod, resolvedPath, err := fc.root.opts.Hooks.InspectPkl(fc.baseDir, imp.Path, alias, goPrefix)
		if err != nil {
			fc.addError(imp.PathSpan, "%v", err)
			return
		}
		fc.cp.PklModules = append(fc.cp.PklModules, mod.Nested...)
		fc.cp.PklModules = append(fc.cp.PklModules, mod)
		scope.Define(&Symbol{
			Name:     alias,
			Kind:     SymModule,
			Type:     mod,
			IsSchema: false, // Pkl modules evaluate directly into a populated value AND can be amended!
			Doc:      fmt.Sprintf("Pkl module %s", imp.Path),
			DefSpan:  diag.Span{File: resolvedPath},
		})

	default:
		// .werk module import
		targetPath := imp.Path
		if !filepath.IsAbs(targetPath) {
			targetPath = filepath.Join(fc.baseDir, targetPath)
		}
		abs, _ := filepath.Abs(targetPath)
		if fc.root.loadingSet[abs] {
			fc.addError(imp.PathSpan, "cyclic module import involving %q", imp.Path)
			return
		}
		subCP := fc.root.checkFileInternal(abs, alias)
		for _, d := range subCP.Diagnostics {
			if d.Severity == diag.SeverityError {
				fc.addError(imp.PathSpan, "in imported module %q: %s", imp.Path, d.Message)
				break
			}
		}
		if subCP.SelfModule == nil {
			return
		}
		fc.mergeTransitiveModules(subCP)
		hasRequired := false
		for _, f := range subCP.SelfModule.Fields {
			if f.Required {
				hasRequired = true
				break
			}
		}
		scope.Define(&Symbol{
			Name:     alias,
			Kind:     SymModule,
			Type:     subCP.SelfModule,
			IsSchema: hasRequired,
			Doc:      fmt.Sprintf("Werklang module %s", imp.Path),
			DefSpan:  diag.Span{File: abs},
		})
	}
}

func (fc *fileChecker) checkConfigDecl(cfg *parser.ConfigDecl, scope *Scope) {
	mod := NewModuleType(cfg.Name, "WerkCfg_"+ExportGoIdent(cfg.Name), ModConfig, fc.cp.Prog.Path)
	cfgScope := NewScope(scope)

	prevSibs := fc.sibFields
	fc.sibFields = mod.FieldMap
	for _, fd := range cfg.Fields {
		if fd.Value == nil {
			fc.addError(fd.Span, "config field %q must have a value", fd.Name)
		}
		fc.checkModuleOrVarField(fd, mod, cfgScope, true)
	}
	fc.sibFields = prevSibs

	fc.cp.ConfigModules = append(fc.cp.ConfigModules, mod)
	scope.Define(&Symbol{
		Name:    cfg.Name,
		Kind:    SymConfig,
		Type:    mod,
		Doc:     fmt.Sprintf("config %s", cfg.Name),
		DefSpan: cfg.NameSpan,
	})
}

func (fc *fileChecker) checkModuleOrVarField(fd *parser.FieldDecl, mod *ModuleType, scope *Scope, isModuleLevel bool) {
	var declaredType Type
	if fd.Type != nil {
		declaredType = fc.resolveTypeRef(fd.Type, scope)
	}

	var valType Type
	sibRefs := make(map[string]bool)
	if fd.Value != nil {
		prevActive := fc.activeSibs
		fc.activeSibs = sibRefs
		valType = fc.checkExpr(fd.Value, scope)
		fc.activeSibs = prevActive
	}

	// Check if overriding an inherited/existing field
	var existing *ModuleField
	if mod != nil {
		existing = mod.FieldMap[fd.Name]
	}

	finalType := declaredType
	if finalType == nil {
		if existing != nil && fd.Type == nil {
			finalType = existing.Type
		} else if valType != nil {
			finalType = valType
		} else {
			fc.addError(fd.Span, "field %q requires either a type or a value", fd.Name)
			finalType = Invalid
		}
	}

	if valType != nil && finalType != nil && !finalType.Equals(valType) {
		fc.addError(fd.Value.NodeSpan(), "cannot assign %s to %q of type %s", valType.String(), fd.Name, finalType.String())
	}
	if !isModuleLevel && fd.Value == nil {
		fc.addError(fd.Span, "local variable %q must be initialized with '='", fd.Name)
	}

	fc.cp.FieldTypes[fd] = finalType

	if mod != nil {
		mf := &ModuleField{
			Name:        fd.Name,
			GoFieldName: ExportGoIdent(fd.Name),
			Type:        finalType,
			Required:    fd.Value == nil && (existing == nil || existing.Required),
			Derived:     len(sibRefs) > 0,
			DefaultExpr: fd.Value,
			Siblings:    sibRefs,
			DefSpan:     fd.NameSpan,
		}
		if existing != nil {
			mf.GoFieldName = existing.GoFieldName
		}
		mod.AddField(mf)
	}

	kind := SymVar
	if isModuleLevel {
		kind = SymField
	}
	scope.Define(&Symbol{
		Name:    fd.Name,
		Kind:    kind,
		Type:    finalType,
		DefSpan: fd.NameSpan,
	})
}

func (fc *fileChecker) resolveTypeRef(tr parser.TypeRef, scope *Scope) Type {
	switch t := tr.(type) {
	case *parser.PrimitiveType:
		switch t.Kind {
		case "int":
			return Int
		case "float":
			return Float
		case "string":
			return String
		case "bool":
			return Bool
		}
	case *parser.SliceType:
		return &SliceType{Elem: fc.resolveTypeRef(t.Elem, scope)}
	case *parser.NamedType:
		if t.Package == "" {
			sym := scope.Lookup(t.Name)
			if sym != nil {
				if mt, ok := sym.Type.(*ModuleType); ok {
					return mt
				}
			}
			fc.addError(t.Span, "unknown module type %q", t.Name)
			return Invalid
		}
	}
	fc.addError(tr.NodeSpan(), "unsupported type")
	return Invalid
}

func (fc *fileChecker) checkTargetDecl(ti *TargetInfo, parentScope *Scope) {
	tScope := NewScope(parentScope)
	for _, st := range ti.Decl.Body {
		if fd, ok := st.(*parser.FieldDecl); ok && fd.Type == nil {
			switch fd.Name {
			case "desc":
				vt := fc.checkExpr(fd.Value, tScope)
				if !String.Equals(vt) {
					fc.addError(fd.Span, "target 'desc' must be a string, got %s", vt.String())
				}
				if sl, ok := fd.Value.(*parser.StringLit); ok {
					ti.Desc = sl.Value
				} else {
					ti.Desc = "Target " + ti.Name
				}
				fc.cp.FieldTypes[fd] = String
				continue
			case "deps":
				ti.Deps = fc.checkTargetDepList(fd, tScope)
				fc.cp.FieldTypes[fd] = &SliceType{Elem: String}
				continue
			case "parallel_deps":
				ti.ParallelDeps = fc.checkTargetDepList(fd, tScope)
				fc.cp.FieldTypes[fd] = &SliceType{Elem: String}
				continue
			case "inputs":
				vt := fc.checkExpr(fd.Value, tScope)
				strSlice := &SliceType{Elem: String}
				if !strSlice.Equals(vt) {
					fc.addError(fd.Span, "target 'inputs' must be []string, got %s", vt.String())
				}
				ti.InputsExpr = fd.Value
				fc.cp.FieldTypes[fd] = strSlice
				continue
			case "outputs":
				vt := fc.checkExpr(fd.Value, tScope)
				strSlice := &SliceType{Elem: String}
				if !strSlice.Equals(vt) {
					fc.addError(fd.Span, "target 'outputs' must be []string, got %s", vt.String())
				}
				ti.OutputsExpr = fd.Value
				fc.cp.FieldTypes[fd] = strSlice
				continue
			}
		}
		fc.checkStmt(st, tScope, true)
		ti.ExecStmts = append(ti.ExecStmts, st)
	}
}

func (fc *fileChecker) checkTargetDepList(fd *parser.FieldDecl, scope *Scope) []string {
	sl, ok := fd.Value.(*parser.SliceLit)
	if !ok {
		fc.addError(fd.Span, "%q must be a static list of target name strings (e.g. [\"test\"])", fd.Name)
		return nil
	}
	var names []string
	for _, el := range sl.Elems {
		strLit, ok := el.(*parser.StringLit)
		if !ok {
			fc.addError(el.NodeSpan(), "target dependency must be a string literal")
			continue
		}
		fc.cp.ExprTypes[el] = String
		depName := strLit.Value
		if !fc.isValidTargetRef(depName) {
			fc.addError(strLit.Span, "unknown target dependency %q", depName)
		}
		names = append(names, depName)
	}
	fc.cp.ExprTypes[sl] = &SliceType{Elem: String}
	return names
}

func (fc *fileChecker) isValidTargetRef(dep string) bool {
	if _, ok := fc.cp.Targets[dep]; ok {
		return true
	}
	if idx := strings.IndexByte(dep, ':'); idx > 0 {
		ns := dep[:idx]
		tName := dep[idx+1:]
		for _, mp := range fc.cp.MageImports {
			if mp.Alias == ns {
				if _, ok := mp.Targets[strings.ToLower(tName)]; ok {
					return true
				}
				if _, ok := mp.Targets[tName]; ok {
					return true
				}
			}
		}
	}
	return false
}

func (fc *fileChecker) validateTargetDAG() {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[string]int)
	var dfs func(name string, chain []string)
	dfs = func(name string, chain []string) {
		state[name] = visiting
		ti, ok := fc.cp.Targets[name]
		if !ok {
			state[name] = visited
			return
		}
		allDeps := append([]string{}, ti.Deps...)
		allDeps = append(allDeps, ti.ParallelDeps...)
		for _, dep := range allDeps {
			if _, isLocal := fc.cp.Targets[dep]; !isLocal {
				continue
			}
			if state[dep] == visiting {
				cycle := append(chain, dep)
				fc.addError(ti.DefSpan, "dependency cycle detected: %s", strings.Join(cycle, " -> "))
				continue
			}
			if state[dep] == unvisited {
				dfs(dep, append(chain, dep))
			}
		}
		state[name] = visited
	}

	for _, ti := range fc.cp.TargetOrder {
		if state[ti.Name] == unvisited {
			dfs(ti.Name, []string{ti.Name})
		}
	}
}

func (fc *fileChecker) checkStmt(st parser.Stmt, scope *Scope, inTarget bool) {
	switch s := st.(type) {
	case *parser.FieldDecl:
		fc.checkModuleOrVarField(s, nil, scope, false)
	case *parser.IfStmt:
		ct := fc.checkExpr(s.Cond, scope)
		if !Bool.Equals(ct) {
			fc.addError(s.Cond.NodeSpan(), "if condition must be bool, got %s", ct.String())
		}
		thenScope := NewScope(scope)
		for _, child := range s.Then {
			fc.checkStmt(child, thenScope, inTarget)
		}
		if len(s.Else) > 0 {
			elseScope := NewScope(scope)
			for _, child := range s.Else {
				fc.checkStmt(child, elseScope, inTarget)
			}
		}
	case *parser.ForStmt:
		itType := fc.checkExpr(s.Iterable, scope)
		var elemType Type = Invalid
		if sl, ok := itType.(*SliceType); ok {
			elemType = sl.Elem
		} else if itType != Invalid {
			fc.addError(s.Iterable.NodeSpan(), "for-in requires a slice ([]T), got %s", itType.String())
		}
		loopScope := NewScope(scope)
		if s.IndexVar != "" {
			loopScope.Define(&Symbol{
				Name:    s.IndexVar,
				Kind:    SymVar,
				Type:    Int,
				DefSpan: s.IndexSpan,
			})
		}
		loopScope.Define(&Symbol{
			Name:    s.ItemVar,
			Kind:    SymVar,
			Type:    elemType,
			DefSpan: s.ItemSpan,
		})
		for _, child := range s.Body {
			fc.checkStmt(child, loopScope, inTarget)
		}
	case *parser.ExprStmt:
		fc.checkExpr(s.Expr, scope)
	}
}

func (fc *fileChecker) checkExpr(e parser.Expr, scope *Scope) (res Type) {
	if e == nil {
		return Invalid
	}
	defer func() {
		if res == nil {
			res = Invalid
		}
		fc.cp.ExprTypes[e] = res
	}()

	switch x := e.(type) {
	case *parser.IntLit:
		return Int
	case *parser.FloatLit:
		return Float
	case *parser.BoolLit:
		return Bool
	case *parser.StringLit:
		return String
	case *parser.InterpStringExpr:
		for _, sub := range x.Exprs {
			t := fc.checkExpr(sub, scope)
			if t == Void {
				fc.addError(sub.NodeSpan(), "cannot interpolate void expression into string")
			}
		}
		return String

	case *parser.IdentExpr:
		if x.Name == "" {
			return Invalid
		}
		sym := scope.Lookup(x.Name)
		if sym == nil {
			fc.addError(x.Span, "undefined identifier %q", x.Name)
			return Invalid
		}
		fc.cp.IdentSymbols[x] = sym
		if fc.activeSibs != nil && fc.sibFields != nil {
			if _, isSib := fc.sibFields[x.Name]; isSib {
				fc.activeSibs[x.Name] = true
			}
		}
		return sym.Type

	case *parser.SliceLit:
		if len(x.Elems) == 0 {
			return &SliceType{Elem: Any}
		}
		first := fc.checkExpr(x.Elems[0], scope)
		for i := 1; i < len(x.Elems); i++ {
			et := fc.checkExpr(x.Elems[i], scope)
			if !first.Equals(et) {
				fc.addError(x.Elems[i].NodeSpan(), "slice element type mismatch: expected %s, got %s", first.String(), et.String())
			}
		}
		return &SliceType{Elem: first}

	case *parser.IfExpr:
		ct := fc.checkExpr(x.Cond, scope)
		if !Bool.Equals(ct) {
			fc.addError(x.Cond.NodeSpan(), "if condition must be bool, got %s", ct.String())
		}
		tt := fc.checkExpr(x.Then, scope)
		et := fc.checkExpr(x.Else, scope)
		if !tt.Equals(et) {
			fc.addError(x.Span, "if-then-else branches must have matching types: %s vs %s", tt.String(), et.String())
		}
		return tt

	case *parser.UnaryExpr:
		ot := fc.checkExpr(x.Operand, scope)
		switch x.Op {
		case lexer.TokBang:
			if !Bool.Equals(ot) {
				fc.addError(x.Span, "operator '!' requires bool, got %s", ot.String())
			}
			return Bool
		case lexer.TokMinus:
			if ot != Int && ot != Float && ot != Invalid {
				fc.addError(x.Span, "unary '-' requires int or float, got %s", ot.String())
			}
			return ot
		}
		return Invalid

	case *parser.BinaryExpr:
		lt := fc.checkExpr(x.Left, scope)
		rt := fc.checkExpr(x.Right, scope)
		return fc.checkBinaryOp(x.Span, lt, x.Op, rt)

	case *parser.SelectorExpr:
		baseType := fc.checkExpr(x.X, scope)
		if x.Sel == "" {
			return Invalid
		}
		switch bt := baseType.(type) {
		case *ModuleType:
			if id, ok := x.X.(*parser.IdentExpr); ok {
				if sym := fc.cp.IdentSymbols[id]; sym != nil && sym.IsSchema {
					fc.addError(x.Span, "module %q has required fields and must be instantiated with '%s { ... }' before accessing fields", bt.Name, id.Name)
				}
			}
			mf, ok := bt.FieldMap[x.Sel]
			if !ok {
				fc.addError(x.SelSpan, "%s has no field %q", bt.Name, x.Sel)
				return Invalid
			}
			if mf.Type == Invalid {
				fc.addError(x.SelSpan, "field %q on %s has an unsupported type in Werklang", x.Sel, bt.Name)
				return Invalid
			}
			fc.cp.SelSymbols[x] = &Symbol{
				Name:    mf.Name,
				Kind:    SymField,
				Type:    mf.Type,
				Doc:     mf.Doc,
				DefSpan: mf.DefSpan,
			}
			return mf.Type

		case *GoPackageType:
			if fn, ok := bt.Funcs[x.Sel]; ok {
				fc.cp.SelSymbols[x] = &Symbol{
					Name:    fn.Name,
					Kind:    SymGoFunc,
					Type:    fn.ResultWerkType,
					Doc:     fn.SignatureString() + "\n\n" + fn.Doc,
					DefSpan: fn.DefSpan,
					GoFunc:  fn,
				}
				return fn.ResultWerkType
			}
			if vr, ok := bt.Vars[x.Sel]; ok {
				fc.cp.SelSymbols[x] = &Symbol{
					Name:    vr.Name,
					Kind:    SymVar,
					Type:    vr.WerkType,
					Doc:     vr.Doc,
					DefSpan: vr.DefSpan,
				}
				return vr.WerkType
			}
			fc.addError(x.SelSpan, "Go package %q has no exported symbol %q (or its signature uses unsupported types)", bt.ImportPath, x.Sel)
			return Invalid

		case *MagePackageType:
			tgt, ok := bt.Targets[x.Sel]
			if !ok {
				tgt, ok = bt.Targets[strings.ToLower(x.Sel)]
			}
			if !ok {
				fc.addError(x.SelSpan, "Mage package %q has no target %q", bt.ImportPath, x.Sel)
				return Invalid
			}
			fc.cp.SelSymbols[x] = &Symbol{
				Name:       tgt.Name,
				Kind:       SymMageTarget,
				Type:       Void,
				Doc:        tgt.Doc,
				DefSpan:    tgt.DefSpan,
				MageTarget: tgt,
			}
			return Void

		default:
			if baseType != Invalid {
				fc.addError(x.SelSpan, "cannot select .%s on %s", x.Sel, baseType.String())
			}
			return Invalid
		}

	case *parser.IndexExpr:
		bt := fc.checkExpr(x.X, scope)
		it := fc.checkExpr(x.Index, scope)
		if !Int.Equals(it) {
			fc.addError(x.Index.NodeSpan(), "slice index must be int, got %s", it.String())
		}
		if sl, ok := bt.(*SliceType); ok {
			return sl.Elem
		}
		if bt != Invalid {
			fc.addError(x.Span, "cannot index type %s", bt.String())
		}
		return Invalid

	case *parser.CallExpr:
		return fc.checkCallExpr(x, scope)

	case *parser.StructAmendExpr:
		return fc.checkStructAmendExpr(x, scope)
	}

	return Invalid
}

func (fc *fileChecker) checkBinaryOp(span diag.Span, lt Type, op lexer.TokenType, rt Type) Type {
	if lt == Invalid || rt == Invalid {
		return Invalid
	}
	switch op {
	case lexer.TokPlus:
		if lt == Int && rt == Int {
			return Int
		}
		if lt == Float && rt == Float {
			return Float
		}
		if lt == String && rt == String {
			return String
		}
		if _, ok := lt.(*SliceType); ok && lt.Equals(rt) {
			if sl, _ := lt.(*SliceType); sl.Elem == Any {
				return rt
			}
			return lt
		}
		fc.addError(span, "operator '+' not defined on %s and %s", lt.String(), rt.String())
		return Invalid

	case lexer.TokMinus, lexer.TokStar, lexer.TokSlash:
		if lt == Int && rt == Int {
			return Int
		}
		if lt == Float && rt == Float {
			return Float
		}
		fc.addError(span, "operator %q requires matching int or float operands, got %s and %s", op, lt.String(), rt.String())
		return Invalid

	case lexer.TokPercent:
		if lt == Int && rt == Int {
			return Int
		}
		fc.addError(span, "operator '%%' requires int operands, got %s and %s", lt.String(), rt.String())
		return Invalid

	case lexer.TokLt, lexer.TokLte, lexer.TokGt, lexer.TokGte:
		if (lt == Int && rt == Int) || (lt == Float && rt == Float) || (lt == String && rt == String) {
			return Bool
		}
		fc.addError(span, "comparison %q requires matching int, float, or string operands, got %s and %s", op, lt.String(), rt.String())
		return Invalid

	case lexer.TokEq, lexer.TokNeq:
		if !lt.Equals(rt) {
			fc.addError(span, "cannot compare %s and %s", lt.String(), rt.String())
		}
		return Bool

	case lexer.TokAnd, lexer.TokOr:
		if !Bool.Equals(lt) || !Bool.Equals(rt) {
			fc.addError(span, "logical operator %q requires bool operands, got %s and %s", op, lt.String(), rt.String())
		}
		return Bool
	}
	return Invalid
}

func (fc *fileChecker) checkCallExpr(call *parser.CallExpr, scope *Scope) Type {
	// 1. Built-in function call?
	if id, ok := call.Callee.(*parser.IdentExpr); ok {
		if sig, isBuiltin := Builtins[id.Name]; isBuiltin {
			fc.cp.IdentSymbols[id] = scope.Lookup(id.Name)
			fc.cp.ExprTypes[id] = sig.ReturnType
			for i, arg := range call.Args {
				at := fc.checkExpr(arg, scope)
				if i < len(sig.ParamTypes) && !sig.ParamTypes[i].Equals(at) {
					fc.addError(arg.NodeSpan(), "argument %d to %s: expected %s, got %s", i+1, sig.Name, sig.ParamTypes[i].String(), at.String())
				}
			}
			if len(call.Args) < sig.MinArgs || (sig.MaxArgs >= 0 && len(call.Args) > sig.MaxArgs) {
				fc.addError(call.Span, "%s expects %d..%d arguments, got %d", sig.Name, sig.MinArgs, sig.MaxArgs, len(call.Args))
			}
			return sig.ReturnType
		}
	}

	// 2. Imported Go function or Mage target call?
	if sel, ok := call.Callee.(*parser.SelectorExpr); ok {
		_ = fc.checkExpr(sel, scope)
		sym := fc.cp.SelSymbols[sel]
		if sym == nil {
			for _, arg := range call.Args {
				fc.checkExpr(arg, scope)
			}
			return Invalid
		}
		if sym.GoFunc != nil {
			fn := sym.GoFunc
			fc.checkGoCallArgs(call, fn.Name, fn.Params, scope)
			return fn.ResultWerkType
		}
		if sym.MageTarget != nil {
			mt := sym.MageTarget
			fc.checkGoCallArgs(call, mt.Name, mt.Params, scope)
			return Void
		}
	}

	_ = fc.checkExpr(call.Callee, scope)
	for _, arg := range call.Args {
		fc.checkExpr(arg, scope)
	}
	fc.addError(call.Callee.NodeSpan(), "expression is not callable (Werklang has no user-defined functions; use built-ins, Go imports, or targets)")
	return Invalid
}

func (fc *fileChecker) checkGoCallArgs(call *parser.CallExpr, name string, params []GoParam, scope *Scope) {
	isVariadic := len(params) > 0 && params[len(params)-1].Variadic
	minArgs := len(params)
	if isVariadic {
		minArgs = len(params) - 1
	}
	if len(call.Args) < minArgs || (!isVariadic && len(call.Args) > len(params)) {
		fc.addError(call.Span, "%s expects %d arguments, got %d", name, len(params), len(call.Args))
	}
	for i, arg := range call.Args {
		at := fc.checkExpr(arg, scope)
		var expected Type = Any
		if i < len(params) {
			p := params[i]
			if p.Variadic {
				if sl, ok := p.WerkType.(*SliceType); ok {
					expected = sl.Elem
				}
			} else {
				expected = p.WerkType
			}
		} else if isVariadic {
			if sl, ok := params[len(params)-1].WerkType.(*SliceType); ok {
				expected = sl.Elem
			}
		}
		if !expected.Equals(at) {
			fc.addError(arg.NodeSpan(), "argument %d to %s: expected %s, got %s", i+1, name, expected.String(), at.String())
		}
	}
}

func (fc *fileChecker) checkStructAmendExpr(sa *parser.StructAmendExpr, scope *Scope) Type {
	baseType := fc.checkExpr(sa.Base, scope)
	mod, ok := baseType.(*ModuleType)
	if !ok {
		if baseType != Invalid {
			fc.addError(sa.Base.NodeSpan(), "cannot amend non-module type %s", baseType.String())
		}
		for _, fd := range sa.Fields {
			if fd.Value != nil {
				fc.checkExpr(fd.Value, scope)
			}
		}
		return Invalid
	}

	isSchema := false
	if id, ok := sa.Base.(*parser.IdentExpr); ok {
		if sym := fc.cp.IdentSymbols[id]; sym != nil && sym.IsSchema {
			isSchema = true
		}
	}

	provided := make(map[string]bool)
	for _, fd := range sa.Fields {
		if provided[fd.Name] {
			fc.addError(fd.NameSpan, "duplicate field %q in %s block", fd.Name, mod.Name)
		}
		provided[fd.Name] = true
		mf, exists := mod.FieldMap[fd.Name]
		vt := fc.checkExpr(fd.Value, scope)
		if !exists {
			fc.addError(fd.NameSpan, "module %s has no field %q", mod.Name, fd.Name)
			continue
		}
		fc.cp.FieldTypes[fd] = mf.Type
		if !mf.Type.Equals(vt) {
			fc.addError(fd.Value.NodeSpan(), "field %q on %s expects %s, got %s", fd.Name, mod.Name, mf.Type.String(), vt.String())
		}
	}

	if isSchema {
		for _, mf := range mod.Fields {
			if mf.Required && !provided[mf.Name] {
				fc.addError(sa.Span, "missing required field %q (%s) when instantiating %s", mf.Name, mf.Type.String(), mod.Name)
			}
		}
	}

	return mod
}
