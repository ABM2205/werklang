package goinspect

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ABM2205/werklang/internal/diag"
	"github.com/ABM2205/werklang/internal/types"
	"golang.org/x/tools/go/packages"
)

type cacheKey struct {
	dir  string
	spec string
	mage bool
}

var (
	mu       sync.RWMutex
	goCache  = make(map[cacheKey]*types.GoPackageType)
	mageMap  = make(map[cacheKey]*types.MagePackageType)
)

// ParseImportSpec splits an import path with optional @version (e.g. "github.com/foo/bar@v1.2.0").
func ParseImportSpec(raw, schemePrefix string) (importPath, version string) {
	trimmed := strings.TrimPrefix(raw, schemePrefix)
	if idx := strings.LastIndex(trimmed, "@"); idx > 0 {
		return trimmed[:idx], trimmed[idx+1:]
	}
	return trimmed, ""
}

func InspectGoPackage(workDir, rawSpec, alias string) (*types.GoPackageType, error) {
	importPath, version := ParseImportSpec(rawSpec, "go:")
	key := cacheKey{dir: workDir, spec: importPath + "@" + version, mage: false}

	mu.RLock()
	if cached, ok := goCache[key]; ok {
		mu.RUnlock()
		cp := *cached
		if alias != "" {
			cp.Alias = alias
		} else {
			cp.Alias = cp.PkgName
		}
		return &cp, nil
	}
	mu.RUnlock()

	pkg, err := loadGoPackage(workDir, importPath, version, false)
	if err != nil {
		return nil, err
	}

	docs := extractDocs(pkg)
	res := &types.GoPackageType{
		ImportPath: pkg.PkgPath,
		Version:    version,
		Alias:      alias,
		PkgName:    pkg.Name,
		Funcs:      make(map[string]*types.GoFuncSig),
		Vars:       make(map[string]*types.GoVarSig),
	}
	if res.Alias == "" {
		res.Alias = pkg.Name
	}

	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		span := posToSpan(pkg.Fset, obj.Pos(), len(name))
		switch o := obj.(type) {
		case *gotypes.Func:
			sig, ok := o.Type().(*gotypes.Signature)
			if !ok || sig.Recv() != nil {
				continue
			}
			fnSig, ok := bridgeFuncSignature(name, sig, docs[name], span)
			if ok {
				res.Funcs[name] = fnSig
			}
		case *gotypes.Const:
			wt, cast, ok := bridgeGoType(o.Type(), false, 0)
			if ok {
				res.Vars[name] = &types.GoVarSig{
					Name:     name,
					WerkType: wt,
					Cast:     cast,
					IsConst:  true,
					Doc:      docs[name],
					DefSpan:  span,
				}
			}
		case *gotypes.Var:
			wt, cast, ok := bridgeGoType(o.Type(), false, 0)
			if ok {
				res.Vars[name] = &types.GoVarSig{
					Name:     name,
					WerkType: wt,
					Cast:     cast,
					IsConst:  false,
					Doc:      docs[name],
					DefSpan:  span,
				}
			}
		}
	}

	mu.Lock()
	goCache[key] = res
	mu.Unlock()

	cp := *res
	if alias != "" {
		cp.Alias = alias
	} else {
		cp.Alias = cp.PkgName
	}
	return &cp, nil
}

func InspectMagePackage(workDir, rawSpec, alias string) (*types.MagePackageType, error) {
	importPath, version := ParseImportSpec(rawSpec, "mage:")
	key := cacheKey{dir: workDir, spec: importPath + "@" + version, mage: true}

	mu.RLock()
	if cached, ok := mageMap[key]; ok {
		mu.RUnlock()
		cp := *cached
		if alias != "" {
			cp.Alias = alias
		} else {
			cp.Alias = cp.PkgName
		}
		return &cp, nil
	}
	mu.RUnlock()

	pkg, err := loadGoPackage(workDir, importPath, version, true)
	if err != nil {
		return nil, err
	}

	docs := extractDocs(pkg)
	res := &types.MagePackageType{
		ImportPath: pkg.PkgPath,
		Version:    version,
		Alias:      alias,
		PkgName:    pkg.Name,
		Targets:    make(map[string]*types.MageTargetSig),
	}
	if res.Alias == "" {
		res.Alias = pkg.Name
	}

	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch o := obj.(type) {
		case *gotypes.Func:
			sig, ok := o.Type().(*gotypes.Signature)
			if !ok || sig.Recv() != nil {
				continue
			}
			if tgt, ok := bridgeMageTarget(name, "", sig, docs[name], posToSpan(pkg.Fset, obj.Pos(), len(name))); ok {
				res.Targets[name] = tgt
				res.Targets[strings.ToLower(name)] = tgt
				res.Ordered = append(res.Ordered, tgt)
			}
		case *gotypes.TypeName:
			if isMageNamespace(o.Type()) {
				mset := gotypes.NewMethodSet(o.Type())
				for i := 0; i < mset.Len(); i++ {
					sel := mset.At(i)
					fn, ok := sel.Obj().(*gotypes.Func)
					if !ok || !fn.Exported() {
						continue
					}
					sig := fn.Type().(*gotypes.Signature)
					docKey := name + "." + fn.Name()
					if tgt, ok := bridgeMageTarget(fn.Name(), name, sig, docs[docKey], posToSpan(pkg.Fset, fn.Pos(), len(fn.Name()))); ok {
						qual := strings.ToLower(name) + ":" + strings.ToLower(fn.Name())
						res.Targets[qual] = tgt
						res.Targets[fn.Name()] = tgt
						res.Targets[strings.ToLower(fn.Name())] = tgt
						res.Ordered = append(res.Ordered, tgt)
					}
				}
			}
		}
	}

	mu.Lock()
	mageMap[key] = res
	mu.Unlock()

	cp := *res
	if alias != "" {
		cp.Alias = alias
	} else {
		cp.Alias = cp.PkgName
	}
	return &cp, nil
}

func loadGoPackage(workDir, importPath, version string, mageTags bool) (*packages.Package, error) {
	loadDir := workDir
	if loadDir == "" {
		loadDir, _ = os.Getwd()
	}

	// Support relative package imports like ./tools/magehelpers
	pattern := importPath
	if strings.HasPrefix(importPath, "./") || strings.HasPrefix(importPath, "../") {
		abs := filepath.Join(loadDir, importPath)
		loadDir = abs
		pattern = "."
	}

	pkg, err := tryPackagesLoad(loadDir, pattern, mageTags)
	if err == nil {
		return pkg, nil
	}

	// If third-party module not in local go.mod, resolve via cached helper module
	if strings.Contains(importPath, ".") && !strings.HasPrefix(importPath, ".") {
		modDir, prepErr := ensureCachedGoModule(importPath, version)
		if prepErr == nil {
			if pkg2, err2 := tryPackagesLoad(modDir, importPath, mageTags); err2 == nil {
				return pkg2, nil
			}
		}
	}

	return nil, err
}

func tryPackagesLoad(dir, pattern string, mageTags bool) (*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedTypes | packages.NeedSyntax,
		Dir:  dir,
	}
	if mageTags {
		cfg.BuildFlags = []string{"-tags=mage"}
	}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		return nil, fmt.Errorf("loading Go package %q: %w", pattern, err)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("Go package %q not found", pattern)
	}
	p := pkgs[0]
	if len(p.Errors) > 0 || p.Types == nil {
		var msgs []string
		for _, e := range p.Errors {
			msgs = append(msgs, e.Msg)
		}
		return nil, fmt.Errorf("Go package %q errors: %s", pattern, strings.Join(msgs, "; "))
	}
	return p, nil
}

func ensureCachedGoModule(importPath, version string) (string, error) {
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		cacheBase = os.TempDir()
	}
	sanitized := strings.NewReplacer("/", "_", "@", "_", ":", "_").Replace(importPath + "_" + version)
	dir := filepath.Join(cacheBase, "werk", "pkgcache", sanitized)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	gomod := filepath.Join(dir, "go.mod")
	if _, err := os.Stat(gomod); os.IsNotExist(err) {
		initCmd := exec.Command("go", "mod", "init", "werkpkgcache")
		initCmd.Dir = dir
		if out, err := initCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("go mod init: %s", string(out))
		}
		target := importPath
		if version != "" {
			target += "@" + version
		} else {
			target += "@latest"
		}
		getCmd := exec.Command("go", "get", target)
		getCmd.Dir = dir
		if out, err := getCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("go get %s: %s", target, string(out))
		}
	}
	return dir, nil
}

func bridgeFuncSignature(name string, sig *gotypes.Signature, doc string, span diag.Span) (*types.GoFuncSig, bool) {
	if sig.TypeParams() != nil && sig.TypeParams().Len() > 0 {
		return nil, false
	}

	paramsTuple := sig.Params()
	var params []types.GoParam
	for i := 0; i < paramsTuple.Len(); i++ {
		v := paramsTuple.At(i)
		isVar := sig.Variadic() && i == paramsTuple.Len()-1
		wt, cast, ok := bridgeGoType(v.Type(), true, 0)
		if !ok {
			return nil, false
		}
		params = append(params, types.GoParam{
			Name:         v.Name(),
			WerkType:     wt,
			GoTypeString: gotypes.TypeString(v.Type(), nil),
			Variadic:     isVar,
			Cast:         cast,
		})
	}

	results := sig.Results()
	resType := types.Type(types.Void)
	retErr := false
	retCast := types.CastNone
	goResStr := ""

	switch results.Len() {
	case 0:
		// void
	case 1:
		r0 := results.At(0).Type()
		if isErrorType(r0) {
			retErr = true
		} else {
			wt, cast, ok := bridgeGoType(r0, false, 0)
			if !ok {
				return nil, false
			}
			resType = wt
			retCast = cast
			goResStr = gotypes.TypeString(r0, nil)
		}
	case 2:
		r0 := results.At(0).Type()
		r1 := results.At(1).Type()
		if !isErrorType(r1) {
			return nil, false
		}
		wt, cast, ok := bridgeGoType(r0, false, 0)
		if !ok {
			return nil, false
		}
		resType = wt
		retCast = cast
		retErr = true
		goResStr = gotypes.TypeString(r0, nil)
	default:
		return nil, false
	}

	return &types.GoFuncSig{
		Name:           name,
		Params:         params,
		ResultWerkType: resType,
		GoResultType:   goResStr,
		ReturnsError:   retErr,
		ReturnCast:     retCast,
		Doc:            doc,
		DefSpan:        span,
	}, true
}

func bridgeMageTarget(name, namespace string, sig *gotypes.Signature, doc string, span diag.Span) (*types.MageTargetSig, bool) {
	results := sig.Results()
	retErr := false
	if results.Len() == 1 {
		if !isErrorType(results.At(0).Type()) {
			return nil, false
		}
		retErr = true
	} else if results.Len() > 1 {
		return nil, false
	}

	paramsTuple := sig.Params()
	startIdx := 0
	hasCtx := false
	if paramsTuple.Len() > 0 && gotypes.TypeString(paramsTuple.At(0).Type(), nil) == "context.Context" {
		hasCtx = true
		startIdx = 1
	}

	var params []types.GoParam
	for i := startIdx; i < paramsTuple.Len(); i++ {
		v := paramsTuple.At(i)
		wt, cast, ok := bridgeGoType(v.Type(), true, 0)
		if !ok {
			return nil, false
		}
		params = append(params, types.GoParam{
			Name:         v.Name(),
			WerkType:     wt,
			GoTypeString: gotypes.TypeString(v.Type(), nil),
			Cast:         cast,
		})
	}

	return &types.MageTargetSig{
		Name:         name,
		Namespace:    namespace,
		Params:       params,
		HasContext:   hasCtx,
		ReturnsError: retErr,
		Doc:          doc,
		DefSpan:      span,
	}, true
}

func bridgeGoType(t gotypes.Type, isParam bool, depth int) (types.Type, types.GoCastKind, bool) {
	if depth > 3 {
		return types.Any, types.CastNone, true
	}
	under := t.Underlying()
	switch u := under.(type) {
	case *gotypes.Basic:
		switch u.Kind() {
		case gotypes.Int:
			// Named int types like os.FileMode or time.Duration need explicit cast
			if _, isNamed := t.(*gotypes.Named); isNamed {
				return types.Int, types.CastIntCast, true
			}
			return types.Int, types.CastNone, true
		case gotypes.Int8, gotypes.Int16, gotypes.Int32, gotypes.Int64,
			gotypes.Uint, gotypes.Uint8, gotypes.Uint16, gotypes.Uint32, gotypes.Uint64:
			return types.Int, types.CastIntCast, true
		case gotypes.Float64:
			if _, isNamed := t.(*gotypes.Named); isNamed {
				return types.Float, types.CastFloatCast, true
			}
			return types.Float, types.CastNone, true
		case gotypes.Float32:
			return types.Float, types.CastFloatCast, true
		case gotypes.String:
			return types.String, types.CastNone, true
		case gotypes.Bool:
			return types.Bool, types.CastNone, true
		}
	case *gotypes.Slice:
		// []byte <-> string bridge!
		if elemBasic, ok := u.Elem().Underlying().(*gotypes.Basic); ok && elemBasic.Kind() == gotypes.Uint8 {
			if isParam {
				return types.String, types.CastStringToBytes, true
			}
			return types.String, types.CastBytesToString, true
		}
		elemType, elemCast, ok := bridgeGoType(u.Elem(), isParam, depth+1)
		if !ok || elemCast != types.CastNone {
			return nil, types.CastNone, false
		}
		return &types.SliceType{Elem: elemType}, types.CastNone, true
	case *gotypes.Interface:
		if u.Empty() {
			return types.Any, types.CastNone, true
		}
	case *gotypes.Pointer:
		if !isParam {
			if _, ok := u.Elem().Underlying().(*gotypes.Struct); ok {
				return bridgeStructType(u.Elem(), depth+1)
			}
		}
	case *gotypes.Struct:
		if !isParam {
			return bridgeStructType(t, depth+1)
		}
	}
	return nil, types.CastNone, false
}

func bridgeStructType(t gotypes.Type, depth int) (types.Type, types.GoCastKind, bool) {
	name := gotypes.TypeString(t, nil)
	st, ok := t.Underlying().(*gotypes.Struct)
	if !ok {
		return nil, types.CastNone, false
	}
	mod := types.NewModuleType(name, name, types.ModGoStruct, "")
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if !f.Exported() {
			continue
		}
		ft, _, ok := bridgeGoType(f.Type(), false, depth+1)
		if !ok {
			continue
		}
		mod.AddField(&types.ModuleField{
			Name:        f.Name(),
			GoFieldName: f.Name(),
			Type:        ft,
		})
	}
	return mod, types.CastNone, true
}

func isErrorType(t gotypes.Type) bool {
	return gotypes.TypeString(t, nil) == "error"
}

func isMageNamespace(t gotypes.Type) bool {
	if strings.HasSuffix(gotypes.TypeString(t, nil), "mg.Namespace") {
		return true
	}
	st, ok := t.Underlying().(*gotypes.Struct)
	if !ok {
		return false
	}
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if f.Embedded() && strings.HasSuffix(gotypes.TypeString(f.Type(), nil), "mg.Namespace") {
			return true
		}
	}
	return false
}

func extractDocs(pkg *packages.Package) map[string]string {
	docs := make(map[string]string)
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Doc == nil {
					continue
				}
				txt := strings.TrimSpace(d.Doc.Text())
				if d.Recv == nil {
					docs[d.Name.Name] = txt
				} else if len(d.Recv.List) > 0 {
					if id, ok := d.Recv.List[0].Type.(*ast.Ident); ok {
						docs[id.Name+"."+d.Name.Name] = txt
					}
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						doc := ""
						if vs.Doc != nil {
							doc = strings.TrimSpace(vs.Doc.Text())
						} else if d.Doc != nil {
							doc = strings.TrimSpace(d.Doc.Text())
						}
						for _, n := range vs.Names {
							docs[n.Name] = doc
						}
					}
				}
			}
		}
	}
	return docs
}

func posToSpan(fset *token.FileSet, pos token.Pos, nameLen int) diag.Span {
	if fset == nil || !pos.IsValid() {
		return diag.Span{}
	}
	p := fset.Position(pos)
	line := 0
	if p.Line > 0 {
		line = p.Line - 1
	}
	col := 0
	if p.Column > 0 {
		col = p.Column - 1
	}
	return diag.Span{
		File:  p.Filename,
		Start: diag.Pos{Offset: p.Offset, Line: line, Col: col},
		End:   diag.Pos{Offset: p.Offset + nameLen, Line: line, Col: col + nameLen},
	}
}
