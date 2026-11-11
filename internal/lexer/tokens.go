package lexer

type TokenType string

const (
	TokEOF    TokenType = "EOF"
	TokIdent  TokenType = "IDENT"
	TokString TokenType = "STRING"
	TokLParen TokenType = "("
	TokRParen TokenType = ")"
	TokAssign TokenType = "="
)

type Token struct {
	Type    TokenType
	Literal string
}
