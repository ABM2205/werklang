package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ABM2205/werklang/internal/diag"
	"github.com/ABM2205/werklang/internal/golower"
	"github.com/ABM2205/werklang/internal/pklschema"
	"github.com/ABM2205/werklang/internal/runner"
	"github.com/ABM2205/werklang/internal/scanner"
)

type openDoc struct {
	uri            string
	path           string
	version        int
	text           string
	scan           *scanner.Result
	lastGoodConfig *pklschema.CompiledConfig
	lowered        *golower.LowerResult

	virtGoURI    string
	virtGoPath   string
	virtPklURI   string
	goWorkDir    string
	goplsDiags   []Diagnostic
	pklLspDiags  []Diagnostic
	builtinDiags []Diagnostic
}

type Server struct {
	in      *bufio.Reader
	out     io.Writer
	writeMu sync.Mutex

	mu          sync.Mutex
	rootDir     string
	docs        map[string]*openDoc
	virtGoToDoc map[string]string // virtGoURI -> werkURI
	virtPklToDoc map[string]string // virtPklURI -> werkURI

	goplsClients map[string]*childLSP // keyed by workspace rootDir
	pklClient    *childLSP
	triedPklLsp  bool
}

func NewServer(in io.Reader, out io.Writer) *Server {
	return &Server{
		in:           bufio.NewReader(in),
		out:          out,
		docs:         make(map[string]*openDoc),
		virtGoToDoc:  make(map[string]string),
		virtPklToDoc: make(map[string]string),
		goplsClients: make(map[string]*childLSP),
	}
}

func (s *Server) Run() error {
	defer s.shutdownChildren()
	for {
		raw, err := readMessage(s.in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var msg rpcMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if stop := s.handleMessage(msg); stop {
			return nil
		}
	}
}

func (s *Server) shutdownChildren() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.goplsClients {
		c.close()
	}
	if s.pklClient != nil {
		s.pklClient.close()
	}
}

func (s *Server) reply(id *json.RawMessage, result any, rpcErr *rpcError) {
	if id == nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = writeMessage(s.out, rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
		Error:   rpcErr,
	})
}

func (s *Server) notify(method string, params any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = writeMessage(s.out, rpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

func (s *Server) handleMessage(msg rpcMessage) bool {
	switch msg.Method {
	case "initialize":
		var p InitializeParams
		_ = json.Unmarshal(msg.Params, &p)
		s.mu.Lock()
		if p.RootURI != "" {
			s.rootDir = URIToPath(p.RootURI)
		} else if p.RootPath != "" {
			s.rootDir = p.RootPath
		}
		s.mu.Unlock()

		caps := map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync": 1, // Full sync
				"hoverProvider":    true,
				"definitionProvider": true,
				"documentSymbolProvider": true,
				"documentFormattingProvider": true,
				"completionProvider": map[string]any{
					"triggerCharacters": []string{".", ":", "\""},
				},
				"signatureHelpProvider": map[string]any{
					"triggerCharacters": []string{"(", ","},
				},
			},
			"serverInfo": map[string]any{
				"name":    "werk-lsp",
				"version": "2.0.0",
			},
		}
		s.reply(msg.ID, caps, nil)

	case "initialized":
		// No-op

	case "shutdown":
		s.shutdownChildren()
		s.reply(msg.ID, map[string]any{}, nil)

	case "exit":
		return true

	case "textDocument/didOpen":
		var p DidOpenTextDocumentParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.updateDocument(p.TextDocument.URI, p.TextDocument.Version, p.TextDocument.Text, true)
		}

	case "textDocument/didChange":
		var p DidChangeTextDocumentParams
		if json.Unmarshal(msg.Params, &p) == nil && len(p.ContentChanges) > 0 {
			latest := p.ContentChanges[len(p.ContentChanges)-1].Text
			s.updateDocument(p.TextDocument.URI, p.TextDocument.Version, latest, false)
		}

	case "textDocument/didSave":
		var p DidSaveTextDocumentParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.mu.Lock()
			doc := s.docs[p.TextDocument.URI]
			var text string
			var ver int
			if doc != nil {
				text = doc.text
				ver = doc.version + 1
			}
			s.mu.Unlock()
			if doc != nil {
				s.updateDocument(p.TextDocument.URI, ver, text, false)
			}
		}

	case "textDocument/didClose":
		var p DidCloseTextDocumentParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.closeDocument(p.TextDocument.URI)
		}

	case "textDocument/hover":
		var p TextDocumentPositionParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.reply(msg.ID, s.handleHover(p), nil)
		} else {
			s.reply(msg.ID, nil, nil)
		}

	case "textDocument/completion":
		var p TextDocumentPositionParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.reply(msg.ID, s.handleCompletion(p), nil)
		} else {
			s.reply(msg.ID, []CompletionItem{}, nil)
		}

	case "textDocument/definition":
		var p TextDocumentPositionParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.reply(msg.ID, s.handleDefinition(p), nil)
		} else {
			s.reply(msg.ID, nil, nil)
		}

	case "textDocument/signatureHelp":
		var p TextDocumentPositionParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.reply(msg.ID, s.handleSignatureHelp(p), nil)
		} else {
			s.reply(msg.ID, nil, nil)
		}

	case "textDocument/documentSymbol":
		var p DocumentSymbolParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.reply(msg.ID, s.handleDocumentSymbols(p.TextDocument.URI), nil)
		} else {
			s.reply(msg.ID, []DocumentSymbol{}, nil)
		}

	case "textDocument/formatting":
		var p DocumentFormattingParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.reply(msg.ID, s.handleFormatting(p.TextDocument.URI), nil)
		} else {
			s.reply(msg.ID, []TextEdit{}, nil)
		}

	default:
		if msg.ID != nil {
			s.reply(msg.ID, nil, &rpcError{Code: -32601, Message: "method not found: " + msg.Method})
		}
	}
	return false
}

func (s *Server) updateDocument(uri string, version int, text string, isOpen bool) {
	path := URIToPath(uri)
	absPath, err := filepath.Abs(path)
	if err == nil {
		path = absPath
	}

	scanRes := scanner.Scan(path, []byte(text))

	s.mu.Lock()
	doc, exists := s.docs[uri]
	if !exists {
		doc = &openDoc{
			uri:  uri,
			path: path,
		}
		s.docs[uri] = doc
	}
	doc.version = version
	doc.text = text
	doc.scan = scanRes
	prevGoodCfg := doc.lastGoodConfig
	s.mu.Unlock()

	var builtinDiags []Diagnostic
	for _, d := range scanRes.Diags {
		builtinDiags = append(builtinDiags, toLspDiagnostic(d, "werk"))
	}

	// 1. Evaluate / reflect Pkl schema (retaining `lastGoodConfig` if user is mid-keystroke with syntax error)
	activeCfg := prevGoodCfg
	if scanRes.Config != nil {
		overlay := map[string][]byte{path: []byte(text)}
		cfg, pklErr := pklschema.EvaluateAndGenerate(pklschema.EvalOptions{
			ScriptPath: path,
			Scan:       scanRes,
			Overlay:    overlay,
			SchemaOnly: true,
		})
		if pklErr == nil && cfg != nil {
			activeCfg = cfg
		} else if pklErr != nil {
			builtinDiags = append(builtinDiags, parsePklDiagnostic(scanRes.Config, pklErr.Error()))
		}
	} else {
		activeCfg = nil
	}

	// 2. Determine Go workspace (project `go.mod` or standalone cached single-file workspace!)
	goModRoot := runner.FindGoModRoot(filepath.Dir(path))
	goWorkDir := goModRoot
	var virtGoPath string
	if goWorkDir == "" {
		wsDir, wsErr := runner.EnsureStandaloneWorkspace(path, scanRes.Deps, nil)
		if wsErr == nil {
			goWorkDir = wsDir
			virtGoPath = filepath.Join(wsDir, "werk_virtual.go")
		}
	}
	if virtGoPath == "" {
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		virtGoPath = filepath.Join(filepath.Dir(path), stem+"_werk_lsp.go")
	}

	// 3. Lower Go script (with TolerateErrors so partial edits still produce a virtual Go buffer)
	lowerRes, _ := golower.Lower(golower.LowerOptions{
		Filename:           path,
		WorkDir:            goWorkDir,
		Scan:               scanRes,
		Config:             activeCfg,
		EmitLineDirectives: false,
		TolerateErrors:     true,
	})
	if lowerRes != nil {
		// Append non-duplicate `golower` diagnostics (excluding scan diags already added)
		for i := len(scanRes.Diags); i < len(lowerRes.Diags); i++ {
			builtinDiags = append(builtinDiags, toLspDiagnostic(lowerRes.Diags[i], "werk"))
		}
	}

	virtGoURI := PathToURI(virtGoPath)
	virtPklURI := PathToURI(path + ".virtual.pkl")

	// 4. Sync to child LSPs (`gopls` and `pkl-lsp`) if available; otherwise run fast built-in Go check
	gopls := s.getOrStartGopls(goWorkDir)
	if gopls == nil && lowerRes != nil {
		builtinDiags = append(builtinDiags, runBuiltinGoTypecheck(lowerRes)...)
	}

	pklLsp := s.getOrStartPklLsp(filepath.Dir(path))

	s.mu.Lock()
	doc.lastGoodConfig = activeCfg
	doc.lowered = lowerRes
	doc.virtGoPath = virtGoPath
	doc.virtGoURI = virtGoURI
	doc.virtPklURI = virtPklURI
	doc.goWorkDir = goWorkDir
	doc.builtinDiags = builtinDiags
	s.virtGoToDoc[virtGoURI] = uri
	s.virtPklToDoc[virtPklURI] = uri
	s.mu.Unlock()

	if gopls != nil && lowerRes != nil {
		if isOpen {
			_ = gopls.notify("textDocument/didOpen", map[string]any{
				"textDocument": map[string]any{
					"uri":        virtGoURI,
					"languageId": "go",
					"version":    version,
					"text":       lowerRes.GoSource,
				},
			})
		} else {
			_ = gopls.notify("textDocument/didChange", map[string]any{
				"textDocument": map[string]any{
					"uri":     virtGoURI,
					"version": version,
				},
				"contentChanges": []map[string]any{{"text": lowerRes.GoSource}},
			})
		}
	}

	if pklLsp != nil && scanRes.Config != nil {
		pklProj := scanRes.PklProjection()
		if isOpen {
			_ = pklLsp.notify("textDocument/didOpen", map[string]any{
				"textDocument": map[string]any{
					"uri":        virtPklURI,
					"languageId": "pkl",
					"version":    version,
					"text":       pklProj,
				},
			})
		} else {
			_ = pklLsp.notify("textDocument/didChange", map[string]any{
				"textDocument": map[string]any{
					"uri":     virtPklURI,
					"version": version,
				},
				"contentChanges": []map[string]any{{"text": pklProj}},
			})
		}
	}

	s.publishMergedDiagnostics(uri)
}

func (s *Server) closeDocument(uri string) {
	s.mu.Lock()
	doc := s.docs[uri]
	delete(s.docs, uri)
	if doc != nil {
		delete(s.virtGoToDoc, doc.virtGoURI)
		delete(s.virtPklToDoc, doc.virtPklURI)
	}
	gopls := (*childLSP)(nil)
	if doc != nil {
		gopls = s.goplsClients[doc.goWorkDir]
	}
	pklLsp := s.pklClient
	s.mu.Unlock()

	if doc != nil {
		if gopls != nil {
			_ = gopls.notify("textDocument/didClose", map[string]any{
				"textDocument": map[string]any{"uri": doc.virtGoURI},
			})
		}
		if pklLsp != nil {
			_ = pklLsp.notify("textDocument/didClose", map[string]any{
				"textDocument": map[string]any{"uri": doc.virtPklURI},
			})
		}
	}
	s.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: []Diagnostic{},
	})
}

func (s *Server) publishMergedDiagnostics(uri string) {
	s.mu.Lock()
	doc := s.docs[uri]
	if doc == nil {
		s.mu.Unlock()
		return
	}
	merged := make([]Diagnostic, 0, len(doc.builtinDiags)+len(doc.goplsDiags)+len(doc.pklLspDiags))
	merged = append(merged, doc.builtinDiags...)
	merged = append(merged, doc.goplsDiags...)
	merged = append(merged, doc.pklLspDiags...)
	s.mu.Unlock()

	s.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: merged,
	})
}

func (s *Server) getOrStartGopls(workDir string) *childLSP {
	if workDir == "" {
		return nil
	}
	s.mu.Lock()
	if existing, ok := s.goplsClients[workDir]; ok {
		s.mu.Unlock()
		return existing
	}
	s.mu.Unlock()

	bin := findOptionalBinary("gopls", "gopls")
	if bin == "" {
		return nil
	}

	client, err := startChildLSP("gopls", bin, []string{"serve"}, workDir, func(virtURI string, diags []Diagnostic) {
		s.mu.Lock()
		werkURI, ok := s.virtGoToDoc[virtURI]
		var doc *openDoc
		if ok {
			doc = s.docs[werkURI]
		}
		var mapped []Diagnostic
		if doc != nil && doc.lowered != nil {
			for _, d := range diags {
				wlStart, wcStart, ok1 := doc.lowered.SpanMap.GoToWerk(d.Range.Start.Line, d.Range.Start.Character)
				wlEnd, wcEnd, ok2 := doc.lowered.SpanMap.GoToWerk(d.Range.End.Line, d.Range.End.Character)
				if ok1 {
					if !ok2 {
						wlEnd, wcEnd = wlStart, wcStart+1
					}
					d.Range = Range{
						Start: Position{Line: wlStart, Character: wcStart},
						End:   Position{Line: wlEnd, Character: wcEnd},
					}
					d.Source = "gopls"
					mapped = append(mapped, d)
				}
			}
			doc.goplsDiags = mapped
		}
		s.mu.Unlock()
		if ok {
			s.publishMergedDiagnostics(werkURI)
		}
	})
	if err != nil {
		return nil
	}

	s.mu.Lock()
	s.goplsClients[workDir] = client
	s.mu.Unlock()
	return client
}

func (s *Server) getOrStartPklLsp(dir string) *childLSP {
	s.mu.Lock()
	if s.triedPklLsp {
		c := s.pklClient
		s.mu.Unlock()
		return c
	}
	s.triedPklLsp = true
	root := s.rootDir
	s.mu.Unlock()

	if root == "" {
		root = pklschema.FindProjectRoot(dir)
	}
	bin := findOptionalBinary("pkl-lsp", "pkl-lsp")
	if bin == "" {
		return nil
	}

	client, err := startChildLSP("pkl-lsp", bin, nil, root, func(virtURI string, diags []Diagnostic) {
		s.mu.Lock()
		werkURI, ok := s.virtPklToDoc[virtURI]
		var doc *openDoc
		if ok {
			doc = s.docs[werkURI]
		}
		if doc != nil && doc.scan != nil && doc.scan.Config != nil {
			var filtered []Diagnostic
			for _, d := range diags {
				if spanContains(doc.scan.Config.Span, d.Range.Start) {
					d.Source = "pkl-lsp"
					filtered = append(filtered, d)
				}
			}
			doc.pklLspDiags = filtered
		}
		s.mu.Unlock()
		if ok {
			s.publishMergedDiagnostics(werkURI)
		}
	})
	if err != nil {
		return nil
	}

	s.mu.Lock()
	s.pklClient = client
	s.mu.Unlock()
	return client
}

// --- Hover, Completion, Definition, SignatureHelp, Symbols, Formatting ---

func (s *Server) handleHover(p TextDocumentPositionParams) *Hover {
	s.mu.Lock()
	doc := s.docs[p.TextDocument.URI]
	s.mu.Unlock()
	if doc == nil || doc.scan == nil {
		return nil
	}

	// 1. Inside `config { ... }` -> Pkl hover
	if doc.scan.Config != nil && spanContains(doc.scan.Config.Span, p.Position) {
		if pklLsp := s.getOrStartPklLsp(filepath.Dir(doc.path)); pklLsp != nil {
			raw, err := pklLsp.call("textDocument/hover", map[string]any{
				"textDocument": map[string]any{"uri": doc.virtPklURI},
				"position":     p.Position,
			}, 2*time.Second)
			if err == nil && len(raw) > 0 && string(raw) != "null" {
				var h Hover
				if json.Unmarshal(raw, &h) == nil && h.Contents.Value != "" {
					return &h
				}
			}
		}
		return hoverInsideConfig(doc, p.Position)
	}

	// 2. Outside `config {}`: check config variable / field selector hover first (includes Pkl `///` doc comments!)
	if h := hoverConfigSelectorInGo(doc, p.Position); h != nil {
		return h
	}

	// 3. Forward to `gopls` if available
	if doc.lowered != nil {
		if goLine, goCol, ok := doc.lowered.SpanMap.WerkToGo(p.Position.Line, p.Position.Character); ok {
			if gopls := s.getOrStartGopls(doc.goWorkDir); gopls != nil {
				raw, err := gopls.call("textDocument/hover", map[string]any{
					"textDocument": map[string]any{"uri": doc.virtGoURI},
					"position":     Position{Line: goLine, Character: goCol},
				}, 2*time.Second)
				if err == nil && len(raw) > 0 && string(raw) != "null" {
					var h Hover
					if json.Unmarshal(raw, &h) == nil && h.Contents.Value != "" {
						h.Range = nil
						return &h
					}
				}
			}
			// Built-in `go/types` fallback hover
			return builtinGoHover(doc.lowered, goLine, goCol)
		}
	}
	return nil
}

func (s *Server) handleCompletion(p TextDocumentPositionParams) any {
	s.mu.Lock()
	doc := s.docs[p.TextDocument.URI]
	s.mu.Unlock()
	if doc == nil || doc.scan == nil {
		return []CompletionItem{}
	}

	// 1. Inside `config { ... }` -> Pkl completions
	if doc.scan.Config != nil && spanContains(doc.scan.Config.Span, p.Position) {
		if pklLsp := s.getOrStartPklLsp(filepath.Dir(doc.path)); pklLsp != nil {
			raw, err := pklLsp.call("textDocument/completion", map[string]any{
				"textDocument": map[string]any{"uri": doc.virtPklURI},
				"position":     p.Position,
			}, 2*time.Second)
			if err == nil && len(raw) > 0 && string(raw) != "null" {
				var anyRes any
				if json.Unmarshal(raw, &anyRes) == nil {
					return anyRes
				}
			}
		}
		return pklBuiltinCompletions()
	}

	// 2. Outside `config {}`: check `<configVar>.` or `<configVar>.<Nested>.` selector completions
	if items, ok := completeConfigSelector(doc, p.Position); ok {
		return items
	}

	// 3. Forward to `gopls` if available
	if doc.lowered != nil {
		if goLine, goCol, ok := doc.lowered.SpanMap.WerkToGo(p.Position.Line, p.Position.Character); ok {
			if gopls := s.getOrStartGopls(doc.goWorkDir); gopls != nil {
				raw, err := gopls.call("textDocument/completion", map[string]any{
					"textDocument": map[string]any{"uri": doc.virtGoURI},
					"position":     Position{Line: goLine, Character: goCol},
				}, 2*time.Second)
				if err == nil && len(raw) > 0 && string(raw) != "null" {
					var anyRes any
					if json.Unmarshal(raw, &anyRes) == nil {
						return anyRes
					}
				}
			}
		}
	}

	return builtinGoCompletions(doc)
}

func (s *Server) handleDefinition(p TextDocumentPositionParams) any {
	s.mu.Lock()
	doc := s.docs[p.TextDocument.URI]
	s.mu.Unlock()
	if doc == nil || doc.scan == nil {
		return nil
	}

	// 1. Inside `config {}` -> forward to `pkl-lsp`
	if doc.scan.Config != nil && spanContains(doc.scan.Config.Span, p.Position) {
		if pklLsp := s.getOrStartPklLsp(filepath.Dir(doc.path)); pklLsp != nil {
			raw, err := pklLsp.call("textDocument/definition", map[string]any{
				"textDocument": map[string]any{"uri": doc.virtPklURI},
				"position":     p.Position,
			}, 2*time.Second)
			if err == nil && len(raw) > 0 && string(raw) != "null" {
				var anyRes any
				if json.Unmarshal(raw, &anyRes) == nil {
					return anyRes
				}
			}
		}
		return nil
	}

	// 2. Cross-language Go-to-Definition: `app` or `app.Db.Host` jumps straight into `config app { ... }`!
	if loc := definitionForConfigSelector(doc, p.Position); loc != nil {
		return *loc
	}

	// 3. Forward to `gopls` (mapping virtual Go locations back to `.werk`!)
	if doc.lowered != nil {
		if goLine, goCol, ok := doc.lowered.SpanMap.WerkToGo(p.Position.Line, p.Position.Character); ok {
			if gopls := s.getOrStartGopls(doc.goWorkDir); gopls != nil {
				raw, err := gopls.call("textDocument/definition", map[string]any{
					"textDocument": map[string]any{"uri": doc.virtGoURI},
					"position":     Position{Line: goLine, Character: goCol},
				}, 2*time.Second)
				if err == nil && len(raw) > 0 && string(raw) != "null" {
					return s.remapLocationsFromGopls(doc, raw)
				}
			}
			return builtinGoDefinition(doc, goLine, goCol)
		}
	}
	return nil
}

func (s *Server) handleSignatureHelp(p TextDocumentPositionParams) any {
	s.mu.Lock()
	doc := s.docs[p.TextDocument.URI]
	s.mu.Unlock()
	if doc == nil || doc.lowered == nil {
		return nil
	}
	if doc.scan != nil && doc.scan.Config != nil && spanContains(doc.scan.Config.Span, p.Position) {
		return nil
	}
	if goLine, goCol, ok := doc.lowered.SpanMap.WerkToGo(p.Position.Line, p.Position.Character); ok {
		if gopls := s.getOrStartGopls(doc.goWorkDir); gopls != nil {
			raw, err := gopls.call("textDocument/signatureHelp", map[string]any{
				"textDocument": map[string]any{"uri": doc.virtGoURI},
				"position":     Position{Line: goLine, Character: goCol},
			}, 2*time.Second)
			if err == nil && len(raw) > 0 && string(raw) != "null" {
				var anyRes any
				if json.Unmarshal(raw, &anyRes) == nil {
					return anyRes
				}
			}
		}
	}
	return nil
}

func (s *Server) handleDocumentSymbols(uri string) []DocumentSymbol {
	s.mu.Lock()
	doc := s.docs[uri]
	s.mu.Unlock()
	if doc == nil || doc.scan == nil {
		return []DocumentSymbol{}
	}

	var syms []DocumentSymbol
	if doc.scan.Config != nil {
		cfgSym := DocumentSymbol{
			Name:           "config " + doc.scan.Config.VarName(),
			Detail:         "Pkl template",
			Kind:           SymbolKindStruct,
			Range:          spanToRange(doc.scan.Config.Span),
			SelectionRange: spanToRange(doc.scan.Config.KeywordSpan),
		}
		if doc.lastGoodConfig != nil {
			keys := make([]string, 0, len(doc.lastGoodConfig.RootFields))
			for k := range doc.lastGoodConfig.RootFields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fm := doc.lastGoodConfig.RootFields[k]
				r := spanToRange(fm.DefSpan)
				cfgSym.Children = append(cfgSym.Children, DocumentSymbol{
					Name:           fm.PklName,
					Detail:         fm.GoType,
					Kind:           SymbolKindProperty,
					Range:          r,
					SelectionRange: r,
				})
			}
		}
		syms = append(syms, cfgSym)
	}

	if doc.lowered != nil {
		fset := token.NewFileSet()
		if pf, err := parser.ParseFile(fset, "", doc.lowered.GoSource, 0); err == nil {
			tfile := fset.File(pf.Pos())
			for _, d := range pf.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name != "main" {
					goPos := tfile.Position(fn.Name.Pos())
					if wl, wc, ok := doc.lowered.SpanMap.GoToWerk(goPos.Line-1, goPos.Column-1); ok {
						r := Range{
							Start: Position{Line: wl, Character: wc},
							End:   Position{Line: wl, Character: wc + len(fn.Name.Name)},
						}
						syms = append(syms, DocumentSymbol{
							Name:           fn.Name.Name,
							Kind:           SymbolKindFunction,
							Range:          r,
							SelectionRange: r,
						})
					}
				}
			}
		}
	}
	return syms
}

func (s *Server) handleFormatting(uri string) []TextEdit {
	s.mu.Lock()
	doc := s.docs[uri]
	s.mu.Unlock()
	if doc == nil {
		return []TextEdit{}
	}
	formatted, err := golower.FormatWerk(doc.path, []byte(doc.text))
	if err != nil || string(formatted) == doc.text {
		return []TextEdit{}
	}
	lines := strings.Split(doc.text, "\n")
	lastLine := len(lines) - 1
	lastCol := len(lines[lastLine])
	return []TextEdit{
		{
			Range: Range{
				Start: Position{Line: 0, Character: 0},
				End:   Position{Line: lastLine, Character: lastCol},
			},
			NewText: string(formatted),
		},
	}
}

// --- Built-in Intelligence Helpers ---

func toLspDiagnostic(d diag.Diagnostic, source string) Diagnostic {
	sev := 1
	if d.Severity == diag.SeverityWarning {
		sev = 2
	}
	return Diagnostic{
		Range:    spanToRange(d.Span),
		Severity: sev,
		Source:   source,
		Message:  d.Message,
	}
}

var pklLineRegex = regexp.MustCompile(`(?m)^\s*(\d+)\s*\|\s*`)

func parsePklDiagnostic(blk *scanner.ConfigBlock, rawMsg string) Diagnostic {
	line := blk.KeywordSpan.Start.Line
	col := blk.KeywordSpan.Start.Col
	if m := pklLineRegex.FindStringSubmatch(rawMsg); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			line = n - 1
			col = 0
		}
	}
	// Extract concise headline from Pkl error output
	summary := strings.TrimSpace(rawMsg)
	for _, l := range strings.Split(rawMsg, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "–– Pkl Error") {
			continue
		}
		summary = t
		break
	}
	return Diagnostic{
		Range: Range{
			Start: Position{Line: line, Character: col},
			End:   Position{Line: line, Character: col + 1},
		},
		Severity: 1,
		Source:   "pkl",
		Message:  summary,
	}
}

func runBuiltinGoTypecheck(lr *golower.LowerResult) []Diagnostic {
	var out []Diagnostic
	fset := token.NewFileSet()
	pf, parseErr := parser.ParseFile(fset, "werk.go", lr.GoSource, parser.AllErrors)
	if parseErr != nil {
		// Map syntax error positions back to `.werk`
		var errList interface{ Error() string } = parseErr
		_ = errList
		posRegex := regexp.MustCompile(`werk\.go:(\d+):(\d+):\s*(.+)`)
		for _, m := range posRegex.FindAllStringSubmatch(parseErr.Error(), -1) {
			ln, _ := strconv.Atoi(m[1])
			col, _ := strconv.Atoi(m[2])
			if wl, wc, ok := lr.SpanMap.GoToWerk(ln-1, col-1); ok {
				out = append(out, Diagnostic{
					Range: Range{
						Start: Position{Line: wl, Character: wc},
						End:   Position{Line: wl, Character: wc + 1},
					},
					Severity: 1,
					Source:   "go",
					Message:  m[3],
				})
			}
		}
		return out
	}

	conf := types.Config{
		Importer: importer.Default(),
		Error: func(err error) {
			if te, ok := err.(types.Error); ok {
				p := fset.Position(te.Pos)
				if wl, wc, mapped := lr.SpanMap.GoToWerk(p.Line-1, p.Column-1); mapped {
					out = append(out, Diagnostic{
						Range: Range{
							Start: Position{Line: wl, Character: wc},
							End:   Position{Line: wl, Character: wc + 1},
						},
						Severity: 1,
						Source:   "go",
						Message:  te.Msg,
					})
				}
			}
		},
	}
	_, _ = conf.Check("main", fset, []*ast.File{pf}, nil)
	return out
}

func extractChainAtCursor(text string, pos Position) ([]string, string) {
	lines := strings.Split(text, "\n")
	if pos.Line < 0 || pos.Line >= len(lines) {
		return nil, ""
	}
	line := lines[pos.Line]
	col := pos.Character
	if col > len(line) {
		col = len(line)
	}
	// Expand right to finish identifier under cursor
	end := col
	for end < len(line) && (line[end] == '_' || unicode.IsLetter(rune(line[end])) || unicode.IsDigit(rune(line[end]))) {
		end++
	}
	start := col
	for start > 0 && (line[start-1] == '.' || line[start-1] == '_' || unicode.IsLetter(rune(line[start-1])) || unicode.IsDigit(rune(line[start-1]))) {
		start--
	}
	expr := line[start:end]
	prefixBeforeCursor := line[start:col]
	if expr == "" {
		return nil, ""
	}
	return strings.Split(expr, "."), prefixBeforeCursor
}

func lookupFieldChain(cfg *pklschema.CompiledConfig, parts []string) *pklschema.FieldMeta {
	if cfg == nil || len(parts) < 2 || parts[0] != cfg.VarName {
		return nil
	}
	fields := cfg.RootFields
	var cur *pklschema.FieldMeta
	for _, seg := range parts[1:] {
		if fields == nil {
			return nil
		}
		m, ok := fields[seg]
		if !ok {
			return nil
		}
		cur = m
		fields = m.Children
	}
	return cur
}

func hoverInsideConfig(doc *openDoc, pos Position) *Hover {
	if doc.lastGoodConfig == nil {
		return nil
	}
	chain, _ := extractChainAtCursor(doc.text, pos)
	if len(chain) == 0 {
		return nil
	}
	word := chain[len(chain)-1]
	for _, fm := range doc.lastGoodConfig.RootFields {
		if fm.PklName == word {
			md := fmt.Sprintf("```pkl\n%s: %s\n```\n*(Go field: `%s.%s` `%s`)*",
				fm.PklName, fm.PklTypeDesc, doc.lastGoodConfig.VarName, fm.GoName, fm.GoType)
			if fm.Doc != "" {
				md += "\n\n" + fm.Doc
			}
			return &Hover{Contents: MarkupContent{Kind: "markdown", Value: md}}
		}
	}
	return nil
}

func hoverConfigSelectorInGo(doc *openDoc, pos Position) *Hover {
	cfg := doc.lastGoodConfig
	if cfg == nil {
		return nil
	}
	parts, _ := extractChainAtCursor(doc.text, pos)
	if len(parts) == 0 || parts[0] != cfg.VarName {
		return nil
	}
	if len(parts) == 1 {
		return &Hover{
			Contents: MarkupContent{
				Kind:  "markdown",
				Value: fmt.Sprintf("```go\nvar %s %s\n```\nCompile-time Pkl configuration object.", cfg.VarName, cfg.StructName),
			},
		}
	}
	fm := lookupFieldChain(cfg, parts)
	if fm == nil {
		return nil
	}
	md := fmt.Sprintf("```go\nfield %s %s\n```\n*Pkl property `%s` (`%s`)*", fm.GoName, fm.GoType, fm.PklName, fm.PklTypeDesc)
	if fm.Doc != "" {
		md += "\n\n" + fm.Doc
	}
	return &Hover{Contents: MarkupContent{Kind: "markdown", Value: md}}
}

func completeConfigSelector(doc *openDoc, pos Position) ([]CompletionItem, bool) {
	cfg := doc.lastGoodConfig
	if cfg == nil {
		return nil, false
	}
	_, before := extractChainAtCursor(doc.text, pos)
	if !strings.Contains(before, ".") {
		return nil, false
	}
	dotIdx := strings.LastIndexByte(before, '.')
	ownerChain := strings.Split(before[:dotIdx], ".")
	if len(ownerChain) == 0 || ownerChain[0] != cfg.VarName {
		return nil, false
	}

	var targetFields map[string]*pklschema.FieldMeta
	if len(ownerChain) == 1 {
		targetFields = cfg.RootFields
	} else {
		fm := lookupFieldChain(cfg, ownerChain)
		if fm != nil {
			targetFields = fm.Children
		}
	}
	if targetFields == nil {
		return nil, false
	}

	keys := make([]string, 0, len(targetFields))
	for k := range targetFields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	items := make([]CompletionItem, 0, len(keys))
	for _, k := range keys {
		fm := targetFields[k]
		item := CompletionItem{
			Label:  fm.GoName,
			Kind:   CompletionKindField,
			Detail: fmt.Sprintf("%s (pkl: %s)", fm.GoType, fm.PklName),
		}
		if fm.Doc != "" {
			item.Documentation = &MarkupContent{Kind: "markdown", Value: fm.Doc}
		}
		items = append(items, item)
	}
	return items, true
}

func definitionForConfigSelector(doc *openDoc, pos Position) *Location {
	cfg := doc.lastGoodConfig
	if cfg == nil || doc.scan == nil || doc.scan.Config == nil {
		return nil
	}
	parts, _ := extractChainAtCursor(doc.text, pos)
	if len(parts) == 0 || parts[0] != cfg.VarName {
		return nil
	}
	if len(parts) == 1 {
		return &Location{
			URI:   doc.uri,
			Range: spanToRange(doc.scan.Config.KeywordSpan),
		}
	}
	fm := lookupFieldChain(cfg, parts)
	if fm == nil {
		return nil
	}
	return &Location{
		URI:   doc.uri,
		Range: spanToRange(fm.DefSpan),
	}
}

func (s *Server) remapLocationsFromGopls(doc *openDoc, raw json.RawMessage) any {
	remapOne := func(loc Location) Location {
		if loc.URI == doc.virtGoURI && doc.lowered != nil {
			if wl, wc, ok := doc.lowered.SpanMap.GoToWerk(loc.Range.Start.Line, loc.Range.Start.Character); ok {
				el, ec, ok2 := doc.lowered.SpanMap.GoToWerk(loc.Range.End.Line, loc.Range.End.Character)
				if !ok2 {
					el, ec = wl, wc+1
				}
				return Location{
					URI: doc.uri,
					Range: Range{
						Start: Position{Line: wl, Character: wc},
						End:   Position{Line: el, Character: ec},
					},
				}
			}
		}
		return loc
	}

	var list []Location
	if err := json.Unmarshal(raw, &list); err == nil {
		for i := range list {
			list[i] = remapOne(list[i])
		}
		return list
	}
	var single Location
	if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
		return remapOne(single)
	}
	return nil
}

func builtinGoHover(lr *golower.LowerResult, goLine, goCol int) *Hover {
	fset := token.NewFileSet()
	pf, err := parser.ParseFile(fset, "werk.go", lr.GoSource, 0)
	if err != nil {
		return nil
	}
	info := &types.Info{
		Defs: make(map[*ast.Ident]types.Object),
		Uses: make(map[*ast.Ident]types.Object),
	}
	conf := types.Config{Importer: importer.Default(), Error: func(error) {}}
	_, _ = conf.Check("main", fset, []*ast.File{pf}, info)

	tfile := fset.File(pf.Pos())
	var hit types.Object
	ast.Inspect(pf, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		p := tfile.Position(id.Pos())
		if p.Line-1 == goLine && goCol >= p.Column-1 && goCol <= p.Column-1+len(id.Name) {
			if obj := info.Uses[id]; obj != nil {
				hit = obj
			} else if obj := info.Defs[id]; obj != nil {
				hit = obj
			}
		}
		return true
	})
	if hit == nil {
		return nil
	}
	return &Hover{
		Contents: MarkupContent{
			Kind:  "markdown",
			Value: fmt.Sprintf("```go\n%s\n```", hit.String()),
		},
	}
}

func builtinGoDefinition(doc *openDoc, goLine, goCol int) *Location {
	fset := token.NewFileSet()
	pf, err := parser.ParseFile(fset, "werk.go", doc.lowered.GoSource, 0)
	if err != nil {
		return nil
	}
	info := &types.Info{
		Defs: make(map[*ast.Ident]types.Object),
		Uses: make(map[*ast.Ident]types.Object),
	}
	conf := types.Config{Importer: importer.Default(), Error: func(error) {}}
	_, _ = conf.Check("main", fset, []*ast.File{pf}, info)

	tfile := fset.File(pf.Pos())
	var hit types.Object
	ast.Inspect(pf, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		p := tfile.Position(id.Pos())
		if p.Line-1 == goLine && goCol >= p.Column-1 && goCol <= p.Column-1+len(id.Name) {
			hit = info.Uses[id]
		}
		return true
	})
	if hit == nil || !hit.Pos().IsValid() {
		return nil
	}
	defFile := fset.File(hit.Pos())
	if defFile == nil || defFile.Name() != "werk.go" {
		return nil
	}
	dp := defFile.Position(hit.Pos())
	if wl, wc, ok := doc.lowered.SpanMap.GoToWerk(dp.Line-1, dp.Column-1); ok {
		return &Location{
			URI: doc.uri,
			Range: Range{
				Start: Position{Line: wl, Character: wc},
				End:   Position{Line: wl, Character: wc + len(hit.Name())},
			},
		}
	}
	return nil
}

func builtinGoCompletions(doc *openDoc) []CompletionItem {
	var items []CompletionItem
	if doc.lastGoodConfig != nil {
		items = append(items, CompletionItem{
			Label:  doc.lastGoodConfig.VarName,
			Kind:   CompletionKindVariable,
			Detail: doc.lastGoodConfig.StructName + " (Pkl config)",
		})
	}
	for _, kw := range []string{"config", "import", "func", "type", "const", "var", "for", "if", "else", "switch", "defer"} {
		items = append(items, CompletionItem{
			Label: kw,
			Kind:  CompletionKindKeyword,
		})
	}
	return items
}

func pklBuiltinCompletions() []CompletionItem {
	pklTypes := []string{
		"String", "Boolean", "Int", "Int8", "Int16", "Int32", "Int64",
		"UInt", "UInt8", "UInt16", "UInt32", "Float", "Duration", "DataSize",
		"Listing", "Mapping", "Dynamic", "Pair", "class", "typealias", "hidden", "local",
	}
	items := make([]CompletionItem, 0, len(pklTypes))
	for _, t := range pklTypes {
		items = append(items, CompletionItem{
			Label: t,
			Kind:  CompletionKindKeyword,
		})
	}
	return items
}
