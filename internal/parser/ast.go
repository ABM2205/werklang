package parser

type Program struct {
	Vars   []VarDecl
	Prints []PrintStmt
}

type VarDecl struct {
	Name  string
	Value string
}

type PrintStmt struct {
	VarName string
}
