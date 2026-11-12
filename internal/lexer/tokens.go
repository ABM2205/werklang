package lexer

import "github.com/ABM2205/werklang/internal/diag"

type TokenType string

const (
	TokEOF     TokenType = "EOF"
	TokIllegal TokenType = "ILLEGAL"

	// Identifiers & literals
	TokIdent       TokenType = "IDENT"
	TokIntLit      TokenType = "INT_LIT"
	TokFloatLit    TokenType = "FLOAT_LIT"
	TokString      TokenType = "STRING"
	TokStringStart TokenType = "STRING_START" // opening "...\(
	TokStringMid   TokenType = "STRING_MID"   // )...\(
	TokStringEnd   TokenType = "STRING_END"   // )..."

	// Reserved keywords (design/001_keywords.md)
	TokKwInt    TokenType = "int"
	TokKwFloat  TokenType = "float"
	TokKwString TokenType = "string"
	TokKwBool   TokenType = "bool"

	TokKwAmends TokenType = "amends"
	TokKwImport TokenType = "import"
	TokKwAs     TokenType = "as"
	TokKwConfig TokenType = "config"
	TokKwTarget TokenType = "target"

	TokKwIf   TokenType = "if"
	TokKwThen TokenType = "then"
	TokKwElse TokenType = "else"
	TokKwFor  TokenType = "for"
	TokKwIn   TokenType = "in"

	TokKwTrue  TokenType = "true"
	TokKwFalse TokenType = "false"

	// Operators & punctuation
	TokAssign   TokenType = "="
	TokPlus     TokenType = "+"
	TokMinus    TokenType = "-"
	TokStar     TokenType = "*"
	TokSlash    TokenType = "/"
	TokPercent  TokenType = "%"
	TokEq       TokenType = "=="
	TokNeq      TokenType = "!="
	TokLt       TokenType = "<"
	TokLte      TokenType = "<="
	TokGt       TokenType = ">"
	TokGte      TokenType = ">="
	TokAnd      TokenType = "&&"
	TokOr       TokenType = "||"
	TokBang     TokenType = "!"
	TokLParen   TokenType = "("
	TokRParen   TokenType = ")"
	TokLBrace   TokenType = "{"
	TokRBrace   TokenType = "}"
	TokLBracket TokenType = "["
	TokRBracket TokenType = "]"
	TokComma    TokenType = ","
	TokDot      TokenType = "."
)

var keywords = map[string]TokenType{
	"int":    TokKwInt,
	"float":  TokKwFloat,
	"string": TokKwString,
	"bool":   TokKwBool,
	"amends": TokKwAmends,
	"import": TokKwImport,
	"as":     TokKwAs,
	"config": TokKwConfig,
	"target": TokKwTarget,
	"if":     TokKwIf,
	"then":   TokKwThen,
	"else":   TokKwElse,
	"for":    TokKwFor,
	"in":     TokKwIn,
	"true":   TokKwTrue,
	"false":  TokKwFalse,
}

func LookupIdent(ident string) TokenType {
	if kw, ok := keywords[ident]; ok {
		return kw
	}
	return TokIdent
}

type Token struct {
	Type      TokenType
	Literal   string
	Multiline bool
	Span      diag.Span
}

type Comment struct {
	Text    string
	Shebang bool
	Span    diag.Span
}
