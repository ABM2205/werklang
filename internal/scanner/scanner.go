package scanner

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ABM2205/werklang/internal/diag"
)

// DepDirective represents an inline `//werk:dep module@version` directive.
type DepDirective struct {
	Module  string
	Version string
	Span    diag.Span
}

// ConfigBlock describes the single top-level `config [name] { ... }` block in a .werk file.
type ConfigBlock struct {
	Name        string // Empty if anonymous `config { ... }`
	Span        diag.Span
	KeywordSpan diag.Span
	NameSpan    diag.Span
	OpenBrace   diag.Pos
	BodyStart   diag.Pos
	BodyEnd     diag.Pos
	CloseBrace  diag.Pos
	Body        string
}

// VarName returns the Go variable identifier (`config` when anonymous).
func (c *ConfigBlock) VarName() string {
	if c == nil || c.Name == "" {
		return "config"
	}
	return c.Name
}

// StructName returns the top-level generated Go struct type name.
func (c *ConfigBlock) StructName() string {
	if c == nil || c.Name == "" {
		return "werkConfig"
	}
	return c.Name + "Config"
}

// TypePrefix returns the prefix used for nested generated Go struct types.
func (c *ConfigBlock) TypePrefix() string {
	if c == nil || c.Name == "" {
		return "werk"
	}
	return c.Name
}

// Result holds the scanned structure of a .werk source file.
type Result struct {
	File       string
	Source     []byte
	HasShebang bool
	ShebangEnd int // Byte offset after first newline (or EOF) of `#!` line
	Config     *ConfigBlock
	Deps       []DepDirective
	Diags      diag.Diagnostics
}

// PklProjection returns a 1:1 line-preserving Pkl module string where every byte
// outside the `config { ... }` body is replaced with `' '` except `\n` and `\r`.
func (r *Result) PklProjection() string {
	if r.Config == nil {
		return blankPreservingNewlines(r.Source, 0, len(r.Source))
	}
	out := make([]byte, len(r.Source))
	bodyStart := r.Config.BodyStart.Offset
	bodyEnd := r.Config.BodyEnd.Offset
	for i, b := range r.Source {
		if i >= bodyStart && i < bodyEnd {
			out[i] = b
		} else if b == '\n' || b == '\r' {
			out[i] = b
		} else {
			out[i] = ' '
		}
	}
	return string(out)
}

// GoProjection returns a 1:1 line-preserving Go host projection where any line-1
// `#!` shebang and the `config [name] { ... }` block are blanked out with spaces
// while preserving `\n` and `\r` so byte/line/col coordinates match the .werk file.
func (r *Result) GoProjection() string {
	out := make([]byte, len(r.Source))
	copy(out, r.Source)
	if r.HasShebang {
		blankSlicePreservingNewlines(out, 0, r.ShebangEnd)
	}
	if r.Config != nil {
		blankSlicePreservingNewlines(out, r.Config.Span.Start.Offset, r.Config.Span.End.Offset)
	}
	return string(out)
}

func blankPreservingNewlines(src []byte, start, end int) string {
	out := make([]byte, end-start)
	for i := start; i < end; i++ {
		if src[i] == '\n' || src[i] == '\r' {
			out[i-start] = src[i]
		} else {
			out[i-start] = ' '
		}
	}
	return string(out)
}

func blankSlicePreservingNewlines(buf []byte, start, end int) {
	for i := start; i < end && i < len(buf); i++ {
		if buf[i] != '\n' && buf[i] != '\r' {
			buf[i] = ' '
		}
	}
}

type cursor struct {
	file string
	src  []byte
	off  int
	line int
	col  int
}

func (c *cursor) pos() diag.Pos {
	return diag.Pos{Offset: c.off, Line: c.line, Col: c.col}
}

func (c *cursor) eof() bool {
	return c.off >= len(c.src)
}

func (c *cursor) peek() byte {
	if c.off >= len(c.src) {
		return 0
	}
	return c.src[c.off]
}

func (c *cursor) peekAt(delta int) byte {
	idx := c.off + delta
	if idx < 0 || idx >= len(c.src) {
		return 0
	}
	return c.src[idx]
}

func (c *cursor) advance() byte {
	if c.off >= len(c.src) {
		return 0
	}
	b := c.src[c.off]
	if b < utf8.RuneSelf {
		c.off++
		if b == '\n' {
			c.line++
			c.col = 0
		} else {
			c.col++
		}
		return b
	}
	_, size := utf8.DecodeRune(c.src[c.off:])
	c.off += size
	c.col++
	return b
}

func (c *cursor) advanceN(n int) {
	for i := 0; i < n && !c.eof(); i++ {
		c.advance()
	}
}

// Scan lexically partitions a .werk file into Go host regions, an optional shebang,
// inline `//werk:dep` directives, and at most one `config [name] { ... }` Pkl block.
func Scan(filename string, src []byte) *Result {
	res := &Result{
		File:   filename,
		Source: src,
	}
	c := &cursor{file: filename, src: src}

	// 1. Optional `#!` shebang at line 1, col 0
	if len(src) >= 2 && src[0] == '#' && src[1] == '!' {
		res.HasShebang = true
		for !c.eof() && c.peek() != '\n' {
			c.advance()
		}
		if c.peek() == '\n' {
			c.advance()
		}
		res.ShebangEnd = c.off
	}

	braceDepth := 0
	parenDepth := 0
	bracketDepth := 0

	for !c.eof() {
		b := c.peek()

		// Go line comment `// ...`
		if b == '/' && c.peekAt(1) == '/' {
			startPos := c.pos()
			c.advanceN(2)
			commentStart := c.off
			for !c.eof() && c.peek() != '\n' {
				c.advance()
			}
			endPos := c.pos()
			if braceDepth == 0 && parenDepth == 0 && bracketDepth == 0 {
				if dep, ok := parseDepDirective(filename, string(src[commentStart:c.off]), startPos, endPos); ok {
					res.Deps = append(res.Deps, dep)
				}
			}
			continue
		}

		// Go block comment `/* ... */`
		if b == '/' && c.peekAt(1) == '*' {
			c.advanceN(2)
			for !c.eof() {
				if c.peek() == '*' && c.peekAt(1) == '/' {
					c.advanceN(2)
					break
				}
				c.advance()
			}
			continue
		}

		// Go interpreted string `"..."`
		if b == '"' {
			c.advance()
			for !c.eof() {
				ch := c.advance()
				if ch == '\\' && !c.eof() {
					c.advance()
					continue
				}
				if ch == '"' || ch == '\n' {
					break
				}
			}
			continue
		}

		// Go raw string `` `...` ``
		if b == '`' {
			c.advance()
			for !c.eof() {
				if c.advance() == '`' {
					break
				}
			}
			continue
		}

		// Go rune literal `'...'`
		if b == '\'' {
			c.advance()
			for !c.eof() {
				ch := c.advance()
				if ch == '\\' && !c.eof() {
					c.advance()
					continue
				}
				if ch == '\'' || ch == '\n' {
					break
				}
			}
			continue
		}

		switch b {
		case '{':
			braceDepth++
			c.advance()
			continue
		case '}':
			if braceDepth > 0 {
				braceDepth--
			}
			c.advance()
			continue
		case '(':
			parenDepth++
			c.advance()
			continue
		case ')':
			if parenDepth > 0 {
				parenDepth--
			}
			c.advance()
			continue
		case '[':
			bracketDepth++
			c.advance()
			continue
		case ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
			c.advance()
			continue
		}

		// Identifier scan
		if isIdentStart(c.src[c.off:]) {
			kwStart := c.pos()
			ident := scanGoIdent(c)
			kwEnd := c.pos()

			if ident == "config" && braceDepth == 0 && parenDepth == 0 && bracketDepth == 0 {
				saved := *c
				if blk, ok := tryScanConfigHeaderAndBody(c, filename, kwStart, kwEnd, &res.Diags); ok {
					if res.Config != nil {
						res.Diags = append(res.Diags, diag.Diagnostic{
							Span:     blk.KeywordSpan,
							Severity: diag.SeverityError,
							Message:  "at most one config block is allowed per .werk file",
						})
					} else {
						res.Config = blk
					}
					continue
				}
				*c = saved
			}
			continue
		}

		c.advance()
	}

	return res
}

func parseDepDirective(file, commentText string, start, end diag.Pos) (DepDirective, bool) {
	trimmed := strings.TrimSpace(commentText)
	for _, prefix := range []string{"werk:dep ", "werk:dep\t"} {
		if strings.HasPrefix(trimmed, prefix) {
			spec := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			if spec == "" {
				return DepDirective{}, false
			}
			mod := spec
			ver := "latest"
			if at := strings.LastIndexByte(spec, '@'); at > 0 {
				mod = spec[:at]
				ver = spec[at+1:]
			}
			if mod != "" && ver != "" {
				return DepDirective{
					Module:  mod,
					Version: ver,
					Span:    diag.Span{File: file, Start: start, End: end},
				}, true
			}
		}
	}
	return DepDirective{}, false
}

func isIdentStart(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	r, _ := utf8.DecodeRune(b)
	return r == '_' || unicode.IsLetter(r)
}

func isIdentPart(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	r, _ := utf8.DecodeRune(b)
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func scanGoIdent(c *cursor) string {
	start := c.off
	for !c.eof() && isIdentPart(c.src[c.off:]) {
		c.advance()
	}
	return string(c.src[start:c.off])
}

func skipGoTrivia(c *cursor) {
	for !c.eof() {
		b := c.peek()
		if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			c.advance()
			continue
		}
		if b == '/' && c.peekAt(1) == '/' {
			c.advanceN(2)
			for !c.eof() && c.peek() != '\n' {
				c.advance()
			}
			continue
		}
		if b == '/' && c.peekAt(1) == '*' {
			c.advanceN(2)
			for !c.eof() {
				if c.peek() == '*' && c.peekAt(1) == '/' {
					c.advanceN(2)
					break
				}
				c.advance()
			}
			continue
		}
		break
	}
}

func tryScanConfigHeaderAndBody(c *cursor, file string, kwStart, kwEnd diag.Pos, diags *diag.Diagnostics) (*ConfigBlock, bool) {
	skipGoTrivia(c)

	var name string
	var nameSpan diag.Span
	if !c.eof() && isIdentStart(c.src[c.off:]) {
		nStart := c.pos()
		name = scanGoIdent(c)
		nEnd := c.pos()
		nameSpan = diag.Span{File: file, Start: nStart, End: nEnd}
		skipGoTrivia(c)
	}

	if c.eof() || c.peek() != '{' {
		return nil, false
	}

	openPos := c.pos()
	c.advance() // consume '{'
	bodyStart := c.pos()

	closed, closePos, bodyEnd := scanPklBalancedBody(c)
	if !closed {
		*diags = append(*diags, diag.Diagnostic{
			Span:     diag.Span{File: file, Start: openPos, End: bodyStart},
			Severity: diag.SeverityError,
			Message:  "unterminated config block (missing closing '}')",
		})
		bodyEnd = c.pos()
		closePos = c.pos()
	}

	endPos := c.pos()
	return &ConfigBlock{
		Name:        name,
		Span:        diag.Span{File: file, Start: kwStart, End: endPos},
		KeywordSpan: diag.Span{File: file, Start: kwStart, End: kwEnd},
		NameSpan:    nameSpan,
		OpenBrace:   openPos,
		BodyStart:   bodyStart,
		BodyEnd:     bodyEnd,
		CloseBrace:  closePos,
		Body:        string(c.src[bodyStart.Offset:bodyEnd.Offset]),
	}, true
}

// scanPklBalancedBody scans Pkl tokens inside `config { ... }` until the matching closing `}`.
func scanPklBalancedBody(c *cursor) (closed bool, closeBracePos diag.Pos, bodyEndPos diag.Pos) {
	depth := 1
	for !c.eof() {
		b := c.peek()

		// Pkl line comment `// ...` or `/// ...`
		if b == '/' && c.peekAt(1) == '/' {
			c.advanceN(2)
			for !c.eof() && c.peek() != '\n' {
				c.advance()
			}
			continue
		}

		// Pkl nested block comment `/* ... /* ... */ ... */`
		if b == '/' && c.peekAt(1) == '*' {
			c.advanceN(2)
			commentDepth := 1
			for !c.eof() && commentDepth > 0 {
				if c.peek() == '/' && c.peekAt(1) == '*' {
					c.advanceN(2)
					commentDepth++
					continue
				}
				if c.peek() == '*' && c.peekAt(1) == '/' {
					c.advanceN(2)
					commentDepth--
					continue
				}
				c.advance()
			}
			continue
		}

		// Pkl custom-delimited string `#"..."#` or `###"""..."""###`
		if b == '#' {
			hashes := 0
			for c.peekAt(hashes) == '#' {
				hashes++
			}
			if c.peekAt(hashes) == '"' {
				c.advanceN(hashes)
				scanPklString(c, hashes)
				continue
			}
		}

		// Pkl standard or multiline string `"..."` or `"""..."""`
		if b == '"' {
			scanPklString(c, 0)
			continue
		}

		// Pkl backtick identifier `` `foo` ``
		if b == '`' {
			c.advance()
			for !c.eof() && c.peek() != '`' && c.peek() != '\n' {
				c.advance()
			}
			if c.peek() == '`' {
				c.advance()
			}
			continue
		}

		if b == '{' {
			depth++
			c.advance()
			continue
		}

		if b == '}' {
			depth--
			if depth == 0 {
				bodyEnd := c.pos()
				closePos := c.pos()
				c.advance() // consume matching '}'
				return true, closePos, bodyEnd
			}
			c.advance()
			continue
		}

		c.advance()
	}
	return false, c.pos(), c.pos()
}

func scanPklString(c *cursor, hashes int) {
	multiline := c.peek() == '"' && c.peekAt(1) == '"' && c.peekAt(2) == '"'
	if multiline {
		c.advanceN(3)
	} else {
		c.advance()
	}

	for !c.eof() {
		// Check closing delimiter
		if multiline {
			if c.peek() == '"' && c.peekAt(1) == '"' && c.peekAt(2) == '"' && hasTrailingHashes(c, 3, hashes) {
				c.advanceN(3 + hashes)
				return
			}
		} else {
			if c.peek() == '"' && hasTrailingHashes(c, 1, hashes) {
				c.advanceN(1 + hashes)
				return
			}
			if c.peek() == '\n' && hashes == 0 {
				return
			}
		}

		// Check escape or interpolation `\(` (with `hashes` '#' chars)
		if c.peek() == '\\' && hasTrailingHashes(c, 1, hashes) {
			nextAfter := c.peekAt(1 + hashes)
			if nextAfter == '(' {
				// String interpolation `\(` ... `)`
				c.advanceN(2 + hashes)
				scanPklInterpolation(c)
				continue
			}
			// Escaped character
			c.advanceN(1 + hashes)
			if !c.eof() {
				c.advance()
			}
			continue
		}

		c.advance()
	}
}

func hasTrailingHashes(c *cursor, offset, hashes int) bool {
	for i := 0; i < hashes; i++ {
		if c.peekAt(offset+i) != '#' {
			return false
		}
	}
	return true
}

func scanPklInterpolation(c *cursor) {
	parenDepth := 1
	for !c.eof() && parenDepth > 0 {
		b := c.peek()
		if b == '"' {
			scanPklString(c, 0)
			continue
		}
		if b == '#' && c.peekAt(1) == '"' {
			hashes := 0
			for c.peekAt(hashes) == '#' {
				hashes++
			}
			if c.peekAt(hashes) == '"' {
				c.advanceN(hashes)
				scanPklString(c, hashes)
				continue
			}
		}
		if b == '(' {
			parenDepth++
			c.advance()
			continue
		}
		if b == ')' {
			parenDepth--
			c.advance()
			continue
		}
		c.advance()
	}
}
