package lsp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ABM2205/werklang/internal/lsp"
)

func encodeRPC(t *testing.T, payload any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(raw), string(raw))
}

func TestLSPDiagnosticsCompletionHoverAndDefinition(t *testing.T) {
	dir := t.TempDir()

	svcPath := filepath.Join(dir, "service.werk")
	if err := os.WriteFile(svcPath, []byte("name string\nport int = 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wfPath := filepath.Join(dir, "Werkfile")
	wfURI := lsp.PathToURI(wfPath)

	src := `import "./service.werk" as Service
import "go:path/filepath"

api = Service {
    name = "gw"
}

target test {
    desc = "Run unit tests"
    sh("echo ok")
}

target build {
    deps = ["test"]
    p = filepath.Join("bin", api.name)
    print(p)
}
`
	var inBuf, outBuf bytes.Buffer
	inBuf.WriteString(encodeRPC(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"rootUri": lsp.PathToURI(dir),
		},
	}))
	inBuf.WriteString(encodeRPC(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        wfURI,
				"languageId": "werklang",
				"version":    1,
				"text":       src,
			},
		},
	}))
	inBuf.WriteString(encodeRPC(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "shutdown",
	}))
	inBuf.WriteString(encodeRPC(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "exit",
	}))

	srv := lsp.NewServer(&inBuf, &outBuf)
	// Test direct semantic queries after opening document (before exit clears nothing unless didClose is called)
	// Wait: let's run didOpen without exit first, inspect state, then exit!
	inBuf.Reset()
	outBuf.Reset()
	inBuf.WriteString(encodeRPC(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"rootUri": lsp.PathToURI(dir)},
	}))
	inBuf.WriteString(encodeRPC(t, map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        wfURI,
				"languageId": "werklang",
				"version":    1,
				"text":       src,
			},
		},
	}))

	srv = lsp.NewServer(&inBuf, &outBuf)
	if err := srv.Run(); err != nil {
		t.Fatalf("LSP Run error: %v", err)
	}
	if !strings.Contains(outBuf.String(), `"diagnostics":[]`) {
		t.Fatalf("expected 0 diagnostics for valid Werkfile, got:\n%s", outBuf.String())
	}

	// 1. Completion inside `Service { ... }` (line 4) should offer unpopulated `port` field!
	amendItems := srv.Complete(wfURI, lsp.Position{Line: 4, Character: 4})
	foundPort := false
	for _, it := range amendItems {
		if it.Label == "port" {
			foundPort = true
			break
		}
	}
	if !foundPort {
		t.Fatalf("expected 'port' completion inside Service { ... }, got %+v", amendItems)
	}

	// 2. Completion after `filepath.` (line 14, col 17) should offer `Join`!
	goItems := srv.Complete(wfURI, lsp.Position{Line: 14, Character: 17})
	foundJoin := false
	for _, it := range goItems {
		if it.Label == "Join" {
			foundJoin = true
			break
		}
	}
	if !foundJoin {
		t.Fatalf("expected 'Join' completion on filepath., got %+v", goItems)
	}

	// 3. Completion inside `deps = ["test"]` (line 13, col 14) should offer `test`!
	depItems := srv.Complete(wfURI, lsp.Position{Line: 13, Character: 14})
	foundTestDep := false
	for _, it := range depItems {
		if it.Label == "test" {
			foundTestDep = true
			break
		}
	}
	if !foundTestDep {
		t.Fatalf("expected 'test' target completion inside deps = [...], got %+v", depItems)
	}

	// 4. Go to Definition on `"test"` inside `deps = ["test"]` (line 13, col 14) -> line 7 (`target test`)!
	loc := srv.Definition(wfURI, lsp.Position{Line: 13, Character: 14})
	if loc == nil || loc.Range.Start.Line != 7 {
		t.Fatalf("expected definition of dep 'test' at line 7, got %+v", loc)
	}

	// 5. Hover on `Join` (line 14, col 18) -> shows Go function signature
	hov := srv.Hover(wfURI, lsp.Position{Line: 14, Character: 18})
	if hov == nil || !strings.Contains(hov.Contents.Value, "Join") {
		t.Fatalf("expected hover info for filepath.Join, got %+v", hov)
	}

	// 6. Document symbols
	syms := srv.DocumentSymbols(wfURI)
	if len(syms) < 4 {
		t.Fatalf("expected at least 4 document symbols, got %+v", syms)
	}
}
