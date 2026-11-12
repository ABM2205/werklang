package parser

import (
	"fmt"
	"strconv"

	"github.com/ABM2205/werklang/internal/diag"
	"github.com/ABM2205/werklang/internal/lexer"
)

const (
	precLowest = iota
	precOr
	precAnd
	precEquals
	precCompare
	precSum
	precProduct
	precPrefix
	precPostfix
)

func tokenPrecedence(t lexer.TokenType) int {
	switch t {
	case lexer.TokOr:
		return precOr
	case lexer.TokAnd:
		return precAnd
	case lexer.TokEq, lexer.TokNeq:
		return precEquals
	case lexer.TokLt, lexer.TokLte, lexer.TokGt, lexer.TokGte:
		return precCompare
	case lexer.TokPlus, lexer.TokMinus:
		return precSum
	case lexer.TokStar, lexer.TokSlash, lexer.TokPercent:
		return precProduct
	case lexer.TokDot, lexer.TokLParen, lexer.TokLBracket, lexer.TokLBrace:
		return precPostfix
	default:
		return precLowest
	}
}

type Parser struct {
	l              *lexer.Lexer
	file           string
	prev           lexer.Token
	cur            lexer.Token
	peek           lexer.Token
	peek2          lexer.Token
	allowStructLit bool
	Diagnostics    []diag.Diagnostic
}

func NewParser(l *lexer.Lexer) *Parser {
	p := &Parser{
		l:              l,
		allowStructLit: true,
	}
	p.next()
	p.next()
	p.next()
	p.file = p.cur.Span.File
	return p
}

func ParseFile(path, src string) (*Program, []diag.Diagnostic) {
	l := lexer.NewFileLexer(path, src)
	p := NewParser(l)
	prog := p.ParseProgram()
	prog.Path = path
	prog.Comments = l.Comments
	var allDiags []diag.Diagnostic
	allDiags = append(allDiags, l.Diagnostics...)
	allDiags = append(allDiags, p.Diagnostics...)
	return prog, allDiags
}

func (p *Parser) next() {
	p.prev = p.cur
	p.cur = p.peek
	p.peek = p.peek2
	p.peek2 = p.l.NextToken()
}

func (p *Parser) addError(span diag.Span, format string, args ...any) {
	p.Diagnostics = append(p.Diagnostics, diag.Diagnostic{
		Span:     span,
		Severity: diag.SeverityError,
		Message:  fmt.Sprintf(format, args...),
	})
}

func (p *Parser) expect(t lexer.TokenType) (lexer.Token, bool) {
	if p.cur.Type == t {
		tok := p.cur
		p.next()
		return tok, true
	}
	p.addError(p.cur.Span, "expected %s, got %s (%q)", t, p.cur.Type, p.cur.Literal)
	return lexer.Token{Type: t, Span: p.cur.Span}, false
}

func (p *Parser) syncTopOrBlock(inBlock bool) {
	startLine := p.cur.Span.Start.Line
	p.next()
	for p.cur.Type != lexer.TokEOF {
		if inBlock && p.cur.Type == lexer.TokRBrace {
			return
		}
		switch p.cur.Type {
		case lexer.TokKwAmends, lexer.TokKwImport, lexer.TokKwConfig, lexer.TokKwTarget, lexer.TokKwIf, lexer.TokKwFor:
			return
		}
		if p.cur.Span.Start.Line > startLine {
			return
		}
		p.next()
	}
}

func (p *Parser) ParseProgram() *Program {
	startPos := p.cur.Span.Start
	prog := &Program{}

	for p.cur.Type != lexer.TokEOF {
		switch p.cur.Type {
		case lexer.TokKwAmends:
			am := p.parseAmends()
			if prog.Amends != nil {
				p.addError(am.Span, "duplicate amends clause")
			} else if len(prog.Items) > 0 {
				p.addError(am.Span, "amends clause must appear at the top of the file")
			}
			prog.Amends = am
			prog.Items = append(prog.Items, am)
		case lexer.TokKwImport:
			imp := p.parseImport()
			prog.Imports = append(prog.Imports, imp)
			prog.Items = append(prog.Items, imp)
		case lexer.TokKwConfig:
			cfg := p.parseConfig()
			prog.Configs = append(prog.Configs, cfg)
			prog.Items = append(prog.Items, cfg)
		case lexer.TokKwTarget:
			tgt := p.parseTarget()
			prog.Targets = append(prog.Targets, tgt)
			prog.Items = append(prog.Items, tgt)
		default:
			stmt := p.parseStmt(false)
			if stmt != nil {
				if fd, ok := stmt.(*FieldDecl); ok {
					prog.Fields = append(prog.Fields, fd)
				}
				prog.Stmts = append(prog.Stmts, stmt)
				prog.Items = append(prog.Items, stmt)
			}
		}
	}

	prog.Span = diag.Span{
		File:  p.file,
		Start: startPos,
		End:   p.cur.Span.End,
	}
	return prog
}

func (p *Parser) parseAmends() *AmendsClause {
	kw := p.cur
	p.next() // consume 'amends'
	strTok, _ := p.expect(lexer.TokString)
	return &AmendsClause{
		Path:     strTok.Literal,
		PathSpan: strTok.Span,
		Span:     diag.Span{File: p.file, Start: kw.Span.Start, End: strTok.Span.End},
	}
}

func (p *Parser) parseImport() *ImportDecl {
	kw := p.cur
	p.next() // consume 'import'
	strTok, _ := p.expect(lexer.TokString)
	endPos := strTok.Span.End
	var alias string
	var aliasSpan diag.Span
	if p.cur.Type == lexer.TokKwAs {
		p.next() // consume 'as'
		idTok, ok := p.expect(lexer.TokIdent)
		if ok {
			alias = idTok.Literal
			aliasSpan = idTok.Span
			endPos = idTok.Span.End
		}
	}
	return &ImportDecl{
		Path:      strTok.Literal,
		PathSpan:  strTok.Span,
		Alias:     alias,
		AliasSpan: aliasSpan,
		Span:      diag.Span{File: p.file, Start: kw.Span.Start, End: endPos},
	}
}

func (p *Parser) parseConfig() *ConfigDecl {
	kw := p.cur
	p.next() // consume 'config'
	nameTok, _ := p.expect(lexer.TokIdent)
	p.expect(lexer.TokLBrace)

	var fields []*FieldDecl
	for p.cur.Type != lexer.TokRBrace && p.cur.Type != lexer.TokEOF {
		if p.isFieldDeclStart() {
			fd := p.parseFieldDecl()
			fields = append(fields, fd)
		} else {
			p.addError(p.cur.Span, "expected field declaration in config block, got %s (%q)", p.cur.Type, p.cur.Literal)
			p.syncTopOrBlock(true)
		}
	}
	endTok, _ := p.expect(lexer.TokRBrace)
	return &ConfigDecl{
		Name:     nameTok.Literal,
		NameSpan: nameTok.Span,
		Fields:   fields,
		Span:     diag.Span{File: p.file, Start: kw.Span.Start, End: endTok.Span.End},
	}
}

func (p *Parser) parseTarget() *TargetDecl {
	kw := p.cur
	p.next() // consume 'target'
	nameTok, _ := p.expect(lexer.TokIdent)
	body, endPos := p.parseBlock()
	return &TargetDecl{
		Name:     nameTok.Literal,
		NameSpan: nameTok.Span,
		Body:     body,
		Span:     diag.Span{File: p.file, Start: kw.Span.Start, End: endPos},
	}
}

func (p *Parser) parseBlock() ([]Stmt, diag.Pos) {
	p.expect(lexer.TokLBrace)
	var stmts []Stmt
	for p.cur.Type != lexer.TokRBrace && p.cur.Type != lexer.TokEOF {
		st := p.parseStmt(true)
		if st != nil {
			stmts = append(stmts, st)
		}
	}
	endTok, _ := p.expect(lexer.TokRBrace)
	return stmts, endTok.Span.End
}

func (p *Parser) isFieldDeclStart() bool {
	if p.cur.Type != lexer.TokIdent {
		return false
	}
	if p.peek.Type == lexer.TokAssign {
		return true
	}
	switch p.peek.Type {
	case lexer.TokKwInt, lexer.TokKwFloat, lexer.TokKwString, lexer.TokKwBool:
		return true
	case lexer.TokLBracket:
		return p.peek.Span.Start.Line == p.cur.Span.Start.Line && p.peek2.Type == lexer.TokRBracket
	case lexer.TokIdent:
		return p.peek.Span.Start.Line == p.cur.Span.Start.Line
	}
	return false
}

func (p *Parser) parseStmt(inBlock bool) Stmt {
	switch p.cur.Type {
	case lexer.TokKwIf:
		return p.parseIfStmt()
	case lexer.TokKwFor:
		return p.parseForStmt()
	default:
		if p.isFieldDeclStart() {
			return p.parseFieldDecl()
		}
		startCur := p.cur
		expr := p.parseExpr(precLowest)
		if expr == nil {
			if p.cur.Span.Start == startCur.Span.Start {
				p.syncTopOrBlock(inBlock)
			}
			return nil
		}
		return &ExprStmt{
			Expr: expr,
			Span: expr.NodeSpan(),
		}
	}
}

func (p *Parser) parseFieldDecl() *FieldDecl {
	nameTok := p.cur
	p.next() // consume field/variable name

	var typ TypeRef
	endPos := nameTok.Span.End
	if p.cur.Type != lexer.TokAssign {
		typ = p.parseTypeRef()
		if typ != nil {
			endPos = typ.NodeSpan().End
		}
	}

	var val Expr
	if p.cur.Type == lexer.TokAssign {
		assignTok := p.cur
		p.next() // consume '='
		endPos = assignTok.Span.End
		val = p.parseExpr(precLowest)
		if val != nil {
			endPos = val.NodeSpan().End
		}
	}

	return &FieldDecl{
		Name:     nameTok.Literal,
		NameSpan: nameTok.Span,
		Type:     typ,
		Value:    val,
		Span:     diag.Span{File: p.file, Start: nameTok.Span.Start, End: endPos},
	}
}

func (p *Parser) parseTypeRef() TypeRef {
	switch p.cur.Type {
	case lexer.TokKwInt, lexer.TokKwFloat, lexer.TokKwString, lexer.TokKwBool:
		tok := p.cur
		p.next()
		return &PrimitiveType{Kind: tok.Literal, Span: tok.Span}
	case lexer.TokLBracket:
		lb := p.cur
		p.next() // '['
		p.expect(lexer.TokRBracket)
		elem := p.parseTypeRef()
		endPos := lb.Span.End
		if elem != nil {
			endPos = elem.NodeSpan().End
		}
		return &SliceType{
			Elem: elem,
			Span: diag.Span{File: p.file, Start: lb.Span.Start, End: endPos},
		}
	case lexer.TokIdent:
		first := p.cur
		p.next()
		if p.cur.Type == lexer.TokDot && p.peek.Type == lexer.TokIdent {
			p.next() // '.'
			second := p.cur
			p.next()
			return &NamedType{
				Package: first.Literal,
				Name:    second.Literal,
				Span:    diag.Span{File: p.file, Start: first.Span.Start, End: second.Span.End},
			}
		}
		return &NamedType{
			Name: first.Literal,
			Span: first.Span,
		}
	default:
		p.addError(p.cur.Span, "expected type, got %s (%q)", p.cur.Type, p.cur.Literal)
		return nil
	}
}

func (p *Parser) parseIfStmt() *IfStmt {
	kw := p.cur
	p.next() // consume 'if'

	saved := p.allowStructLit
	p.allowStructLit = false
	cond := p.parseExpr(precLowest)
	p.allowStructLit = saved

	thenBody, endPos := p.parseBlock()
	var elseBody []Stmt
	if p.cur.Type == lexer.TokKwElse {
		p.next() // consume 'else'
		if p.cur.Type == lexer.TokKwIf {
			nested := p.parseIfStmt()
			elseBody = []Stmt{nested}
			endPos = nested.Span.End
		} else {
			elseBody, endPos = p.parseBlock()
		}
	}
	return &IfStmt{
		Cond: cond,
		Then: thenBody,
		Else: elseBody,
		Span: diag.Span{File: p.file, Start: kw.Span.Start, End: endPos},
	}
}

func (p *Parser) parseForStmt() *ForStmt {
	kw := p.cur
	p.next() // consume 'for'

	firstTok, _ := p.expect(lexer.TokIdent)
	var indexVar string
	var indexSpan diag.Span
	itemVar := firstTok.Literal
	itemSpan := firstTok.Span

	if p.cur.Type == lexer.TokComma {
		p.next() // consume ','
		secondTok, _ := p.expect(lexer.TokIdent)
		indexVar = firstTok.Literal
		indexSpan = firstTok.Span
		itemVar = secondTok.Literal
		itemSpan = secondTok.Span
	}

	p.expect(lexer.TokKwIn)

	saved := p.allowStructLit
	p.allowStructLit = false
	iterable := p.parseExpr(precLowest)
	p.allowStructLit = saved

	body, endPos := p.parseBlock()
	return &ForStmt{
		IndexVar:  indexVar,
		IndexSpan: indexSpan,
		ItemVar:   itemVar,
		ItemSpan:  itemSpan,
		Iterable:  iterable,
		Body:      body,
		Span:      diag.Span{File: p.file, Start: kw.Span.Start, End: endPos},
	}
}

func (p *Parser) parseExpr(minPrec int) Expr {
	left := p.parsePrefix()
	if left == nil {
		return nil
	}

	for {
		prec := tokenPrecedence(p.cur.Type)
		if prec <= minPrec {
			break
		}

		switch p.cur.Type {
		case lexer.TokDot:
			p.next() // consume '.'
			if p.cur.Type == lexer.TokIdent {
				sel := p.cur
				p.next()
				left = &SelectorExpr{
					X:       left,
					Sel:     sel.Literal,
					SelSpan: sel.Span,
					Span:    diag.Span{File: p.file, Start: left.NodeSpan().Start, End: sel.Span.End},
				}
			} else {
				// Incomplete selector (e.g. `app.` during LSP completion)
				dotEnd := p.prev.Span.End
				p.addError(p.prev.Span, "expected field or function name after '.'")
				emptySpan := diag.Span{File: p.file, Start: dotEnd, End: dotEnd}
				left = &SelectorExpr{
					X:       left,
					Sel:     "",
					SelSpan: emptySpan,
					Span:    diag.Span{File: p.file, Start: left.NodeSpan().Start, End: dotEnd},
				}
				return left
			}

		case lexer.TokLParen:
			// Only attach call `(` if on same line as callee
			if p.cur.Span.Start.Line > left.NodeSpan().End.Line {
				return left
			}
			left = p.parseCallExpr(left)

		case lexer.TokLBracket:
			if p.cur.Span.Start.Line > left.NodeSpan().End.Line {
				return left
			}
			left = p.parseIndexExpr(left)

		case lexer.TokLBrace:
			if !p.allowStructLit || !isAmendableBase(left) || p.cur.Span.Start.Line > left.NodeSpan().End.Line {
				return left
			}
			left = p.parseStructAmendExpr(left)

		default:
			op := p.cur.Type
			p.next()
			right := p.parseExpr(prec)
			endPos := p.prev.Span.End
			if right != nil {
				endPos = right.NodeSpan().End
			}
			left = &BinaryExpr{
				Left:  left,
				Op:    op,
				Right: right,
				Span:  diag.Span{File: p.file, Start: left.NodeSpan().Start, End: endPos},
			}
		}
	}
	return left
}

func isAmendableBase(e Expr) bool {
	switch e.(type) {
	case *IdentExpr, *SelectorExpr, *IndexExpr, *StructAmendExpr:
		return true
	default:
		return false
	}
}

func (p *Parser) parsePrefix() Expr {
	switch p.cur.Type {
	case lexer.TokIdent:
		tok := p.cur
		p.next()
		return &IdentExpr{Name: tok.Literal, Span: tok.Span}

	case lexer.TokIntLit:
		tok := p.cur
		p.next()
		val, err := strconv.ParseInt(tok.Literal, 10, 64)
		if err != nil {
			p.addError(tok.Span, "invalid integer literal %q: %v", tok.Literal, err)
		}
		return &IntLit{Value: val, Raw: tok.Literal, Span: tok.Span}

	case lexer.TokFloatLit:
		tok := p.cur
		p.next()
		val, err := strconv.ParseFloat(tok.Literal, 64)
		if err != nil {
			p.addError(tok.Span, "invalid float literal %q: %v", tok.Literal, err)
		}
		return &FloatLit{Value: val, Raw: tok.Literal, Span: tok.Span}

	case lexer.TokKwTrue:
		tok := p.cur
		p.next()
		return &BoolLit{Value: true, Span: tok.Span}

	case lexer.TokKwFalse:
		tok := p.cur
		p.next()
		return &BoolLit{Value: false, Span: tok.Span}

	case lexer.TokString:
		tok := p.cur
		p.next()
		return &StringLit{Value: tok.Literal, Multiline: tok.Multiline, Span: tok.Span}

	case lexer.TokStringStart:
		return p.parseInterpString()

	case lexer.TokLBracket:
		return p.parseSliceLit()

	case lexer.TokLParen:
		lp := p.cur
		p.next() // '('
		saved := p.allowStructLit
		p.allowStructLit = true
		inner := p.parseExpr(precLowest)
		p.allowStructLit = saved
		rp, _ := p.expect(lexer.TokRParen)
		if inner == nil {
			return &IdentExpr{Name: "", Span: diag.Span{File: p.file, Start: lp.Span.Start, End: rp.Span.End}}
		}
		return inner

	case lexer.TokBang, lexer.TokMinus:
		opTok := p.cur
		p.next()
		operand := p.parseExpr(precPrefix)
		endPos := opTok.Span.End
		if operand != nil {
			endPos = operand.NodeSpan().End
		}
		return &UnaryExpr{
			Op:      opTok.Type,
			Operand: operand,
			Span:    diag.Span{File: p.file, Start: opTok.Span.Start, End: endPos},
		}

	case lexer.TokKwIf:
		return p.parseIfExpr()

	default:
		p.addError(p.cur.Span, "unexpected token %s (%q)", p.cur.Type, p.cur.Literal)
		return nil
	}
}

func (p *Parser) parseInterpString() Expr {
	startTok := p.cur
	multiline := startTok.Multiline
	parts := []string{startTok.Literal}
	var exprs []Expr
	p.next() // consume TokStringStart

	saved := p.allowStructLit
	p.allowStructLit = true
	defer func() { p.allowStructLit = saved }()

	for {
		expr := p.parseExpr(precLowest)
		if expr != nil {
			exprs = append(exprs, expr)
		} else {
			exprs = append(exprs, &StringLit{Value: "", Span: p.cur.Span})
		}

		if p.cur.Type == lexer.TokStringMid {
			parts = append(parts, p.cur.Literal)
			p.next()
			continue
		}
		if p.cur.Type == lexer.TokStringEnd {
			endTok := p.cur
			parts = append(parts, endTok.Literal)
			p.next()
			return &InterpStringExpr{
				Parts:     parts,
				Exprs:     exprs,
				Multiline: multiline,
				Span:      diag.Span{File: p.file, Start: startTok.Span.Start, End: endTok.Span.End},
			}
		}

		p.addError(p.cur.Span, "unterminated string interpolation; expected ')'")
		parts = append(parts, "")
		return &InterpStringExpr{
			Parts:     parts,
			Exprs:     exprs,
			Multiline: multiline,
			Span:      diag.Span{File: p.file, Start: startTok.Span.Start, End: p.cur.Span.End},
		}
	}
}

func (p *Parser) parseSliceLit() *SliceLit {
	lb := p.cur
	p.next() // consume '['

	saved := p.allowStructLit
	p.allowStructLit = true
	defer func() { p.allowStructLit = saved }()

	var elems []Expr
	for p.cur.Type != lexer.TokRBracket && p.cur.Type != lexer.TokEOF {
		el := p.parseExpr(precLowest)
		if el != nil {
			elems = append(elems, el)
		}
		if p.cur.Type == lexer.TokComma {
			p.next()
			continue
		}
		break
	}
	rb, _ := p.expect(lexer.TokRBracket)
	return &SliceLit{
		Elems: elems,
		Span:  diag.Span{File: p.file, Start: lb.Span.Start, End: rb.Span.End},
	}
}

func (p *Parser) parseIfExpr() *IfExpr {
	kw := p.cur
	p.next() // consume 'if'

	saved := p.allowStructLit
	p.allowStructLit = true
	defer func() { p.allowStructLit = saved }()

	cond := p.parseExpr(precLowest)
	p.expect(lexer.TokKwThen)
	thenExpr := p.parseExpr(precLowest)
	p.expect(lexer.TokKwElse)
	elseExpr := p.parseExpr(precLowest)

	endPos := kw.Span.End
	if elseExpr != nil {
		endPos = elseExpr.NodeSpan().End
	}
	return &IfExpr{
		Cond: cond,
		Then: thenExpr,
		Else: elseExpr,
		Span: diag.Span{File: p.file, Start: kw.Span.Start, End: endPos},
	}
}

func (p *Parser) parseCallExpr(callee Expr) *CallExpr {
	p.next() // consume '('
	saved := p.allowStructLit
	p.allowStructLit = true
	defer func() { p.allowStructLit = saved }()

	var args []Expr
	for p.cur.Type != lexer.TokRParen && p.cur.Type != lexer.TokEOF {
		arg := p.parseExpr(precLowest)
		if arg != nil {
			args = append(args, arg)
		}
		if p.cur.Type == lexer.TokComma {
			p.next()
			continue
		}
		break
	}
	rp, _ := p.expect(lexer.TokRParen)
	return &CallExpr{
		Callee: callee,
		Args:   args,
		Span:   diag.Span{File: p.file, Start: callee.NodeSpan().Start, End: rp.Span.End},
	}
}

func (p *Parser) parseIndexExpr(base Expr) *IndexExpr {
	p.next() // consume '['
	saved := p.allowStructLit
	p.allowStructLit = true
	idx := p.parseExpr(precLowest)
	p.allowStructLit = saved
	rb, _ := p.expect(lexer.TokRBracket)
	return &IndexExpr{
		X:     base,
		Index: idx,
		Span:  diag.Span{File: p.file, Start: base.NodeSpan().Start, End: rb.Span.End},
	}
}

func (p *Parser) parseStructAmendExpr(base Expr) *StructAmendExpr {
	p.next() // consume '{'
	saved := p.allowStructLit
	p.allowStructLit = true
	defer func() { p.allowStructLit = saved }()

	var fields []*FieldDecl
	for p.cur.Type != lexer.TokRBrace && p.cur.Type != lexer.TokEOF {
		if p.cur.Type == lexer.TokIdent && p.peek.Type == lexer.TokAssign {
			fd := p.parseFieldDecl()
			fields = append(fields, fd)
			if p.cur.Type == lexer.TokComma {
				p.next()
			}
		} else {
			p.addError(p.cur.Span, "expected 'field = value' in struct block, got %s (%q)", p.cur.Type, p.cur.Literal)
			p.syncTopOrBlock(true)
		}
	}
	rb, _ := p.expect(lexer.TokRBrace)
	return &StructAmendExpr{
		Base:   base,
		Fields: fields,
		Span:   diag.Span{File: p.file, Start: base.NodeSpan().Start, End: rb.Span.End},
	}
}
