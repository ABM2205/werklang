package parser

import (
	"github.com/ABM2205/werklang/internal/diag"
	"github.com/ABM2205/werklang/internal/lexer"
)

type Node interface {
	NodeSpan() diag.Span
}

type Stmt interface {
	Node
	stmtNode()
}

type Expr interface {
	Node
	exprNode()
}

type TypeRef interface {
	Node
	typeNode()
}

// Program represents a parsed .werk file or Werkfile.
type Program struct {
	Path     string
	Amends   *AmendsClause
	Imports  []*ImportDecl
	Configs  []*ConfigDecl
	Targets  []*TargetDecl
	Fields   []*FieldDecl
	Stmts    []Stmt // Top-level executable order (FieldDecls + imperative Stmts)
	Items    []Node // Exact source order of all top-level declarations/statements
	Comments []lexer.Comment
	Span     diag.Span
}

func (p *Program) NodeSpan() diag.Span { return p.Span }

type AmendsClause struct {
	Path     string
	PathSpan diag.Span
	Span     diag.Span
}

func (a *AmendsClause) NodeSpan() diag.Span { return a.Span }

type ImportDecl struct {
	Path      string
	PathSpan  diag.Span
	Alias     string
	AliasSpan diag.Span
	Span      diag.Span
}

func (i *ImportDecl) NodeSpan() diag.Span { return i.Span }

type ConfigDecl struct {
	Name     string
	NameSpan diag.Span
	Fields   []*FieldDecl
	Span     diag.Span
}

func (c *ConfigDecl) NodeSpan() diag.Span { return c.Span }

type TargetDecl struct {
	Name     string
	NameSpan diag.Span
	Body     []Stmt
	Span     diag.Span
}

func (t *TargetDecl) NodeSpan() diag.Span { return t.Span }

// FieldDecl represents both module/config field declarations (`port int = 8080`,
// `token string`) and variable bindings (`foo = "bar"`).
type FieldDecl struct {
	Name     string
	NameSpan diag.Span
	Type     TypeRef // nil if inferred
	Value    Expr    // nil if required schema field
	Span     diag.Span
}

func (f *FieldDecl) NodeSpan() diag.Span { return f.Span }
func (f *FieldDecl) stmtNode()           {}

type IfStmt struct {
	Cond Expr
	Then []Stmt
	Else []Stmt // may contain a single *IfStmt for `else if`
	Span diag.Span
}

func (s *IfStmt) NodeSpan() diag.Span { return s.Span }
func (s *IfStmt) stmtNode()           {}

type ForStmt struct {
	IndexVar  string
	IndexSpan diag.Span
	ItemVar   string
	ItemSpan  diag.Span
	Iterable  Expr
	Body      []Stmt
	Span      diag.Span
}

func (s *ForStmt) NodeSpan() diag.Span { return s.Span }
func (s *ForStmt) stmtNode()           {}

type ExprStmt struct {
	Expr Expr
	Span diag.Span
}

func (s *ExprStmt) NodeSpan() diag.Span { return s.Span }
func (s *ExprStmt) stmtNode()           {}

// --- Type References ---

type PrimitiveType struct {
	Kind string // "int", "float", "string", "bool"
	Span diag.Span
}

func (t *PrimitiveType) NodeSpan() diag.Span { return t.Span }
func (t *PrimitiveType) typeNode()           {}

type SliceType struct {
	Elem TypeRef
	Span diag.Span
}

func (t *SliceType) NodeSpan() diag.Span { return t.Span }
func (t *SliceType) typeNode()           {}

type NamedType struct {
	Package string
	Name    string
	Span    diag.Span
}

func (t *NamedType) NodeSpan() diag.Span { return t.Span }
func (t *NamedType) typeNode()           {}

// --- Expressions ---

type IdentExpr struct {
	Name string
	Span diag.Span
}

func (e *IdentExpr) NodeSpan() diag.Span { return e.Span }
func (e *IdentExpr) exprNode()           {}

type IntLit struct {
	Value int64
	Raw   string
	Span  diag.Span
}

func (e *IntLit) NodeSpan() diag.Span { return e.Span }
func (e *IntLit) exprNode()           {}

type FloatLit struct {
	Value float64
	Raw   string
	Span  diag.Span
}

func (e *FloatLit) NodeSpan() diag.Span { return e.Span }
func (e *FloatLit) exprNode()           {}

type BoolLit struct {
	Value bool
	Span  diag.Span
}

func (e *BoolLit) NodeSpan() diag.Span { return e.Span }
func (e *BoolLit) exprNode()           {}

type StringLit struct {
	Value     string
	Multiline bool
	Span      diag.Span
}

func (e *StringLit) NodeSpan() diag.Span { return e.Span }
func (e *StringLit) exprNode()           {}

type InterpStringExpr struct {
	Parts     []string // len(Parts) == len(Exprs) + 1
	Exprs     []Expr
	Multiline bool
	Span      diag.Span
}

func (e *InterpStringExpr) NodeSpan() diag.Span { return e.Span }
func (e *InterpStringExpr) exprNode()           {}

type SliceLit struct {
	Elems []Expr
	Span  diag.Span
}

func (e *SliceLit) NodeSpan() diag.Span { return e.Span }
func (e *SliceLit) exprNode()           {}

type IfExpr struct {
	Cond Expr
	Then Expr
	Else Expr
	Span diag.Span
}

func (e *IfExpr) NodeSpan() diag.Span { return e.Span }
func (e *IfExpr) exprNode()           {}

type UnaryExpr struct {
	Op      lexer.TokenType
	Operand Expr
	Span    diag.Span
}

func (e *UnaryExpr) NodeSpan() diag.Span { return e.Span }
func (e *UnaryExpr) exprNode()           {}

type BinaryExpr struct {
	Left  Expr
	Op    lexer.TokenType
	Right Expr
	Span  diag.Span
}

func (e *BinaryExpr) NodeSpan() diag.Span { return e.Span }
func (e *BinaryExpr) exprNode()           {}

type SelectorExpr struct {
	X       Expr
	Sel     string
	SelSpan diag.Span
	Span    diag.Span
}

func (e *SelectorExpr) NodeSpan() diag.Span { return e.Span }
func (e *SelectorExpr) exprNode()           {}

type IndexExpr struct {
	X     Expr
	Index Expr
	Span  diag.Span
}

func (e *IndexExpr) NodeSpan() diag.Span { return e.Span }
func (e *IndexExpr) exprNode()           {}

type CallExpr struct {
	Callee Expr
	Args   []Expr
	Span   diag.Span
}

func (e *CallExpr) NodeSpan() diag.Span { return e.Span }
func (e *CallExpr) exprNode()           {}

type StructAmendExpr struct {
	Base   Expr
	Fields []*FieldDecl
	Span   diag.Span
}

func (e *StructAmendExpr) NodeSpan() diag.Span { return e.Span }
func (e *StructAmendExpr) exprNode()           {}
