package lexer

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ABM2205/werklang/internal/diag"
)

type interpState struct {
	parenDepth int
	multiline  bool
}

type Lexer struct {
	file        string
	src         string
	pos         int
	line        int
	col         int
	interpStack []interpState

	Comments    []Comment
	Diagnostics []diag.Diagnostic
}

func NewLexer(input string) *Lexer {
	return NewFileLexer("", input)
}

func NewFileLexer(file, input string) *Lexer {
	l := &Lexer{
		file: file,
		src:  input,
	}
	// Optional shebang on first line: #!/usr/bin/env werk
	if strings.HasPrefix(l.src, "#!") {
		start := l.curPos()
		for l.pos < len(l.src) && l.src[l.pos] != '\n' {
			l.advanceRune()
		}
		end := l.curPos()
		l.Comments = append(l.Comments, Comment{
			Text:    l.src[start.Offset:end.Offset],
			Shebang: true,
			Span:    diag.Span{File: l.file, Start: start, End: end},
		})
	}
	return l
}

func (l *Lexer) curPos() diag.Pos {
	return diag.Pos{Offset: l.pos, Line: l.line, Col: l.col}
}

func (l *Lexer) peekRune() (rune, int) {
	if l.pos >= len(l.src) {
		return 0, 0
	}
	return utf8.DecodeRuneInString(l.src[l.pos:])
}

func (l *Lexer) peekByte(offset int) byte {
	idx := l.pos + offset
	if idx >= len(l.src) {
		return 0
	}
	return l.src[idx]
}

func (l *Lexer) advanceRune() rune {
	if l.pos >= len(l.src) {
		return 0
	}
	r, w := utf8.DecodeRuneInString(l.src[l.pos:])
	l.pos += w
	if r == '\n' {
		l.line++
		l.col = 0
	} else if r > 0xFFFF {
		l.col += 2 // UTF-16 surrogate pair
	} else {
		l.col++
	}
	return r
}

func (l *Lexer) addError(span diag.Span, msg string) {
	l.Diagnostics = append(l.Diagnostics, diag.Diagnostic{
		Span:     span,
		Severity: diag.SeverityError,
		Message:  msg,
	})
}

func (l *Lexer) skipWhitespaceAndComments() {
	for l.pos < len(l.src) {
		r, _ := l.peekRune()
		if unicode.IsSpace(r) {
			l.advanceRune()
			continue
		}
		if l.peekByte(0) == '/' && l.peekByte(1) == '/' {
			start := l.curPos()
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.advanceRune()
			}
			end := l.curPos()
			l.Comments = append(l.Comments, Comment{
				Text: l.src[start.Offset:end.Offset],
				Span: diag.Span{File: l.file, Start: start, End: end},
			})
			continue
		}
		break
	}
}

func (l *Lexer) NextToken() Token {
	l.skipWhitespaceAndComments()
	start := l.curPos()
	if l.pos >= len(l.src) {
		return Token{Type: TokEOF, Span: diag.Span{File: l.file, Start: start, End: start}}
	}

	ch := l.peekByte(0)
	switch ch {
	case '(':
		l.advanceRune()
		if len(l.interpStack) > 0 {
			l.interpStack[len(l.interpStack)-1].parenDepth++
		}
		return l.makeTok(TokLParen, "(", start)
	case ')':
		if len(l.interpStack) > 0 && l.interpStack[len(l.interpStack)-1].parenDepth == 0 {
			// Closing `)` of a `\(...)` string interpolation: resume scanning enclosing string.
			top := l.interpStack[len(l.interpStack)-1]
			l.advanceRune() // consume ')'
			return l.scanStringSegment(start, true, top.multiline)
		}
		l.advanceRune()
		if len(l.interpStack) > 0 {
			l.interpStack[len(l.interpStack)-1].parenDepth--
		}
		return l.makeTok(TokRParen, ")", start)
	case '{':
		l.advanceRune()
		return l.makeTok(TokLBrace, "{", start)
	case '}':
		l.advanceRune()
		return l.makeTok(TokRBrace, "}", start)
	case '[':
		l.advanceRune()
		return l.makeTok(TokLBracket, "[", start)
	case ']':
		l.advanceRune()
		return l.makeTok(TokRBracket, "]", start)
	case ',':
		l.advanceRune()
		return l.makeTok(TokComma, ",", start)
	case '.':
		l.advanceRune()
		return l.makeTok(TokDot, ".", start)
	case '+':
		l.advanceRune()
		return l.makeTok(TokPlus, "+", start)
	case '-':
		l.advanceRune()
		return l.makeTok(TokMinus, "-", start)
	case '*':
		l.advanceRune()
		return l.makeTok(TokStar, "*", start)
	case '/':
		l.advanceRune()
		return l.makeTok(TokSlash, "/", start)
	case '%':
		l.advanceRune()
		return l.makeTok(TokPercent, "%", start)
	case '=':
		l.advanceRune()
		if l.peekByte(0) == '=' {
			l.advanceRune()
			return l.makeTok(TokEq, "==", start)
		}
		return l.makeTok(TokAssign, "=", start)
	case '!':
		l.advanceRune()
		if l.peekByte(0) == '=' {
			l.advanceRune()
			return l.makeTok(TokNeq, "!=", start)
		}
		return l.makeTok(TokBang, "!", start)
	case '<':
		l.advanceRune()
		if l.peekByte(0) == '=' {
			l.advanceRune()
			return l.makeTok(TokLte, "<=", start)
		}
		return l.makeTok(TokLt, "<", start)
	case '>':
		l.advanceRune()
		if l.peekByte(0) == '=' {
			l.advanceRune()
			return l.makeTok(TokGte, ">=", start)
		}
		return l.makeTok(TokGt, ">", start)
	case '&':
		l.advanceRune()
		if l.peekByte(0) == '&' {
			l.advanceRune()
			return l.makeTok(TokAnd, "&&", start)
		}
		span := diag.Span{File: l.file, Start: start, End: l.curPos()}
		l.addError(span, "unexpected '&' (did you mean '&&'?)")
		return Token{Type: TokIllegal, Literal: "&", Span: span}
	case '|':
		l.advanceRune()
		if l.peekByte(0) == '|' {
			l.advanceRune()
			return l.makeTok(TokOr, "||", start)
		}
		span := diag.Span{File: l.file, Start: start, End: l.curPos()}
		l.addError(span, "unexpected '|' (did you mean '||'?)")
		return Token{Type: TokIllegal, Literal: "|", Span: span}
	case '"':
		if l.peekByte(1) == '"' && l.peekByte(2) == '"' {
			l.advanceRune()
			l.advanceRune()
			l.advanceRune()
			// Strip optional immediate newline after opening """
			if l.peekByte(0) == '\r' && l.peekByte(1) == '\n' {
				l.advanceRune()
				l.advanceRune()
			} else if l.peekByte(0) == '\n' {
				l.advanceRune()
			}
			return l.scanStringSegment(start, false, true)
		}
		l.advanceRune() // consume opening '"'
		return l.scanStringSegment(start, false, false)
	}

	r, _ := l.peekRune()
	if unicode.IsDigit(r) {
		return l.scanNumber(start)
	}
	if isIdentStart(r) {
		return l.scanIdent(start)
	}

	bad := l.advanceRune()
	span := diag.Span{File: l.file, Start: start, End: l.curPos()}
	l.addError(span, fmt.Sprintf("unexpected character %q", bad))
	return Token{Type: TokIllegal, Literal: string(bad), Span: span}
}

func (l *Lexer) makeTok(typ TokenType, lit string, start diag.Pos) Token {
	return Token{
		Type:    typ,
		Literal: lit,
		Span:    diag.Span{File: l.file, Start: start, End: l.curPos()},
	}
}

func (l *Lexer) scanIdent(start diag.Pos) Token {
	for l.pos < len(l.src) {
		r, _ := l.peekRune()
		if !isIdentPart(r) {
			break
		}
		l.advanceRune()
	}
	lit := l.src[start.Offset:l.pos]
	return l.makeTok(LookupIdent(lit), lit, start)
}

func (l *Lexer) scanNumber(start diag.Pos) Token {
	tokType := TokIntLit
	for l.pos < len(l.src) && (unicode.IsDigit(rune(l.peekByte(0))) || l.peekByte(0) == '_') {
		l.advanceRune()
	}
	if l.peekByte(0) == '.' && unicode.IsDigit(rune(l.peekByte(1))) {
		tokType = TokFloatLit
		l.advanceRune() // consume '.'
		for l.pos < len(l.src) && (unicode.IsDigit(rune(l.peekByte(0))) || l.peekByte(0) == '_') {
			l.advanceRune()
		}
	}
	raw := strings.ReplaceAll(l.src[start.Offset:l.pos], "_", "")
	return l.makeTok(tokType, raw, start)
}

func (l *Lexer) scanStringSegment(start diag.Pos, continuation bool, multiline bool) Token {
	var b strings.Builder
	for l.pos < len(l.src) {
		if multiline {
			if l.peekByte(0) == '"' && l.peekByte(1) == '"' && l.peekByte(2) == '"' {
				l.advanceRune()
				l.advanceRune()
				l.advanceRune()
				if continuation {
					l.interpStack = l.interpStack[:len(l.interpStack)-1]
					tok := l.makeTok(TokStringEnd, b.String(), start)
					tok.Multiline = true
					return tok
				}
				tok := l.makeTok(TokString, b.String(), start)
				tok.Multiline = true
				return tok
			}
		} else {
			if l.peekByte(0) == '"' {
				l.advanceRune()
				if continuation {
					l.interpStack = l.interpStack[:len(l.interpStack)-1]
					return l.makeTok(TokStringEnd, b.String(), start)
				}
				return l.makeTok(TokString, b.String(), start)
			}
			if l.peekByte(0) == '\n' {
				span := diag.Span{File: l.file, Start: start, End: l.curPos()}
				l.addError(span, "unterminated string literal")
				if continuation {
					l.interpStack = l.interpStack[:len(l.interpStack)-1]
					return l.makeTok(TokStringEnd, b.String(), start)
				}
				return l.makeTok(TokString, b.String(), start)
			}
		}

		if l.peekByte(0) == '\\' {
			escStart := l.curPos()
			l.advanceRune() // consume '\'
			if l.pos >= len(l.src) {
				break
			}
			next := l.peekByte(0)
			switch next {
			case '(':
				l.advanceRune() // consume '('
				if !continuation {
					l.interpStack = append(l.interpStack, interpState{parenDepth: 0, multiline: multiline})
					tok := l.makeTok(TokStringStart, b.String(), start)
					tok.Multiline = multiline
					return tok
				}
				l.interpStack[len(l.interpStack)-1].parenDepth = 0
				tok := l.makeTok(TokStringMid, b.String(), start)
				tok.Multiline = multiline
				return tok
			case 'n':
				l.advanceRune()
				b.WriteByte('\n')
			case 'r':
				l.advanceRune()
				b.WriteByte('\r')
			case 't':
				l.advanceRune()
				b.WriteByte('\t')
			case '\\':
				l.advanceRune()
				b.WriteByte('\\')
			case '"':
				l.advanceRune()
				b.WriteByte('"')
			default:
				bad := l.advanceRune()
				l.addError(diag.Span{File: l.file, Start: escStart, End: l.curPos()}, fmt.Sprintf("unknown escape sequence \\%c", bad))
				b.WriteRune(bad)
			}
			continue
		}

		b.WriteRune(l.advanceRune())
	}

	span := diag.Span{File: l.file, Start: start, End: l.curPos()}
	l.addError(span, "unterminated string literal")
	if continuation && len(l.interpStack) > 0 {
		l.interpStack = l.interpStack[:len(l.interpStack)-1]
		tok := l.makeTok(TokStringEnd, b.String(), start)
		tok.Multiline = multiline
		return tok
	}
	tok := l.makeTok(TokString, b.String(), start)
	tok.Multiline = multiline
	return tok
}

func isIdentStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isIdentPart(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
