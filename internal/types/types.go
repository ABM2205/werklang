package types

import (
	"fmt"
	"strings"

	"github.com/ABM2205/werklang/internal/diag"
	"github.com/ABM2205/werklang/internal/parser"
)

type Type interface {
	String() string
	Equals(other Type) bool
}

type BasicKind string

const (
	KindInt     BasicKind = "int"
	KindFloat   BasicKind = "float"
	KindString  BasicKind = "string"
	KindBool    BasicKind = "bool"
	KindAny     BasicKind = "any"
	KindVoid    BasicKind = "void"
	KindInvalid BasicKind = "invalid"
)

type BasicType struct {
	Kind BasicKind
}

func (b *BasicType) String() string { return string(b.Kind) }
func (b *BasicType) Equals(other Type) bool {
	if b.Kind == KindInvalid || b.Kind == KindAny {
		return true
	}
	ob, ok := other.(*BasicType)
	if !ok {
		return false
	}
	if ob.Kind == KindInvalid || ob.Kind == KindAny {
		return true
	}
	return b.Kind == ob.Kind
}

var (
	Int     = &BasicType{Kind: KindInt}
	Float   = &BasicType{Kind: KindFloat}
	String  = &BasicType{Kind: KindString}
	Bool    = &BasicType{Kind: KindBool}
	Any     = &BasicType{Kind: KindAny}
	Void    = &BasicType{Kind: KindVoid}
	Invalid = &BasicType{Kind: KindInvalid}
)

type SliceType struct {
	Elem Type
}

func (s *SliceType) String() string {
	if s.Elem == nil {
		return "[]any"
	}
	return "[]" + s.Elem.String()
}

func (s *SliceType) Equals(other Type) bool {
	if ob, ok := other.(*BasicType); ok && (ob.Kind == KindInvalid || ob.Kind == KindAny) {
		return true
	}
	os, ok := other.(*SliceType)
	if !ok {
		return false
	}
	if s.Elem == Any || os.Elem == Any {
		return true
	}
	return s.Elem.Equals(os.Elem)
}

type ModuleSourceKind int

const (
	ModWerk ModuleSourceKind = iota
	ModConfig
	ModPkl
	ModGoStruct
)

type ModuleField struct {
	Name        string
	GoFieldName string
	Type        Type
	Required    bool
	Derived     bool
	DefaultExpr parser.Expr
	Siblings    map[string]bool // sibling fields referenced by DefaultExpr
	Doc         string
	DefSpan     diag.Span
}

type ModuleType struct {
	Name       string
	GoTypeName string
	SourceKind ModuleSourceKind
	SourcePath string
	Fields     []*ModuleField
	FieldMap   map[string]*ModuleField
	Nested     []*ModuleType // nested structs (e.g. nested Pkl objects)
	Prog       *parser.Program
}

func NewModuleType(name, goTypeName string, kind ModuleSourceKind, path string) *ModuleType {
	return &ModuleType{
		Name:       name,
		GoTypeName: goTypeName,
		SourceKind: kind,
		SourcePath: path,
		FieldMap:   make(map[string]*ModuleField),
	}
}

func (m *ModuleType) AddField(f *ModuleField) {
	if existing, ok := m.FieldMap[f.Name]; ok {
		*existing = *f
		return
	}
	m.Fields = append(m.Fields, f)
	m.FieldMap[f.Name] = f
}

func (m *ModuleType) String() string { return m.Name }

func (m *ModuleType) Equals(other Type) bool {
	if ob, ok := other.(*BasicType); ok && (ob.Kind == KindInvalid || ob.Kind == KindAny) {
		return true
	}
	om, ok := other.(*ModuleType)
	if !ok {
		return false
	}
	return m.GoTypeName == om.GoTypeName
}

// --- Go & Mage Interop Types ---

type GoCastKind int

const (
	CastNone GoCastKind = iota
	CastIntCast
	CastFloatCast
	CastBytesToString
	CastStringToBytes
)

type GoParam struct {
	Name         string
	WerkType     Type
	GoTypeString string
	Variadic     bool
	Cast         GoCastKind
}

type GoFuncSig struct {
	Name           string
	Params         []GoParam
	ResultWerkType Type
	GoResultType   string
	ReturnsError   bool
	ReturnCast     GoCastKind
	Doc            string
	DefSpan        diag.Span
}

func (f *GoFuncSig) SignatureString() string {
	var params []string
	for _, p := range f.Params {
		tStr := p.WerkType.String()
		if p.Variadic {
			if st, ok := p.WerkType.(*SliceType); ok {
				tStr = "..." + st.Elem.String()
			}
		}
		if p.Name != "" {
			params = append(params, fmt.Sprintf("%s %s", p.Name, tStr))
		} else {
			params = append(params, tStr)
		}
	}
	ret := ""
	if f.ResultWerkType != nil && f.ResultWerkType != Void {
		ret = " -> " + f.ResultWerkType.String()
	}
	return fmt.Sprintf("%s(%s)%s", f.Name, strings.Join(params, ", "), ret)
}

type GoVarSig struct {
	Name     string
	WerkType Type
	Cast     GoCastKind
	IsConst  bool
	Doc      string
	DefSpan  diag.Span
}

type GoPackageType struct {
	ImportPath string
	Version    string
	Alias      string
	PkgName    string
	Funcs      map[string]*GoFuncSig
	Vars       map[string]*GoVarSig
}

func (g *GoPackageType) String() string         { return "go:" + g.ImportPath }
func (g *GoPackageType) Equals(other Type) bool { return g == other }

type MageTargetSig struct {
	Name         string // Go exported function or method name (e.g. "Build")
	Namespace    string // Non-empty if defined on an mg.Namespace type (e.g. "Docker")
	Params       []GoParam
	HasContext   bool
	ReturnsError bool
	Doc          string
	DefSpan      diag.Span
}

type MagePackageType struct {
	ImportPath string
	Version    string
	Alias      string
	PkgName    string
	Targets    map[string]*MageTargetSig // keyed by exact Go name and lowercase name
	Ordered    []*MageTargetSig
}

func (m *MagePackageType) String() string         { return "mage:" + m.ImportPath }
func (m *MagePackageType) Equals(other Type) bool { return m == other }

// ExportGoIdent produces a deterministic exported Go identifier for a Werklang name.
func ExportGoIdent(name string) string {
	if name == "" {
		return "Field_"
	}
	parts := strings.Split(name, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			b.WriteString("_")
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		if len(p) > 1 {
			b.WriteString(p[1:])
		}
	}
	out := b.String()
	if out == "" {
		return "Field_"
	}
	return out
}
