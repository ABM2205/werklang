package format

import (
	"fmt"
	"strings"

	"github.com/ABM2205/werklang/internal/diag"
	"github.com/ABM2205/werklang/internal/lexer"
	"github.com/ABM2205/werklang/internal/parser"
)

// Source parses and canonically formats Werklang source code.
// Returns an error if the source contains syntax errors (never corrupting broken files).
func Source(path, src string) (string, error) {
	prog, diags := parser.ParseFile(path, src)
	for _, d := range diags {
		if d.Severity == diag.SeverityError {
			return "", fmt.Errorf("%s", d.String())
		}
	}
	return Program(prog), nil
}

type formatter struct {
	b          strings.Builder
	comments   []lexer.Comment
	commentIdx int
	indent     int
}

func Program(prog *parser.Program) string {
	f := &formatter{
		comments: prog.Comments,
	}

	// Emit shebang first if present
	if len(f.comments) > 0 && f.comments[0].Shebang {
		f.b.WriteString(f.comments[0].Text)
		f.b.WriteByte('\n')
		f.commentIdx++
	}

	prevLine := -1
	for i, item := range prog.Items {
		itemStart := item.NodeSpan().Start.Line
		f.emitCommentsBefore(itemStart)

		if i > 0 {
			// Blank line before config/target blocks or when original source had a blank line
			_, isCfg := item.(*parser.ConfigDecl)
			_, isTgt := item.(*parser.TargetDecl)
			_, prevCfg := prog.Items[i-1].(*parser.ConfigDecl)
			_, prevTgt := prog.Items[i-1].(*parser.TargetDecl)
			if isCfg || isTgt || prevCfg || prevTgt || itemStart > prevLine+1 {
				f.b.WriteByte('\n')
			}
		}

		f.emitTopItem(item)
		f.emitTrailingCommentOnLine(item.NodeSpan().End.Line)
		f.b.WriteByte('\n')
		prevLine = item.NodeSpan().End.Line
	}

	// Remaining trailing comments
	for f.commentIdx < len(f.comments) {
		c := f.comments[f.commentIdx]
		f.commentIdx++
		if prevLine >= 0 && c.Span.Start.Line > prevLine+1 {
			f.b.WriteByte('\n')
		}
		f.writeIndent()
		f.b.WriteString(c.Text)
		f.b.WriteByte('\n')
		prevLine = c.Span.End.Line
	}

	return f.b.String()
}

func (f *formatter) writeIndent() {
	for i := 0; i < f.indent; i++ {
		f.b.WriteString("    ")
	}
}

func (f *formatter) emitCommentsBefore(line int) {
	for f.commentIdx < len(f.comments) {
		c := f.comments[f.commentIdx]
		if c.Span.Start.Line >= line {
			break
		}
		f.commentIdx++
		f.writeIndent()
		f.b.WriteString(c.Text)
		f.b.WriteByte('\n')
	}
}

func (f *formatter) emitTrailingCommentOnLine(line int) {
	if f.commentIdx < len(f.comments) {
		c := f.comments[f.commentIdx]
		if c.Span.Start.Line == line {
			f.commentIdx++
			f.b.WriteString(" ")
			f.b.WriteString(c.Text)
		}
	}
}

func (f *formatter) emitTopItem(item parser.Node) {
	switch n := item.(type) {
	case *parser.AmendsClause:
		f.writeIndent()
		f.b.WriteString("amends ")
		f.b.WriteString(escapeQuoted(n.Path))
	case *parser.ImportDecl:
		f.writeIndent()
		f.b.WriteString("import ")
		f.b.WriteString(escapeQuoted(n.Path))
		if n.Alias != "" {
			f.b.WriteString(" as ")
			f.b.WriteString(n.Alias)
		}
	case *parser.ConfigDecl:
		f.writeIndent()
		f.b.WriteString("config ")
		f.b.WriteString(n.Name)
		f.b.WriteString(" {\n")
		f.indent++
		for _, fd := range n.Fields {
			f.emitCommentsBefore(fd.Span.Start.Line)
			f.writeIndent()
			f.emitFieldDecl(fd)
			f.emitTrailingCommentOnLine(fd.Span.End.Line)
			f.b.WriteByte('\n')
		}
		f.emitCommentsBefore(n.Span.End.Line)
		f.indent--
		f.writeIndent()
		f.b.WriteByte('}')
	case *parser.TargetDecl:
		f.writeIndent()
		f.b.WriteString("target ")
		f.b.WriteString(n.Name)
		f.b.WriteString(" ")
		f.emitBlock(n.Body, n.Span.End.Line)
	case parser.Stmt:
		f.writeIndent()
		f.emitStmt(n)
	}
}

func (f *formatter) emitBlock(stmts []parser.Stmt, closeLine int) {
	f.b.WriteString("{\n")
	f.indent++
	prevLine := -1
	for i, st := range stmts {
		stLine := st.NodeSpan().Start.Line
		f.emitCommentsBefore(stLine)
		if i > 0 && prevLine >= 0 && stLine > prevLine+1 {
			f.b.WriteByte('\n')
		}
		f.writeIndent()
		f.emitStmt(st)
		f.emitTrailingCommentOnLine(st.NodeSpan().End.Line)
		f.b.WriteByte('\n')
		prevLine = st.NodeSpan().End.Line
	}
	f.emitCommentsBefore(closeLine)
	f.indent--
	f.writeIndent()
	f.b.WriteByte('}')
}

func (f *formatter) emitStmt(st parser.Stmt) {
	switch s := st.(type) {
	case *parser.FieldDecl:
		f.emitFieldDecl(s)
	case *parser.IfStmt:
		f.emitIfStmt(s)
	case *parser.ForStmt:
		f.b.WriteString("for ")
		if s.IndexVar != "" {
			f.b.WriteString(s.IndexVar)
			f.b.WriteString(", ")
		}
		f.b.WriteString(s.ItemVar)
		f.b.WriteString(" in ")
		f.emitExpr(s.Iterable)
		f.b.WriteByte(' ')
		f.emitBlock(s.Body, s.Span.End.Line)
	case *parser.ExprStmt:
		f.emitExpr(s.Expr)
	}
}

func (f *formatter) emitIfStmt(s *parser.IfStmt) {
	f.b.WriteString("if ")
	f.emitExpr(s.Cond)
	f.b.WriteByte(' ')
	elseStartLine := s.Span.End.Line
	if len(s.Else) > 0 {
		elseStartLine = s.Else[0].NodeSpan().Start.Line
	}
	f.emitBlock(s.Then, elseStartLine)
	if len(s.Else) > 0 {
		f.b.WriteString(" else ")
		if len(s.Else) == 1 {
			if nested, ok := s.Else[0].(*parser.IfStmt); ok {
				f.emitIfStmt(nested)
				return
			}
		}
		f.emitBlock(s.Else, s.Span.End.Line)
	}
}

func (f *formatter) emitFieldDecl(fd *parser.FieldDecl) {
	f.b.WriteString(fd.Name)
	if fd.Type != nil {
		f.b.WriteByte(' ')
		f.emitTypeRef(fd.Type)
	}
	if fd.Value != nil {
		f.b.WriteString(" = ")
		f.emitExpr(fd.Value)
	}
}

func (f *formatter) emitTypeRef(t parser.TypeRef) {
	switch tr := t.(type) {
	case *parser.PrimitiveType:
		f.b.WriteString(tr.Kind)
	case *parser.SliceType:
		f.b.WriteString("[]")
		f.emitTypeRef(tr.Elem)
	case *parser.NamedType:
		if tr.Package != "" {
			f.b.WriteString(tr.Package)
			f.b.WriteByte('.')
		}
		f.b.WriteString(tr.Name)
	}
}

func (f *formatter) emitExpr(e parser.Expr) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *parser.IdentExpr:
		f.b.WriteString(x.Name)
	case *parser.IntLit:
		f.b.WriteString(x.Raw)
	case *parser.FloatLit:
		f.b.WriteString(x.Raw)
	case *parser.BoolLit:
		if x.Value {
			f.b.WriteString("true")
		} else {
			f.b.WriteString("false")
		}
	case *parser.StringLit:
		if x.Multiline {
			f.b.WriteString("\"\"\"\n")
			f.b.WriteString(escapeStringPart(x.Value, true))
			f.b.WriteString("\"\"\"")
		} else {
			f.b.WriteString(escapeQuoted(x.Value))
		}
	case *parser.InterpStringExpr:
		if x.Multiline {
			f.b.WriteString("\"\"\"\n")
		} else {
			f.b.WriteByte('"')
		}
		for i, part := range x.Parts {
			f.b.WriteString(escapeStringPart(part, x.Multiline))
			if i < len(x.Exprs) {
				f.b.WriteString(`\(`)
				f.emitExpr(x.Exprs[i])
				f.b.WriteByte(')')
			}
		}
		if x.Multiline {
			f.b.WriteString("\"\"\"")
		} else {
			f.b.WriteByte('"')
		}
	case *parser.SliceLit:
		f.b.WriteByte('[')
		for i, el := range x.Elems {
			if i > 0 {
				f.b.WriteString(", ")
			}
			f.emitExpr(el)
		}
		f.b.WriteByte(']')
	case *parser.IfExpr:
		f.b.WriteString("if ")
		f.emitExpr(x.Cond)
		f.b.WriteString(" then ")
		f.emitExpr(x.Then)
		f.b.WriteString(" else ")
		f.emitExpr(x.Else)
	case *parser.UnaryExpr:
		f.b.WriteString(string(x.Op))
		f.emitExpr(x.Operand)
	case *parser.BinaryExpr:
		f.emitChildExpr(x.Left, x.Op, false)
		f.b.WriteByte(' ')
		f.b.WriteString(string(x.Op))
		f.b.WriteByte(' ')
		f.emitChildExpr(x.Right, x.Op, true)
	case *parser.SelectorExpr:
		f.emitExpr(x.X)
		f.b.WriteByte('.')
		f.b.WriteString(x.Sel)
	case *parser.IndexExpr:
		f.emitExpr(x.X)
		f.b.WriteByte('[')
		f.emitExpr(x.Index)
		f.b.WriteByte(']')
	case *parser.CallExpr:
		f.emitExpr(x.Callee)
		f.b.WriteByte('(')
		for i, arg := range x.Args {
			if i > 0 {
				f.b.WriteString(", ")
			}
			f.emitExpr(arg)
		}
		f.b.WriteByte(')')
	case *parser.StructAmendExpr:
		f.emitExpr(x.Base)
		if len(x.Fields) == 0 {
			f.b.WriteString(" {}")
			return
		}
		f.b.WriteString(" {\n")
		f.indent++
		for _, fd := range x.Fields {
			f.emitCommentsBefore(fd.Span.Start.Line)
			f.writeIndent()
			f.emitFieldDecl(fd)
			f.emitTrailingCommentOnLine(fd.Span.End.Line)
			f.b.WriteByte('\n')
		}
		f.emitCommentsBefore(x.Span.End.Line)
		f.indent--
		f.writeIndent()
		f.b.WriteByte('}')
	}
}

func (f *formatter) emitChildExpr(child parser.Expr, parentOp lexer.TokenType, rightSide bool) {
	if bin, ok := child.(*parser.BinaryExpr); ok {
		if binPrec(bin.Op) < binPrec(parentOp) || (rightSide && binPrec(bin.Op) == binPrec(parentOp)) {
			f.b.WriteByte('(')
			f.emitExpr(child)
			f.b.WriteByte(')')
			return
		}
	}
	f.emitExpr(child)
}

func binPrec(op lexer.TokenType) int {
	switch op {
	case lexer.TokOr:
		return 1
	case lexer.TokAnd:
		return 2
	case lexer.TokEq, lexer.TokNeq:
		return 3
	case lexer.TokLt, lexer.TokLte, lexer.TokGt, lexer.TokGte:
		return 4
	case lexer.TokPlus, lexer.TokMinus:
		return 5
	case lexer.TokStar, lexer.TokSlash, lexer.TokPercent:
		return 6
	default:
		return 0
	}
}

func escapeQuoted(s string) string {
	return `"` + escapeStringPart(s, false) + `"`
}

func escapeStringPart(s string, multiline bool) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			if multiline {
				b.WriteRune('"')
			} else {
				b.WriteString(`\"`)
			}
		case '\n':
			if multiline {
				b.WriteByte('\n')
			} else {
				b.WriteString(`\n`)
			}
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
