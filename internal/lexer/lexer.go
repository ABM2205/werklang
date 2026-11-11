package lexer

func NewLexer(input string) *Lexer {
	return &Lexer{src: []rune(input)}
}

func (l *Lexer) NextToken() Token {
	for l.pos < len(l.src) && unicode.IsSpace(l.src[l.pos]) {
		l.pos++
	}
	if l.pos >= len(l.src) {
		return Token{Type: TokEOF}
	}

	ch := l.src[l.pos]
	switch ch {
	case '(':
		l.pos++
		return Token{TokLParen, "("}
	case ')':
		l.pos++
		return Token{TokRParen, ")"}
	case '=':
		l.pos++
		return Token{TokAssign, "="}
	case '"':
		l.pos++ // skip opening quote
		start := l.pos
		for l.pos < len(l.src) && l.src[l.pos] != '"' {
			l.pos++
		}
		str := string(l.src[start:l.pos])
		l.pos++ // skip closing quote
		return Token{TokString, str}
	default:
		if unicode.IsLetter(ch) {
			start := l.pos
			for l.pos < len(l.src) && (unicode.IsLetter(l.src[l.pos]) || unicode.IsDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
				l.pos++
			}
			return Token{TokIdent, string(l.src[start:l.pos])}
		}
		panic(fmt.Sprintf("unexpected character: %c", ch))
	}
}
