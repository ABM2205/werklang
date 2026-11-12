package diag

import "fmt"

// Pos represents a source position with 0-indexed Line and UTF-16 Col (LSP compatible).
type Pos struct {
	Offset int
	Line   int
	Col    int
}

// Span represents a half-open source range [Start, End) in File.
type Span struct {
	File  string
	Start Pos
	End   Pos
}

type Severity int

const (
	SeverityError Severity = iota + 1
	SeverityWarning
)

type Diagnostic struct {
	Span     Span
	Severity Severity
	Message  string
}

func (d Diagnostic) String() string {
	sev := "error"
	if d.Severity == SeverityWarning {
		sev = "warning"
	}
	file := d.Span.File
	if file == "" {
		file = "<input>"
	}
	return fmt.Sprintf("%s:%d:%d: %s: %s", file, d.Span.Start.Line+1, d.Span.Start.Col+1, sev, d.Message)
}
