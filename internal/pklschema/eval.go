package pklschema

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ABM2205/werklang/internal/diag"
	"github.com/ABM2205/werklang/internal/scanner"
	"github.com/apple/pkl-go/pkl"
)

// FieldMeta captures reflected metadata for a generated Go struct field (used by LSP & codegen).
type FieldMeta struct {
	PklName     string
	GoName      string
	GoType      string
	PklTypeDesc string
	Doc         string
	DefSpan     diag.Span
	Children    map[string]*FieldMeta // Keyed by GoName for nested structs
}

// EvalOptions configures compile-time or LSP Pkl evaluation for a .werk file.
type EvalOptions struct {
	ScriptPath      string
	Scan            *scanner.Result
	ExplicitConfigs []string          // Paths from `-c` / `--config`
	Overlay         map[string][]byte // Optional unsaved buffers (LSP)
	SchemaOnly      bool              // LSP mode: emit zero-value var stub & tolerate unpopulated required fields
}

// CompiledConfig holds the generated Go declarations and metadata for a .werk `config` block.
type CompiledConfig struct {
	VarName         string
	StructName      string
	GoDecls         string // Typealiases, nested structs, root struct, and `var <name> = ...`
	NeedsTimeImport bool
	FieldTypes      map[string]string     // Top-level GoName -> GoType (for fast syntactic type lookup)
	RootFields      map[string]*FieldMeta // Top-level GoName -> FieldMeta (for LSP hover/completion/definition)
	AmendingFile    string                // Resolved external .pkl path if amended, else ""
	ValuesHash      string                // SHA-256 of GoDecls
}

// FindPklBinary locates the `pkl` CLI executable across PATH and common install locations.
func FindPklBinary() (string, error) {
	if p, err := exec.LookPath("pkl"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		cand := filepath.Join(home, ".local", "bin", "pkl")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	for _, cand := range []string{"/usr/local/bin/pkl", "/usr/bin/pkl"} {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	return "", fmt.Errorf("pkl CLI binary not found on PATH or ~/.local/bin/pkl")
}

// --- JSON Reflection Wire Types ---

type reflPayload struct {
	Props   map[string]reflProp            `json:"props"`
	Classes map[string]map[string]reflProp `json:"classes"`
	Aliases map[string]reflType            `json:"aliases"`
}

type reflProp struct {
	TypeInfo reflType `json:"typeInfo"`
	IsHidden bool     `json:"isHidden"`
	Doc      string   `json:"doc"`
	Line     int      `json:"line"`
	Col      int      `json:"col"`
}

type reflType struct {
	Kind    string     `json:"kind"` // "declared", "nullable", "union", "stringLit", "unknown"
	Name    string     `json:"name"`
	IsLocal bool       `json:"isLocal"`
	Args    []reflType `json:"args"`
	Elem    *reflType  `json:"elem"`
	Members []reflType `json:"members"`
	Value   string     `json:"value"`
}

const reflectScriptTemplate = `import "pkl:reflect"
import %q as target

output { renderer = new JsonRenderer {} }

local mod = reflect.Module(target)

local function describeType(t: reflect.Type): Dynamic =
  if (t is reflect.DeclaredType)
    new Dynamic {
      kind = "declared"
      name = t.referent.name
      isLocal = t.referent.location.displayUri.startsWith("file:") || t.referent.location.displayUri.startsWith("werk:")
      args = t.typeArguments.map((a) -> describeType(a))
    }
  else if (t is reflect.NullableType)
    new Dynamic {
      kind = "nullable"
      elem = describeType(t.member)
    }
  else if (t is reflect.UnionType)
    new Dynamic {
      kind = "union"
      members = t.members.map((m) -> describeType(m))
    }
  else if (t is reflect.StringLiteralType)
    new Dynamic {
      kind = "stringLit"
      value = t.value
    }
  else
    new Dynamic {
      kind = "unknown"
    }

props = new Dynamic {
  for (name, p in mod.moduleClass.properties) {
    [name] = new Dynamic {
      typeInfo = describeType(p.type)
      isHidden = p.modifiers.contains("hidden")
      doc = p.docComment
      line = p.location.line
      col = p.location.column
    }
  }
}
classes = new Dynamic {
  for (cname, c in mod.classes) {
    [cname] = new Dynamic {
      for (pname, p in c.properties) {
        [pname] = new Dynamic {
          typeInfo = describeType(p.type)
          isHidden = p.modifiers.contains("hidden")
          doc = p.docComment
          line = p.location.line
          col = p.location.column
        }
      }
    }
  }
}
aliases = new Dynamic {
  for (aname, a in mod.typeAliases) {
    [aname] = describeType(a.referent)
  }
}
`

// EvaluateAndGenerate evaluates a .werk file's `config` block (and any selected
// external amending .pkl file) and produces static Go struct + value code.
func EvaluateAndGenerate(opts EvalOptions) (*CompiledConfig, error) {
	if opts.Scan == nil || opts.Scan.Config == nil {
		return nil, nil
	}

	absScript, err := filepath.Abs(opts.ScriptPath)
	if err != nil {
		return nil, err
	}
	absScript = filepath.Clean(absScript)

	rootDir := FindProjectRoot(filepath.Dir(absScript))
	relCheck, relErr := filepath.Rel(rootDir, absScript)
	if relErr != nil || strings.HasPrefix(relCheck, "..") {
		rootDir = filepath.Dir(absScript)
		relCheck = filepath.Base(absScript)
	}
	werkURI := "werk:/" + filepath.ToSlash(relCheck)

	amendingFile, err := selectAmendingPklFile(absScript, rootDir, werkURI, opts.ExplicitConfigs)
	if err != nil {
		return nil, err
	}

	pklBin, err := FindPklBinary()
	if err != nil {
		return nil, err
	}

	reader, err := NewWerkModuleReader(rootDir, opts.Overlay)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	evOpts := []func(*pkl.EvaluatorOptions){
		pkl.PreconfiguredOptions,
		func(o *pkl.EvaluatorOptions) {
			o.AllowedModules = append(o.AllowedModules, "werk:")
		},
		pkl.WithModuleReader(reader),
	}
	if st, err := os.Stat(filepath.Join(rootDir, "PklProject.deps.json")); err == nil && !st.IsDir() {
		if proj, loadErr := loadProjectQuiet(ctx, pklBin, filepath.Join(rootDir, "PklProject")); loadErr == nil {
			evOpts = append(evOpts, pkl.WithProjectDependencies(proj))
		}
	}

	ev, err := pkl.NewEvaluatorWithCommand(ctx, []string{pklBin}, evOpts...)
	if err != nil {
		return nil, fmt.Errorf("starting pkl evaluator: %w", err)
	}
	defer ev.Close()

	// 1. Reflect static schema of `werkURI`
	reflSrc := pkl.TextSource(fmt.Sprintf(reflectScriptTemplate, werkURI))
	reflJSON, err := ev.EvaluateOutputText(ctx, reflSrc)
	if err != nil {
		return nil, formatPklEvalError(absScript, werkURI, err)
	}

	var refl reflPayload
	if err := json.Unmarshal([]byte(reflJSON), &refl); err != nil {
		return nil, fmt.Errorf("decoding pkl reflection payload: %w", err)
	}

	// 2. Evaluate concrete values (either amending .pkl file or template defaults)
	var evalObj pkl.Object
	var evalSrc *pkl.ModuleSource
	if amendingFile != "" {
		evalSrc = pkl.FileSource(amendingFile)
	} else {
		evalSrc = pkl.UriSource(werkURI)
	}

	hasValues := true
	if err := ev.EvaluateModule(ctx, evalSrc, &evalObj); err != nil {
		if opts.SchemaOnly {
			// In LSP SchemaOnly mode, unpopulated required template fields shouldn't break Go struct codegen!
			hasValues = false
		} else {
			return nil, formatPklEvalError(absScript, werkURI, err)
		}
	}

	// 3. Emit Go types and value literal
	gen := &configCodegen{
		scriptPath: absScript,
		cfgBlock:   opts.Scan.Config,
		refl:       &refl,
		schemaOnly: opts.SchemaOnly || !hasValues,
		emitted:    make(map[string]bool),
	}
	return gen.build(&evalObj, hasValues, amendingFile)
}

var amendsRegex = regexp.MustCompile(`(?m)^\s*amends\s+"([^"]+)"`)

func parseAmendsTarget(pklPath string) (string, error) {
	raw, err := os.ReadFile(pklPath)
	if err != nil {
		return "", err
	}
	m := amendsRegex.FindSubmatch(raw)
	if len(m) < 2 {
		return "", nil
	}
	return string(m[1]), nil
}

func amendsMatchesWerk(pklPath, amendsTarget, rootDir, absScript, werkURI string) bool {
	if amendsTarget == "" {
		return false
	}
	if amendsTarget == werkURI {
		return true
	}
	if strings.HasPrefix(amendsTarget, "werk:") {
		rel := strings.TrimPrefix(strings.TrimPrefix(amendsTarget, "werk:"), "/")
		cand := filepath.Clean(filepath.Join(rootDir, filepath.FromSlash(rel)))
		if cand == absScript {
			return true
		}
		// Also check relative to the .pkl file's own directory for standalone scripts
		candLocal := filepath.Clean(filepath.Join(filepath.Dir(pklPath), filepath.FromSlash(rel)))
		if candLocal == absScript {
			return true
		}
	}
	return false
}

func selectAmendingPklFile(absScript, rootDir, werkURI string, explicit []string) (string, error) {
	if len(explicit) > 0 {
		for _, p := range explicit {
			absP, err := filepath.Abs(p)
			if err != nil {
				return "", err
			}
			target, err := parseAmendsTarget(absP)
			if err != nil {
				return "", fmt.Errorf("reading config %s: %w", p, err)
			}
			if !strings.HasPrefix(target, "werk:") {
				return "", fmt.Errorf("config file %s must amend a werk: module (e.g. amends %q)", p, werkURI)
			}
			if amendsMatchesWerk(absP, target, rootDir, absScript, werkURI) {
				return absP, nil
			}
		}
		return "", fmt.Errorf("none of the supplied -c config files amend %s", werkURI)
	}

	// Convention fallback 1: `<stem>.pkl` alongside `<stem>.werk`
	scriptDir := filepath.Dir(absScript)
	base := filepath.Base(absScript)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	candidates := []string{
		filepath.Join(scriptDir, stem+".pkl"),
		filepath.Join(scriptDir, "config.pkl"),
	}
	if filepath.Clean(rootDir) != filepath.Clean(scriptDir) {
		candidates = append(candidates, filepath.Join(rootDir, "config.pkl"))
	}

	for _, cand := range candidates {
		st, err := os.Stat(cand)
		if err != nil || st.IsDir() {
			continue
		}
		target, err := parseAmendsTarget(cand)
		if err != nil {
			continue
		}
		if amendsMatchesWerk(cand, target, rootDir, absScript, werkURI) {
			return cand, nil
		}
	}

	return "", nil
}

func formatPklEvalError(absScript, werkURI string, err error) error {
	msg := err.Error()
	msg = strings.ReplaceAll(msg, werkURI, absScript)
	return fmt.Errorf("%s", msg)
}

func loadProjectQuiet(ctx context.Context, pklBin, projectPath string) (*pkl.Project, error) {
	prev := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(prev)
	ev, err := pkl.NewEvaluatorWithCommand(ctx, []string{pklBin}, pkl.PreconfiguredOptions, func(o *pkl.EvaluatorOptions) {
		o.Logger = pkl.NoopLogger
	})
	if err != nil {
		return nil, err
	}
	defer ev.Close()
	return pkl.LoadProjectFromEvaluator(ctx, ev, pkl.FileSource(projectPath))
}

// --- Go Struct & Value Generator ---

type configCodegen struct {
	scriptPath string
	cfgBlock   *scanner.ConfigBlock
	refl       *reflPayload
	schemaOnly bool
	needsTime  bool
	typeDecls  []string
	emitted    map[string]bool
}

func (g *configCodegen) build(evalObj *pkl.Object, hasValues bool, amendingFile string) (*CompiledConfig, error) {
	// 1. Emit string-literal union typealiases declared inside `config {}`
	aliasNames := sortedKeys(g.refl.Aliases)
	aliasGoNames := make(map[string]string)
	for _, aname := range aliasNames {
		at := g.refl.Aliases[aname]
		if at.Kind == "union" && allStringLiterals(at.Members) {
			goAlias := g.scopedTypeName(aname)
			aliasGoNames[aname] = goAlias
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("type %s string\n\nconst (\n", goAlias))
			for _, m := range at.Members {
				constName := goAlias + UpperFirst(m.Value)
				sb.WriteString(fmt.Sprintf("\t%s %s = %s\n", constName, goAlias, strconv.Quote(m.Value)))
			}
			sb.WriteString(")\n")
			g.typeDecls = append(g.typeDecls, sb.String())
			g.emitted[goAlias] = true
		}
	}

	// 2. Determine property order (reflected properties sorted by source line, plus any extra Dynamic keys)
	type propEntry struct {
		name string
		rp   reflProp
		hasR bool
	}
	var entries []propEntry
	seen := make(map[string]bool)
	for name, rp := range g.refl.Props {
		seen[name] = true
		if rp.IsHidden {
			continue
		}
		entries = append(entries, propEntry{name: name, rp: rp, hasR: true})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].rp.Line != entries[j].rp.Line {
			return entries[i].rp.Line < entries[j].rp.Line
		}
		return entries[i].name < entries[j].name
	})

	if hasValues && evalObj != nil {
		extraKeys := sortedKeys(evalObj.Properties)
		for _, k := range extraKeys {
			if !seen[k] {
				entries = append(entries, propEntry{name: k})
			}
		}
	}

	rootStruct := g.cfgBlock.StructName()
	var structFields []string
	var initFields []string
	fieldTypes := make(map[string]string, len(entries))
	rootMeta := make(map[string]*FieldMeta, len(entries))

	for _, e := range entries {
		goField := UpperFirst(e.name)
		var val any
		hasVal := false
		if hasValues && evalObj != nil {
			val, hasVal = evalObj.Properties[e.name]
		}

		nestedPrefix := g.cfgBlock.TypePrefix() + goField
		goType, valExpr, children := g.resolveProp(nestedPrefix, e.rp.TypeInfo, val, hasVal, aliasGoNames)

		structFields = append(structFields, fmt.Sprintf("\t%s %s", goField, goType))
		fieldTypes[goField] = goType
		if !g.schemaOnly && hasVal {
			initFields = append(initFields, fmt.Sprintf("\t%s: %s,", goField, valExpr))
		}

		defLine := g.cfgBlock.StartLineFallback(e.rp.Line)
		defCol := 0
		if e.rp.Col > 0 {
			defCol = e.rp.Col - 1
		}
		rootMeta[goField] = &FieldMeta{
			PklName:     e.name,
			GoName:      goField,
			GoType:      goType,
			PklTypeDesc: describeReflTypeHuman(e.rp.TypeInfo, goType),
			Doc:         strings.TrimSpace(e.rp.Doc),
			DefSpan: diag.Span{
				File:  g.scriptPath,
				Start: diag.Pos{Line: defLine, Col: defCol},
				End:   diag.Pos{Line: defLine, Col: defCol + len(e.name)},
			},
			Children: children,
		}
	}

	var rootDecl strings.Builder
	rootDecl.WriteString(fmt.Sprintf("type %s struct {\n", rootStruct))
	for _, f := range structFields {
		rootDecl.WriteString(f + "\n")
	}
	rootDecl.WriteString("}\n")
	g.typeDecls = append(g.typeDecls, rootDecl.String())

	var full strings.Builder
	for _, td := range g.typeDecls {
		full.WriteString(td)
		full.WriteString("\n")
	}

	varName := g.cfgBlock.VarName()
	if g.schemaOnly {
		full.WriteString(fmt.Sprintf("var %s %s\n", varName, rootStruct))
	} else {
		full.WriteString(fmt.Sprintf("var %s = %s{\n", varName, rootStruct))
		for _, f := range initFields {
			full.WriteString(f + "\n")
		}
		full.WriteString("}\n")
	}

	decls := full.String()
	sum := sha256.Sum256([]byte(decls))

	return &CompiledConfig{
		VarName:         varName,
		StructName:      rootStruct,
		GoDecls:         decls,
		NeedsTimeImport: g.needsTime,
		FieldTypes:      fieldTypes,
		RootFields:      rootMeta,
		AmendingFile:    amendingFile,
		ValuesHash:      hex.EncodeToString(sum[:]),
	}, nil
}

func (g *configCodegen) scopedTypeName(pklName string) string {
	isExportedConfig := len(g.cfgBlock.Name) > 0 && strings.ToUpper(g.cfgBlock.Name[:1]) == g.cfgBlock.Name[:1]
	if isExportedConfig {
		return UpperFirst(pklName)
	}
	return g.cfgBlock.TypePrefix() + UpperFirst(pklName)
}

func (g *configCodegen) resolveProp(
	nestedPrefix string,
	rt reflType,
	val any,
	hasVal bool,
	aliasGoNames map[string]string,
) (goType string, valExpr string, children map[string]*FieldMeta) {
	switch rt.Kind {
	case "nullable":
		if rt.Elem == nil {
			return "any", "nil", nil
		}
		innerType, innerExpr, innerChildren := g.resolveProp(nestedPrefix, *rt.Elem, val, hasVal && val != nil, aliasGoNames)
		// Slices and maps in Go are already nilable without pointer indirection
		if strings.HasPrefix(innerType, "[]") || strings.HasPrefix(innerType, "map[") || innerType == "any" {
			if !hasVal || val == nil {
				return innerType, "nil", innerChildren
			}
			return innerType, innerExpr, innerChildren
		}
		ptrType := "*" + innerType
		if !hasVal || val == nil {
			return ptrType, "nil", innerChildren
		}
		// Go 1.26+ supports `new(expr)` directly!
		return ptrType, fmt.Sprintf("new(%s)", innerExpr), innerChildren

	case "union":
		if allStringLiterals(rt.Members) {
			if !hasVal || val == nil {
				return "string", `""`, nil
			}
			return "string", strconv.Quote(fmt.Sprint(val)), nil
		}
		return g.inferFromValue(nestedPrefix, val, hasVal, aliasGoNames)

	case "stringLit":
		if !hasVal || val == nil {
			return "string", strconv.Quote(rt.Value), nil
		}
		return "string", strconv.Quote(fmt.Sprint(val)), nil

	case "declared":
		// Check local string-union typealias first
		if goAlias, ok := aliasGoNames[rt.Name]; ok {
			if !hasVal || val == nil {
				return goAlias, fmt.Sprintf("%s(%q)", goAlias, ""), nil
			}
			return goAlias, fmt.Sprintf("%s(%s)", goAlias, strconv.Quote(fmt.Sprint(val))), nil
		}
		// Check local Pkl class
		if clsProps, ok := g.refl.Classes[rt.Name]; ok {
			return g.emitPklClass(rt.Name, clsProps, val, hasVal, aliasGoNames)
		}
		// Check Pkl stdlib types
		switch rt.Name {
		case "String":
			return "string", formatStringVal(val, hasVal), nil
		case "Boolean":
			return "bool", formatBoolVal(val, hasVal), nil
		case "Int":
			return "int", formatIntVal("int", val, hasVal), nil
		case "Int8":
			return "int8", formatIntVal("int8", val, hasVal), nil
		case "Int16":
			return "int16", formatIntVal("int16", val, hasVal), nil
		case "Int32":
			return "int32", formatIntVal("int32", val, hasVal), nil
		case "Int64":
			return "int64", formatIntVal("int64", val, hasVal), nil
		case "UInt":
			return "uint", formatUintVal("uint", val, hasVal), nil
		case "UInt8":
			return "uint8", formatUintVal("uint8", val, hasVal), nil
		case "UInt16":
			return "uint16", formatUintVal("uint16", val, hasVal), nil
		case "UInt32":
			return "uint32", formatUintVal("uint32", val, hasVal), nil
		case "UInt64":
			return "uint64", formatUintVal("uint64", val, hasVal), nil
		case "Float", "Number":
			return "float64", formatFloatVal(val, hasVal), nil
		case "Duration":
			g.needsTime = true
			return "_werk_time.Duration", formatDurationVal(val, hasVal), nil
		case "DataSize":
			return "int64", formatDataSizeVal(val, hasVal), nil
		case "Listing", "List", "Set", "Collection":
			var elemRT reflType
			if len(rt.Args) > 0 {
				elemRT = rt.Args[0]
			} else {
				elemRT = reflType{Kind: "unknown"}
			}
			return g.emitSlice(nestedPrefix+"Elem", elemRT, val, hasVal, aliasGoNames)
		case "Mapping", "Map":
			keyRT := reflType{Kind: "unknown"}
			valRT := reflType{Kind: "unknown"}
			if len(rt.Args) >= 2 {
				keyRT = rt.Args[0]
				valRT = rt.Args[1]
			}
			return g.emitMap(nestedPrefix+"Val", keyRT, valRT, val, hasVal, aliasGoNames)
		case "Pair":
			firstRT := reflType{Kind: "unknown"}
			secondRT := reflType{Kind: "unknown"}
			if len(rt.Args) >= 2 {
				firstRT = rt.Args[0]
				secondRT = rt.Args[1]
			}
			return g.emitPair(nestedPrefix, firstRT, secondRT, val, hasVal, aliasGoNames)
		case "Dynamic", "Any", "Object":
			return g.inferFromValue(nestedPrefix, val, hasVal, aliasGoNames)
		default:
			// Check if alias resolves to another type in `g.refl.Aliases`
			if aliased, ok := g.refl.Aliases[rt.Name]; ok {
				return g.resolveProp(nestedPrefix, aliased, val, hasVal, aliasGoNames)
			}
			return g.inferFromValue(nestedPrefix, val, hasVal, aliasGoNames)
		}
	}

	return g.inferFromValue(nestedPrefix, val, hasVal, aliasGoNames)
}

func (g *configCodegen) emitPklClass(
	className string,
	clsProps map[string]reflProp,
	val any,
	hasVal bool,
	aliasGoNames map[string]string,
) (string, string, map[string]*FieldMeta) {
	goStruct := g.scopedTypeName(className)

	type propItem struct {
		name string
		rp   reflProp
	}
	var items []propItem
	for k, rp := range clsProps {
		if rp.IsHidden {
			continue
		}
		items = append(items, propItem{name: k, rp: rp})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].rp.Line != items[j].rp.Line {
			return items[i].rp.Line < items[j].rp.Line
		}
		return items[i].name < items[j].name
	})

	obj := asPklObject(val)
	children := make(map[string]*FieldMeta, len(items))
	var fieldLines []string
	var valLines []string

	for _, it := range items {
		goField := UpperFirst(it.name)
		var fVal any
		fHas := false
		if hasVal && obj != nil {
			fVal, fHas = obj.Properties[it.name]
		}
		ft, fv, grand := g.resolveProp(goStruct+goField, it.rp.TypeInfo, fVal, fHas, aliasGoNames)
		fieldLines = append(fieldLines, fmt.Sprintf("\t%s %s", goField, ft))
		if !g.schemaOnly && fHas {
			valLines = append(valLines, fmt.Sprintf("\t%s: %s,", goField, fv))
		}
		defLine := g.cfgBlock.StartLineFallback(it.rp.Line)
		defCol := 0
		if it.rp.Col > 0 {
			defCol = it.rp.Col - 1
		}
		children[goField] = &FieldMeta{
			PklName:     it.name,
			GoName:      goField,
			GoType:      ft,
			PklTypeDesc: describeReflTypeHuman(it.rp.TypeInfo, ft),
			Doc:         strings.TrimSpace(it.rp.Doc),
			DefSpan: diag.Span{
				File:  g.scriptPath,
				Start: diag.Pos{Line: defLine, Col: defCol},
				End:   diag.Pos{Line: defLine, Col: defCol + len(it.name)},
			},
			Children: grand,
		}
	}

	if !g.emitted[goStruct] {
		g.emitted[goStruct] = true
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("type %s struct {\n", goStruct))
		for _, fl := range fieldLines {
			sb.WriteString(fl + "\n")
		}
		sb.WriteString("}\n")
		g.typeDecls = append(g.typeDecls, sb.String())
	}

	if g.schemaOnly || !hasVal || obj == nil {
		return goStruct, fmt.Sprintf("%s{}", goStruct), children
	}
	var lit strings.Builder
	lit.WriteString(fmt.Sprintf("%s{\n", goStruct))
	for _, vl := range valLines {
		lit.WriteString(vl + "\n")
	}
	lit.WriteString("}")
	return goStruct, lit.String(), children
}

func (g *configCodegen) inferFromValue(
	nestedPrefix string,
	val any,
	hasVal bool,
	aliasGoNames map[string]string,
) (string, string, map[string]*FieldMeta) {
	if !hasVal || val == nil {
		return "any", "nil", nil
	}
	switch v := val.(type) {
	case string:
		return "string", strconv.Quote(v), nil
	case bool:
		return "bool", strconv.FormatBool(v), nil
	case int, int8, int16, int32, int64:
		return "int", formatIntVal("int", v, true), nil
	case uint, uint8, uint16, uint32, uint64:
		return "uint", formatUintVal("uint", v, true), nil
	case float32, float64:
		return "float64", formatFloatVal(v, true), nil
	case pkl.Duration, *pkl.Duration:
		g.needsTime = true
		return "_werk_time.Duration", formatDurationVal(v, true), nil
	case pkl.DataSize, *pkl.DataSize:
		return "int64", formatDataSizeVal(v, true), nil
	case []any:
		return g.inferSliceFromElements(nestedPrefix+"Elem", v, aliasGoNames)
	case map[any]any:
		return g.inferMapFromEntries(nestedPrefix+"Val", v, aliasGoNames)
	case map[string]any:
		anyMap := make(map[any]any, len(v))
		for k, mv := range v {
			anyMap[k] = mv
		}
		return g.inferMapFromEntries(nestedPrefix+"Val", anyMap, aliasGoNames)
	case pkl.Object:
		return g.inferDynamicObject(nestedPrefix, &v, aliasGoNames)
	case *pkl.Object:
		if v == nil {
			return "any", "nil", nil
		}
		return g.inferDynamicObject(nestedPrefix, v, aliasGoNames)
	default:
		return "any", "nil", nil
	}
}

func (g *configCodegen) inferDynamicObject(
	nestedPrefix string,
	obj *pkl.Object,
	aliasGoNames map[string]string,
) (string, string, map[string]*FieldMeta) {
	// Check if this is actually an instance of a reflected local class
	if idx := strings.LastIndexByte(obj.Name, '#'); idx >= 0 {
		clsName := obj.Name[idx+1:]
		if clsProps, ok := g.refl.Classes[clsName]; ok {
			return g.emitPklClass(clsName, clsProps, obj, true, aliasGoNames)
		}
	}

	hasProps := len(obj.Properties) > 0
	hasElems := len(obj.Elements) > 0
	hasEntries := len(obj.Entries) > 0

	// Empty Dynamic `{}` -> `struct{}`
	if !hasProps && !hasElems && !hasEntries {
		return "struct{}", "struct{}{}", nil
	}

	// Elements-only Dynamic (`tags { "web"; "api" }`) -> Go slice
	if !hasProps && hasElems && !hasEntries {
		return g.inferSliceFromElements(nestedPrefix+"Elem", obj.Elements, aliasGoNames)
	}

	// Entries-only Dynamic (`headers { ["X-Env"] = "prod" }`) -> Go map
	if !hasProps && !hasElems && hasEntries {
		return g.inferMapFromEntries(nestedPrefix+"Val", obj.Entries, aliasGoNames)
	}

	// Properties Dynamic (and optional mixed Elements/Entries) -> generated Go struct
	structName := nestedPrefix + "Config"
	propKeys := sortedKeys(obj.Properties)
	children := make(map[string]*FieldMeta, len(propKeys))

	var fieldLines []string
	var valLines []string
	usedFieldNames := make(map[string]bool)

	for _, pk := range propKeys {
		goField := UpperFirst(pk)
		usedFieldNames[goField] = true
		pv := obj.Properties[pk]
		ft, fv, grand := g.inferFromValue(nestedPrefix+goField, pv, true, aliasGoNames)
		fieldLines = append(fieldLines, fmt.Sprintf("\t%s %s", goField, ft))
		if !g.schemaOnly {
			valLines = append(valLines, fmt.Sprintf("\t%s: %s,", goField, fv))
		}
		children[goField] = &FieldMeta{
			PklName:     pk,
			GoName:      goField,
			GoType:      ft,
			PklTypeDesc: ft,
			DefSpan:     findLineInConfigBody(g.scriptPath, g.cfgBlock, pk),
			Children:    grand,
		}
	}

	if hasElems {
		elemField := "Elements"
		if usedFieldNames[elemField] {
			elemField = "PklElements"
		}
		et, ev, _ := g.inferSliceFromElements(nestedPrefix+"Elem", obj.Elements, aliasGoNames)
		fieldLines = append(fieldLines, fmt.Sprintf("\t%s %s", elemField, et))
		if !g.schemaOnly {
			valLines = append(valLines, fmt.Sprintf("\t%s: %s,", elemField, ev))
		}
	}

	if hasEntries {
		entryField := "Entries"
		if usedFieldNames[entryField] {
			entryField = "PklEntries"
		}
		mt, mv, _ := g.inferMapFromEntries(nestedPrefix+"Entry", obj.Entries, aliasGoNames)
		fieldLines = append(fieldLines, fmt.Sprintf("\t%s %s", entryField, mt))
		if !g.schemaOnly {
			valLines = append(valLines, fmt.Sprintf("\t%s: %s,", entryField, mv))
		}
	}

	if !g.emitted[structName] {
		g.emitted[structName] = true
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("type %s struct {\n", structName))
		for _, fl := range fieldLines {
			sb.WriteString(fl + "\n")
		}
		sb.WriteString("}\n")
		g.typeDecls = append(g.typeDecls, sb.String())
	}

	if g.schemaOnly {
		return structName, fmt.Sprintf("%s{}", structName), children
	}
	var lit strings.Builder
	lit.WriteString(fmt.Sprintf("%s{\n", structName))
	for _, vl := range valLines {
		lit.WriteString(vl + "\n")
	}
	lit.WriteString("}")
	return structName, lit.String(), children
}

func (g *configCodegen) emitSlice(
	elemPrefix string,
	elemRT reflType,
	val any,
	hasVal bool,
	aliasGoNames map[string]string,
) (string, string, map[string]*FieldMeta) {
	elems := extractSliceValues(val)
	if elemRT.Kind == "unknown" {
		return g.inferSliceFromElements(elemPrefix, elems, aliasGoNames)
	}
	elemGoType, _, children := g.resolveProp(elemPrefix, elemRT, nil, false, aliasGoNames)
	sliceType := "[]" + elemGoType
	if g.schemaOnly || !hasVal || len(elems) == 0 {
		return sliceType, fmt.Sprintf("%s{}", sliceType), children
	}
	var itemExprs []string
	for _, item := range elems {
		_, expr, _ := g.resolveProp(elemPrefix, elemRT, item, true, aliasGoNames)
		itemExprs = append(itemExprs, expr)
	}
	return sliceType, fmt.Sprintf("%s{%s}", sliceType, strings.Join(itemExprs, ", ")), children
}

func (g *configCodegen) inferSliceFromElements(
	elemPrefix string,
	elems []any,
	aliasGoNames map[string]string,
) (string, string, map[string]*FieldMeta) {
	if len(elems) == 0 {
		return "[]any", "[]any{}", nil
	}
	firstType, firstExpr, children := g.inferFromValue(elemPrefix, elems[0], true, aliasGoNames)
	itemExprs := []string{firstExpr}
	homogeneous := true
	for _, item := range elems[1:] {
		t, expr, _ := g.inferFromValue(elemPrefix, item, true, aliasGoNames)
		if t != firstType {
			homogeneous = false
		}
		itemExprs = append(itemExprs, expr)
	}
	if !homogeneous {
		return "[]any", fmt.Sprintf("[]any{%s}", strings.Join(itemExprs, ", ")), nil
	}
	sliceType := "[]" + firstType
	return sliceType, fmt.Sprintf("%s{%s}", sliceType, strings.Join(itemExprs, ", ")), children
}

func (g *configCodegen) emitMap(
	valPrefix string,
	keyRT, valRT reflType,
	val any,
	hasVal bool,
	aliasGoNames map[string]string,
) (string, string, map[string]*FieldMeta) {
	entries := extractMapValues(val)
	if keyRT.Kind == "unknown" && valRT.Kind == "unknown" {
		return g.inferMapFromEntries(valPrefix, entries, aliasGoNames)
	}
	keyGoType, _, _ := g.resolveProp(valPrefix+"Key", keyRT, nil, false, aliasGoNames)
	if keyGoType == "any" {
		keyGoType = "string"
	}
	valGoType, _, children := g.resolveProp(valPrefix, valRT, nil, false, aliasGoNames)
	mapType := fmt.Sprintf("map[%s]%s", keyGoType, valGoType)
	if g.schemaOnly || !hasVal || len(entries) == 0 {
		return mapType, fmt.Sprintf("%s{}", mapType), children
	}

	type kvPair struct {
		sortKey string
		kExpr   string
		vExpr   string
	}
	var pairs []kvPair
	for k, mv := range entries {
		_, ke, _ := g.resolveProp(valPrefix+"Key", keyRT, k, true, aliasGoNames)
		_, ve, _ := g.resolveProp(valPrefix, valRT, mv, true, aliasGoNames)
		pairs = append(pairs, kvPair{sortKey: fmt.Sprint(k), kExpr: ke, vExpr: ve})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].sortKey < pairs[j].sortKey })

	var parts []string
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("%s: %s", p.kExpr, p.vExpr))
	}
	return mapType, fmt.Sprintf("%s{%s}", mapType, strings.Join(parts, ", ")), children
}

func (g *configCodegen) inferMapFromEntries(
	valPrefix string,
	entries map[any]any,
	aliasGoNames map[string]string,
) (string, string, map[string]*FieldMeta) {
	if len(entries) == 0 {
		return "map[string]any", "map[string]any{}", nil
	}
	type rawKV struct {
		sortKey string
		k       any
		v       any
	}
	var sorted []rawKV
	for k, v := range entries {
		sorted = append(sorted, rawKV{sortKey: fmt.Sprint(k), k: k, v: v})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].sortKey < sorted[j].sortKey })

	keyType, _, _ := g.inferFromValue(valPrefix+"Key", sorted[0].k, true, aliasGoNames)
	valType, _, children := g.inferFromValue(valPrefix, sorted[0].v, true, aliasGoNames)
	homogeneousKey := true
	homogeneousVal := true

	var kExprs, vExprs []string
	for _, item := range sorted {
		kt, ke, _ := g.inferFromValue(valPrefix+"Key", item.k, true, aliasGoNames)
		vt, ve, _ := g.inferFromValue(valPrefix, item.v, true, aliasGoNames)
		if kt != keyType {
			homogeneousKey = false
		}
		if vt != valType {
			homogeneousVal = false
		}
		kExprs = append(kExprs, ke)
		vExprs = append(vExprs, ve)
	}
	if !homogeneousKey {
		keyType = "any"
	}
	if !homogeneousVal {
		valType = "any"
		children = nil
	}
	mapType := fmt.Sprintf("map[%s]%s", keyType, valType)
	var parts []string
	for i := range sorted {
		parts = append(parts, fmt.Sprintf("%s: %s", kExprs[i], vExprs[i]))
	}
	return mapType, fmt.Sprintf("%s{%s}", mapType, strings.Join(parts, ", ")), children
}

func (g *configCodegen) emitPair(
	prefix string,
	firstRT, secondRT reflType,
	val any,
	hasVal bool,
	aliasGoNames map[string]string,
) (string, string, map[string]*FieldMeta) {
	var firstVal, secondVal any
	hasFirst, hasSecond := false, false
	if hasVal {
		if p, ok := val.(pkl.Pair[any, any]); ok {
			firstVal, secondVal = p.First, p.Second
			hasFirst, hasSecond = true, true
		} else if p, ok := val.(*pkl.Pair[any, any]); ok && p != nil {
			firstVal, secondVal = p.First, p.Second
			hasFirst, hasSecond = true, true
		}
	}
	ft, fe, _ := g.resolveProp(prefix+"First", firstRT, firstVal, hasFirst, aliasGoNames)
	st, se, _ := g.resolveProp(prefix+"Second", secondRT, secondVal, hasSecond, aliasGoNames)
	pairType := fmt.Sprintf("struct {\n\tFirst %s\n\tSecond %s\n}", ft, st)
	if g.schemaOnly || !hasFirst {
		return pairType, fmt.Sprintf("%s{}", pairType), nil
	}
	return pairType, fmt.Sprintf("%s{First: %s, Second: %s}", pairType, fe, se), nil
}

// --- Value Formatting Helpers ---

func formatStringVal(val any, hasVal bool) string {
	if !hasVal || val == nil {
		return `""`
	}
	return strconv.Quote(fmt.Sprint(val))
}

func formatBoolVal(val any, hasVal bool) string {
	if !hasVal || val == nil {
		return "false"
	}
	if b, ok := val.(bool); ok {
		return strconv.FormatBool(b)
	}
	return "false"
}

func formatIntVal(goType string, val any, hasVal bool) string {
	if !hasVal || val == nil {
		return "0"
	}
	var n int64
	switch v := val.(type) {
	case int:
		n = int64(v)
	case int8:
		n = int64(v)
	case int16:
		n = int64(v)
	case int32:
		n = int64(v)
	case int64:
		n = v
	case uint:
		n = int64(v)
	case uint8:
		n = int64(v)
	case uint16:
		n = int64(v)
	case uint32:
		n = int64(v)
	case uint64:
		n = int64(v)
	}
	if goType == "int" {
		return strconv.FormatInt(n, 10)
	}
	return fmt.Sprintf("%s(%d)", goType, n)
}

func formatUintVal(goType string, val any, hasVal bool) string {
	if !hasVal || val == nil {
		return "0"
	}
	var n uint64
	switch v := val.(type) {
	case int:
		n = uint64(v)
	case int8:
		n = uint64(v)
	case int16:
		n = uint64(v)
	case int32:
		n = uint64(v)
	case int64:
		n = uint64(v)
	case uint:
		n = uint64(v)
	case uint8:
		n = uint64(v)
	case uint16:
		n = uint64(v)
	case uint32:
		n = uint64(v)
	case uint64:
		n = v
	}
	return fmt.Sprintf("%s(%d)", goType, n)
}

func formatFloatVal(val any, hasVal bool) string {
	if !hasVal || val == nil {
		return "0"
	}
	var f float64
	switch v := val.(type) {
	case float32:
		f = float64(v)
	case float64:
		f = v
	case int:
		f = float64(v)
	case int64:
		f = float64(v)
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

func formatDurationVal(val any, hasVal bool) string {
	if !hasVal || val == nil {
		return "_werk_time.Duration(0)"
	}
	switch v := val.(type) {
	case pkl.Duration:
		return fmt.Sprintf("_werk_time.Duration(%d)", v.GoDuration().Nanoseconds())
	case *pkl.Duration:
		if v != nil {
			return fmt.Sprintf("_werk_time.Duration(%d)", v.GoDuration().Nanoseconds())
		}
	}
	return "_werk_time.Duration(0)"
}

func formatDataSizeVal(val any, hasVal bool) string {
	if !hasVal || val == nil {
		return "int64(0)"
	}
	switch v := val.(type) {
	case pkl.DataSize:
		return fmt.Sprintf("int64(%d)", int64(math.Round(v.ToUnit(pkl.Bytes).Value)))
	case *pkl.DataSize:
		if v != nil {
			return fmt.Sprintf("int64(%d)", int64(math.Round(v.ToUnit(pkl.Bytes).Value)))
		}
	}
	return "int64(0)"
}

func extractSliceValues(val any) []any {
	switch v := val.(type) {
	case []any:
		return v
	case pkl.Object:
		return v.Elements
	case *pkl.Object:
		if v != nil {
			return v.Elements
		}
	}
	return nil
}

func extractMapValues(val any) map[any]any {
	switch v := val.(type) {
	case map[any]any:
		return v
	case map[string]any:
		out := make(map[any]any, len(v))
		for k, mv := range v {
			out[k] = mv
		}
		return out
	case pkl.Object:
		return v.Entries
	case *pkl.Object:
		if v != nil {
			return v.Entries
		}
	}
	return nil
}

func asPklObject(val any) *pkl.Object {
	switch v := val.(type) {
	case pkl.Object:
		return &v
	case *pkl.Object:
		return v
	}
	return nil
}

func allStringLiterals(members []reflType) bool {
	if len(members) == 0 {
		return false
	}
	for _, m := range members {
		if m.Kind != "stringLit" {
			return false
		}
	}
	return true
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func describeReflTypeHuman(rt reflType, fallbackGo string) string {
	switch rt.Kind {
	case "declared":
		if len(rt.Args) == 0 {
			return rt.Name
		}
		var args []string
		for _, a := range rt.Args {
			args = append(args, describeReflTypeHuman(a, ""))
		}
		return fmt.Sprintf("%s<%s>", rt.Name, strings.Join(args, ", "))
	case "nullable":
		if rt.Elem != nil {
			return describeReflTypeHuman(*rt.Elem, "") + "?"
		}
	case "union":
		var parts []string
		for _, m := range rt.Members {
			parts = append(parts, describeReflTypeHuman(m, ""))
		}
		return strings.Join(parts, " | ")
	case "stringLit":
		return strconv.Quote(rt.Value)
	}
	return fallbackGo
}

func findLineInConfigBody(scriptPath string, blk *scanner.ConfigBlock, propName string) diag.Span {
	if blk == nil {
		return diag.Span{File: scriptPath}
	}
	lines := strings.Split(blk.Body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, propName+" ") || strings.HasPrefix(trimmed, propName+"=") || strings.HasPrefix(trimmed, propName+":") || strings.HasPrefix(trimmed, propName+"{") {
			col := strings.Index(line, propName)
			absLine := blk.BodyStart.Line + i
			return diag.Span{
				File:  scriptPath,
				Start: diag.Pos{Line: absLine, Col: col},
				End:   diag.Pos{Line: absLine, Col: col + len(propName)},
			}
		}
	}
	return blk.KeywordSpan
}
