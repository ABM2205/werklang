package diag

import (
	"fmt"
	"strings"
)

// Pos represents a source position with 0-indexed Line and Col (LSP compatible).
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

func (d Diagnostic) Error() string {
	return d.String()
}

type Diagnostics []Diagnostic

func (ds Diagnostics) Error() string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = d.String()
	}
	return strings.Join(parts, "\n")
}

func (ds Diagnostics) HasErrors() bool {
	for _, d := range ds {
		if d.Severity == SeverityError || d.Severity == 0 {
			return true
		}
	}
	return false
}
