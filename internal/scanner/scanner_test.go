package scanner

import (
	"strings"
	"testing"
)

func TestScanLinePreservationAndShebang(t *testing.T) {
	src := `#!/usr/bin/env werk
//werk:dep golang.org/x/sync@v0.24.0
import "fmt"

config app {
    /// Port doc
    port: UInt16 = 8080
    /* nested /* comment with } */ still comment */
    banner = "hello } \(if (true) "}" else "{")"
    raw = #"raw "}" string"#
    db {
        host = "localhost"
    }
}

config := "local Go var named config is fine!"
fmt.Println(app.Port, config)
`
	res := Scan("server.werk", []byte(src))
	if res.Diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", res.Diags)
	}
	if !res.HasShebang {
		t.Fatalf("expected shebang to be detected")
	}
	if len(res.Deps) != 1 || res.Deps[0].Module != "golang.org/x/sync" || res.Deps[0].Version != "v0.24.0" {
		t.Fatalf("unexpected deps: %+v", res.Deps)
	}
	if res.Config == nil || res.Config.Name != "app" {
		t.Fatalf("expected named config 'app', got %+v", res.Config)
	}

	origLines := strings.Split(src, "\n")
	pklLines := strings.Split(res.PklProjection(), "\n")
	goLines := strings.Split(res.GoProjection(), "\n")

	if len(pklLines) != len(origLines) {
		t.Fatalf("pkl line count %d != orig %d", len(pklLines), len(origLines))
	}
	if len(goLines) != len(origLines) {
		t.Fatalf("go line count %d != orig %d", len(goLines), len(origLines))
	}

	// Line 7 (0-indexed 6) is `    port: UInt16 = 8080`
	if pklLines[6] != origLines[6] {
		t.Fatalf("expected 1:1 Pkl line 7 %q, got %q", origLines[6], pklLines[6])
	}
	if strings.TrimSpace(pklLines[0]) != "" || strings.TrimSpace(pklLines[16]) != "" {
		t.Fatalf("expected shebang and Go code blanked in Pkl projection")
	}

	// Line 16 (0-indexed 15) is `config := "local Go var named config is fine!"`
	if goLines[15] != origLines[15] {
		t.Fatalf("expected Go projection to preserve `config := ...`, got %q", goLines[15])
	}
	if strings.TrimSpace(goLines[6]) != "" {
		t.Fatalf("expected config body blanked in Go projection, got %q", goLines[6])
	}
}

func TestRejectMultipleConfigBlocks(t *testing.T) {
	src := `config a { x = 1 }
config b { y = 2 }
`
	res := Scan("dup.werk", []byte(src))
	if !res.Diags.HasErrors() {
		t.Fatalf("expected error for multiple config blocks")
	}
	if !strings.Contains(res.Diags.Error(), "at most one config block") {
		t.Fatalf("unexpected error message: %v", res.Diags)
	}
}
