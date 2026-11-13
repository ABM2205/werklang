package lsp

import (
	"bufio"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestLSPConfigCompletionHoverDefinitionAndDiagnostics(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))

	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()

	srv := NewServer(serverIn, serverOut)
	done := make(chan error, 1)
	go func() {
		done <- srv.Run()
	}()

	reader := bufio.NewReader(clientIn)
	sendReq := func(id int, method string, params any) {
		rawID, _ := json.Marshal(id)
		idPtr := json.RawMessage(rawID)
		rawParams, _ := json.Marshal(params)
		if err := writeMessage(clientOut, rpcMessage{
			JSONRPC: "2.0",
			ID:      &idPtr,
			Method:  method,
			Params:  rawParams,
		}); err != nil {
			t.Fatalf("writeMessage %s: %v", method, err)
		}
	}
	sendNotif := func(method string, params any) {
		if err := writeMessage(clientOut, rpcNotification{
			JSONRPC: "2.0",
			Method:  method,
			Params:  params,
		}); err != nil {
			t.Fatalf("writeMessage %s: %v", method, err)
		}
	}
	readNext := func() rpcMessage {
		raw, err := readMessage(reader)
		if err != nil {
			t.Fatalf("readMessage: %v", err)
		}
		var m rpcMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return m
	}

	// 1. Initialize
	sendReq(1, "initialize", map[string]any{"rootUri": PathToURI(dir)})
	initResp := readNext()
	if !strings.Contains(string(initResp.Result), "werk-lsp") {
		t.Fatalf("unexpected initialize response: %s", string(initResp.Result))
	}

	// 2. Open standalone `.werk` file with `config app` (including unpopulated required template field!)
	werkPath := filepath.Join(dir, "app.werk")
	uri := PathToURI(werkPath)
	src := `#!/usr/bin/env werk
import "fmt"

config app {
	/// API endpoint URL
	apiUrl: String
	httpPort: UInt16 = 8080
	db {
		host = "localhost"
	}
}

fmt.Println(app.HttpPort)
`
	sendNotif("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{
			URI:        uri,
			LanguageID: "werklang",
			Version:    1,
			Text:       src,
		},
	})

	// Expect clean publishDiagnostics (0 errors!)
	diagMsg := readNext()
	if diagMsg.Method != "textDocument/publishDiagnostics" {
		t.Fatalf("expected publishDiagnostics, got %s", diagMsg.Method)
	}
	var pub PublishDiagnosticsParams
	_ = json.Unmarshal(diagMsg.Params, &pub)
	if len(pub.Diagnostics) != 0 {
		t.Fatalf("expected 0 diagnostics on valid .werk script, got: %+v", pub.Diagnostics)
	}

	// 3. Hover on `app.HttpPort` (line 12, col 17)
	sendReq(2, "textDocument/hover", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 12, Character: 17},
	})
	hoverMsg := readNext()
	if !strings.Contains(string(hoverMsg.Result), "HttpPort uint16") {
		t.Fatalf("expected HttpPort uint16 hover, got: %s", string(hoverMsg.Result))
	}

	// 4. Cross-language Go-to-Definition on `app.HttpPort` -> jumps to line 6 (`httpPort: UInt16 = 8080`)!
	sendReq(3, "textDocument/definition", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 12, Character: 17},
	})
	defMsg := readNext()
	var loc Location
	if err := json.Unmarshal(defMsg.Result, &loc); err != nil {
		t.Fatalf("unmarshal definition: %v (%s)", err, string(defMsg.Result))
	}
	if loc.Range.Start.Line != 6 {
		t.Fatalf("expected definition jump to Pkl property line 6, got line %d", loc.Range.Start.Line)
	}

	// 5. Completion on `app.`
	srcDot := strings.Replace(src, "fmt.Println(app.HttpPort)", "fmt.Println(app.)", 1)
	sendNotif("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument:   VersionedTextDocumentIdentifier{URI: uri, Version: 2},
		ContentChanges: []TextDocumentContentChangeEvent{{Text: srcDot}},
	})
	_ = readNext() // diagnostics for mid-edit buffer

	sendReq(4, "textDocument/completion", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 12, Character: 16},
	})
	compMsg := readNext()
	if !strings.Contains(string(compMsg.Result), "ApiUrl") || !strings.Contains(string(compMsg.Result), "API endpoint URL") {
		t.Fatalf("expected ApiUrl + Pkl doc comment in completions, got: %s", string(compMsg.Result))
	}

	// 6. Disallowed `func main()` surfaces diagnostic immediately
	badSrc := "import \"fmt\"\nfunc main() { fmt.Println(1) }\n"
	sendNotif("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument:   VersionedTextDocumentIdentifier{URI: uri, Version: 3},
		ContentChanges: []TextDocumentContentChangeEvent{{Text: badSrc}},
	})
	badDiagMsg := readNext()
	if !strings.Contains(string(badDiagMsg.Params), "cannot declare func main") {
		t.Fatalf("expected func main diagnostic, got: %s", string(badDiagMsg.Params))
	}

	sendReq(5, "shutdown", nil)
	_ = readNext()
	sendNotif("exit", nil)
	if err := <-done; err != nil {
		t.Fatalf("server exit error: %v", err)
	}
}
