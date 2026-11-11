package parser

type Parser struct {
	l    *Lexer
	cur  Token
	peek Token
}

func NewParser(l *Lexer) *Parser {
	p := &Parser{l: l}
	p.next()
	p.next()
	return p
}

func (p *Parser) next() {
	p.cur = p.peek
	p.peek = p.l.NextToken()
}

func (p *Parser) expect(t TokenType) Token {
	if p.cur.Type != t {
		panic(fmt.Sprintf("expected %s, got %s (%q)", t, p.cur.Type, p.cur.Literal))
	}
	tok := p.cur
	p.next()
	return tok
}

func (p *Parser) ParseProgram() *Program {
	prog := &Program{}
	for p.cur.Type != TokEOF {
		if p.cur.Type == TokIdent && p.cur.Literal == "print" {
			p.next() // consume 'print'
			p.expect(TokLParen)
			varName := p.expect(TokIdent).Literal
			p.expect(TokRParen)
			prog.Prints = append(prog.Prints, PrintStmt{VarName: varName})
		} else if p.cur.Type == TokIdent {
			name := p.expect(TokIdent).Literal
			p.expect(TokAssign)
			val := p.expect(TokString).Literal
			prog.Vars = append(prog.Vars, VarDecl{Name: name, Value: val})
		} else {
			panic("unexpected token: " + p.cur.Literal)
		}
	}
	return prog
}
