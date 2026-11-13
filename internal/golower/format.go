package golower

import (
	"go/format"
	"strings"

	"github.com/ABM2205/werklang/internal/scanner"
)

// FormatWerk formats a `.werk` source file:
//   - Preserves any line-1 `#!` shebang.
//   - Formats header Go imports, top-level Go declarations, and top-level Go statements using `gofmt`.
//   - Preserves the `config [name] { ... }` Pkl block with clean header spacing and trimmed trailing whitespace.
func FormatWerk(filename string, src []byte) ([]byte, error) {
	scanRes := scanner.Scan(filename, src)
	if scanRes.Diags.HasErrors() {
		return nil, scanRes.Diags
	}

	goProj := scanRes.GoProjection()
	projLines := strings.Split(goProj, "\n")
	items, diags := partitionTopLevel(filename, []byte(goProj), projLines, scanRes.Config)
	if diags.HasErrors() {
		return nil, diags
	}

	var out strings.Builder
	if scanRes.HasShebang {
		shebang := strings.TrimRight(string(src[:scanRes.ShebangEnd]), "\r\n")
		out.WriteString(shebang + "\n")
	}

	wroteHeader := false

	// Emit imports and config in original relative order
	configEmitted := scanRes.Config == nil
	emitConfig := func() {
		if configEmitted {
			return
		}
		configEmitted = true
		if wroteHeader {
			out.WriteString("\n")
		}
		if scanRes.Config.Name != "" {
			out.WriteString("config " + scanRes.Config.Name + " {")
		} else {
			out.WriteString("config {")
		}
		body := formatPklBody(scanRes.Config.Body)
		if body == "" {
			out.WriteString("}\n")
		} else {
			out.WriteString("\n" + body + "\n}\n")
		}
		wroteHeader = true
	}

	for _, it := range items {
		if it.kind != itemImport {
			continue
		}
		if !configEmitted && scanRes.Config.Span.Start.Line < it.startLine {
			emitConfig()
		}
		if wroteHeader {
			out.WriteString("\n")
		}
		formatted := formatTopChunk(it)
		out.WriteString(formatted + "\n")
		wroteHeader = true
	}
	if !configEmitted {
		emitConfig()
	}

	wroteBody := false
	prevKind := itemKind(-1)
	for _, it := range items {
		if it.kind == itemImport {
			continue
		}
		if wroteHeader && !wroteBody {
			out.WriteString("\n")
		} else if wroteBody && (it.kind == itemFunc || prevKind == itemFunc || it.kind == itemType || prevKind == itemType) {
			out.WriteString("\n")
		}
		out.WriteString(formatTopChunk(it) + "\n")
		wroteBody = true
		prevKind = it.kind
	}

	return []byte(out.String()), nil
}

func formatTopChunk(it topItem) string {
	raw := strings.TrimSpace(strings.Join(it.rawLines, "\n"))
	switch it.kind {
	case itemImport, itemType, itemConst, itemFunc, itemVar:
		wrapped := "package p\n" + raw + "\n"
		if fmted, err := format.Source([]byte(wrapped)); err == nil {
			res := strings.TrimPrefix(string(fmted), "package p\n")
			return strings.TrimSpace(res)
		}
	case itemStmt:
		wrapped := "package p\nfunc _() {\n" + raw + "\n}\n"
		if fmted, err := format.Source([]byte(wrapped)); err == nil {
			s := string(fmted)
			if idx := strings.Index(s, "func _() {\n"); idx >= 0 {
				s = s[idx+len("func _() {\n"):]
				if end := strings.LastIndex(s, "\n}"); end >= 0 {
					s = s[:end]
				}
				lines := strings.Split(s, "\n")
				for i, l := range lines {
					lines[i] = strings.TrimPrefix(l, "\t")
				}
				return strings.TrimSpace(strings.Join(lines, "\n"))
			}
		}
	}
	return raw
}

func formatPklBody(body string) string {
	lines := strings.Split(body, "\n")
	// Trim leading/trailing blank lines
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	return strings.Join(lines, "\n")
}
