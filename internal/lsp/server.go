package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ABM2205/werklang/internal/diag"
	"github.com/ABM2205/werklang/internal/format"
	"github.com/ABM2205/werklang/internal/parser"
	"github.com/ABM2205/werklang/internal/runner"
	"github.com/ABM2205/werklang/internal/types"
)

type Server struct {
	in      *bufio.Reader
	out     io.Writer
	writeMu sync.Mutex

	mu      sync.Mutex
	rootDir string
	docs    map[string]string                // file path -> buffer text
	checked map[string]*types.CheckedProgram // file path -> latest CheckedProgram
}

func NewServer(in io.Reader, out io.Writer) *Server {
	return &Server{
		in:      bufio.NewReader(in),
		out:     out,
		docs:    make(map[string]string),
		checked: make(map[string]*types.CheckedProgram),
	}
}

func (s *Server) Run() error {
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
		shouldExit := s.handleMessage(&msg)
		if shouldExit {
			return nil
		}
	}
}

func (s *Server) sendResult(id *json.RawMessage, result any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = writeMessage(s.out, rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (s *Server) sendError(id *json.RawMessage, code int, message string) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = writeMessage(s.out, rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
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

func (s *Server) handleMessage(msg *rpcMessage) bool {
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

		s.sendResult(msg.ID, map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync":           1, // Full sync
				"hoverProvider":              true,
				"definitionProvider":         true,
				"documentSymbolProvider":     true,
				"documentFormattingProvider": true,
				"completionProvider": map[string]any{
					"triggerCharacters": []string{".", "\"", "{", ":"},
				},
			},
			"serverInfo": map[string]any{
				"name":    "werklang-lsp",
				"version": "0.1.0",
			},
		})

	case "initialized":
		// no-op

	case "shutdown":
		s.sendResult(msg.ID, nil)

	case "exit":
		return true

	case "textDocument/didOpen":
		var p DidOpenTextDocumentParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.updateDocument(p.TextDocument.URI, p.TextDocument.Text)
		}

	case "textDocument/didChange":
		var p DidChangeTextDocumentParams
		if json.Unmarshal(msg.Params, &p) == nil && len(p.ContentChanges) > 0 {
			latest := p.ContentChanges[len(p.ContentChanges)-1].Text
			s.updateDocument(p.TextDocument.URI, latest)
		}

	case "textDocument/didSave":
		var p DidSaveTextDocumentParams
		if json.Unmarshal(msg.Params, &p) == nil {
			path := URIToPath(p.TextDocument.URI)
			s.mu.Lock()
			txt, ok := s.docs[path]
			s.mu.Unlock()
			if ok {
				s.updateDocument(p.TextDocument.URI, txt)
			}
		}

	case "textDocument/didClose":
		var p DidCloseTextDocumentParams
		if json.Unmarshal(msg.Params, &p) == nil {
			path := URIToPath(p.TextDocument.URI)
			s.mu.Lock()
			delete(s.docs, path)
			delete(s.checked, path)
			s.mu.Unlock()
			s.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
				URI:         p.TextDocument.URI,
				Diagnostics: []Diagnostic{},
			})
		}

	case "textDocument/hover":
		var p TextDocumentPositionParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.sendResult(msg.ID, s.Hover(p.TextDocument.URI, p.Position))
		} else {
			s.sendResult(msg.ID, nil)
		}

	case "textDocument/definition":
		var p TextDocumentPositionParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.sendResult(msg.ID, s.Definition(p.TextDocument.URI, p.Position))
		} else {
			s.sendResult(msg.ID, nil)
		}

	case "textDocument/completion":
		var p TextDocumentPositionParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.sendResult(msg.ID, s.Complete(p.TextDocument.URI, p.Position))
		} else {
			s.sendResult(msg.ID, []CompletionItem{})
		}

	case "textDocument/documentSymbol":
		var p DocumentSymbolParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.sendResult(msg.ID, s.DocumentSymbols(p.TextDocument.URI))
		} else {
			s.sendResult(msg.ID, []DocumentSymbol{})
		}

	case "textDocument/formatting":
		var p DocumentFormattingParams
		if json.Unmarshal(msg.Params, &p) == nil {
			s.sendResult(msg.ID, s.FormatDocument(p.TextDocument.URI))
		} else {
			s.sendResult(msg.ID, []TextEdit{})
		}

	default:
		if msg.ID != nil {
			s.sendError(msg.ID, -32601, "method not found: "+msg.Method)
		}
	}
	return false
}

func (s *Server) updateDocument(uri, text string) *types.CheckedProgram {
	path := URIToPath(uri)
	s.mu.Lock()
	s.docs[path] = text
	overlay := make(map[string]string, len(s.docs))
	for k, v := range s.docs {
		overlay[k] = v
	}
	workDir := s.rootDir
	s.mu.Unlock()

	if workDir == "" {
		workDir = filepath.Dir(path)
	}
	cp := runner.Check(path, workDir, overlay)

	s.mu.Lock()
	s.checked[path] = cp
	s.mu.Unlock()

	diags := make([]Diagnostic, 0, len(cp.Diagnostics))
	for _, d := range cp.Diagnostics {
		if d.Span.File != "" && filepath.Clean(d.Span.File) != filepath.Clean(path) {
			continue
		}
		sev := 1
		if d.Severity == diag.SeverityWarning {
			sev = 2
		}
		diags = append(diags, Diagnostic{
			Range:    spanToRange(d.Span),
			Severity: sev,
			Source:   "werklang",
			Message:  d.Message,
		})
	}
	s.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: diags,
	})
	return cp
}

func (s *Server) getChecked(uri string) (*types.CheckedProgram, string) {
	path := URIToPath(uri)
	s.mu.Lock()
	cp := s.checked[path]
	txt := s.docs[path]
	workDir := s.rootDir
	s.mu.Unlock()
	if cp != nil {
		return cp, txt
	}
	if workDir == "" {
		workDir = filepath.Dir(path)
	}
	cp = runner.Check(path, workDir, nil)
	return cp, txt
}

// --- Hover ---

func (s *Server) Hover(uri string, pos Position) *Hover {
	cp, _ := s.getChecked(uri)
	if cp == nil || cp.Prog == nil {
		return nil
	}

	// 1. Check Target declarations
	for _, ti := range cp.TargetOrder {
		if spanContains(ti.DefSpan, pos) {
			md := fmt.Sprintf("```werk\ntarget %s\n```", ti.Name)
			if ti.Desc != "" {
				md += "\n\n" + ti.Desc
			}
			if len(ti.Deps) > 0 {
				md += fmt.Sprintf("\n\n**deps**: `%s`", strings.Join(ti.Deps, ", "))
			}
			if len(ti.ParallelDeps) > 0 {
				md += fmt.Sprintf("\n\n**parallel_deps**: `%s`", strings.Join(ti.ParallelDeps, ", "))
			}
			r := spanToRange(ti.DefSpan)
			return &Hover{Contents: MarkupContent{Kind: "markdown", Value: md}, Range: &r}
		}
	}

	// 2. Check SelectorExpr (`pkg.Fn` or `cfg.field`)
	for sel, sym := range cp.SelSymbols {
		if spanContains(sel.SelSpan, pos) {
			md := fmt.Sprintf("```werk\n%s: %s\n```", sym.Name, sym.Type.String())
			if sym.Doc != "" {
				md += "\n\n" + sym.Doc
			}
			r := spanToRange(sel.SelSpan)
			return &Hover{Contents: MarkupContent{Kind: "markdown", Value: md}, Range: &r}
		}
	}

	// 3. Check IdentExpr
	for id, sym := range cp.IdentSymbols {
		if spanContains(id.Span, pos) {
			md := fmt.Sprintf("```werk\n%s (%s): %s\n```", sym.Name, sym.Kind, sym.Type.String())
			if sym.Doc != "" {
				md += "\n\n" + sym.Doc
			}
			r := spanToRange(id.Span)
			return &Hover{Contents: MarkupContent{Kind: "markdown", Value: md}, Range: &r}
		}
	}

	// 4. Check FieldDecl names
	for fd, ft := range cp.FieldTypes {
		if spanContains(fd.NameSpan, pos) && ft != nil {
			md := fmt.Sprintf("```werk\n%s %s\n```", fd.Name, ft.String())
			r := spanToRange(fd.NameSpan)
			return &Hover{Contents: MarkupContent{Kind: "markdown", Value: md}, Range: &r}
		}
	}

	return nil
}

// --- Go to Definition ---

func (s *Server) Definition(uri string, pos Position) *Location {
	cp, _ := s.getChecked(uri)
	if cp == nil || cp.Prog == nil {
		return nil
	}

	// 1. Import path string -> jump to imported .werk or .pkl file
	for _, imp := range cp.Prog.Imports {
		if spanContains(imp.Span, pos) {
			alias := imp.Alias
			if alias == "" {
				alias = filepath.Base(imp.Path)
				alias = strings.TrimSuffix(alias, filepath.Ext(alias))
			}
			if sym := cp.TopScope.LookupLocal(alias); sym != nil && sym.DefSpan.File != "" && sym.DefSpan.File != cp.Prog.Path {
				return &Location{
					URI:   PathToURI(sym.DefSpan.File),
					Range: spanToRange(sym.DefSpan),
				}
			}
		}
	}

	// 2. Target name inside `deps = ["test"]` or `parallel_deps = ["docker:build"]`
	for _, ti := range cp.TargetOrder {
		for _, st := range ti.Decl.Body {
			fd, ok := st.(*parser.FieldDecl)
			if !ok || (fd.Name != "deps" && fd.Name != "parallel_deps") {
				continue
			}
			sl, ok := fd.Value.(*parser.SliceLit)
			if !ok {
				continue
			}
			for _, el := range sl.Elems {
				strLit, ok := el.(*parser.StringLit)
				if !ok || !spanContains(strLit.Span, pos) {
					continue
				}
				if localTgt, ok := cp.Targets[strLit.Value]; ok {
					return &Location{
						URI:   PathToURI(localTgt.DefSpan.File),
						Range: spanToRange(localTgt.DefSpan),
					}
				}
				if idx := strings.IndexByte(strLit.Value, ':'); idx > 0 {
					ns, tName := strLit.Value[:idx], strLit.Value[idx+1:]
					for _, mp := range cp.MageImports {
						if mp.Alias == ns {
							if mt, ok := mp.Targets[strings.ToLower(tName)]; ok && mt.DefSpan.File != "" {
								return &Location{
									URI:   PathToURI(mt.DefSpan.File),
									Range: spanToRange(mt.DefSpan),
								}
							}
						}
					}
				}
			}
		}
	}

	// 3. Selector expression (`app.port`, `filepath.Join`, `docker.Build`)
	for sel, sym := range cp.SelSymbols {
		if spanContains(sel.SelSpan, pos) && sym.DefSpan.File != "" {
			return &Location{
				URI:   PathToURI(sym.DefSpan.File),
				Range: spanToRange(sym.DefSpan),
			}
		}
	}

	// 4. Identifier expression
	for id, sym := range cp.IdentSymbols {
		if spanContains(id.Span, pos) && sym.DefSpan.File != "" {
			return &Location{
				URI:   PathToURI(sym.DefSpan.File),
				Range: spanToRange(sym.DefSpan),
			}
		}
	}

	return nil
}

// --- Autocompletion ---

func (s *Server) Complete(uri string, pos Position) []CompletionItem {
	cp, text := s.getChecked(uri)
	if cp == nil || cp.Prog == nil {
		return nil
	}

	// 1. Check if cursor is after `receiver.` (either via AST SelectorExpr or text line prefix)
	if selItems, ok := s.completeSelector(cp, text, pos); ok {
		return selItems
	}

	// 2. Check if cursor is inside `deps = ["..."]` or `parallel_deps = ["..."]`
	if depItems, ok := s.completeTargetDeps(cp, pos); ok {
		return depItems
	}

	// 3. Check if cursor is inside `Struct { ... }` (`StructAmendExpr`)
	if amendItems, ok := s.completeStructAmend(cp, pos); ok {
		return amendItems
	}

	// 4. General completions (built-ins, symbols in scope, target metadata, keywords)
	var items []CompletionItem
	inTarget := false
	for _, ti := range cp.TargetOrder {
		if spanContains(ti.Decl.Span, pos) {
			inTarget = true
			break
		}
	}

	if inTarget {
		for _, meta := range []struct{ name, detail string }{
			{"desc", `string — Target description shown in werk -l`},
			{"deps", `[]string — Sequential target dependencies (mg.SerialDeps)`},
			{"parallel_deps", `[]string — Parallel target dependencies (mg.Deps)`},
			{"inputs", `[]string — Input file paths for incremental rebuild checks`},
			{"outputs", `[]string — Output file paths for incremental rebuild checks`},
		} {
			items = append(items, CompletionItem{
				Label:  meta.name,
				Kind:   CompletionKindProperty,
				Detail: meta.detail,
			})
		}
	}

	for _, sym := range cp.TopScope.AllSymbols() {
		kind := CompletionKindVariable
		switch sym.Kind {
		case types.SymBuiltin:
			kind = CompletionKindFunction
		case types.SymModule, types.SymGoPkg, types.SymMagePkg:
			kind = CompletionKindModule
		case types.SymConfig:
			kind = CompletionKindStruct
		case types.SymTarget:
			kind = CompletionKindFunction
		}
		var doc *MarkupContent
		if sym.Doc != "" {
			doc = &MarkupContent{Kind: "markdown", Value: sym.Doc}
		}
		items = append(items, CompletionItem{
			Label:         sym.Name,
			Kind:          kind,
			Detail:        sym.Type.String(),
			Documentation: doc,
		})
	}

	for _, kw := range []string{"import", "amends", "config", "target", "if", "then", "else", "for", "in", "int", "float", "string", "bool", "true", "false"} {
		items = append(items, CompletionItem{
			Label: kw,
			Kind:  CompletionKindKeyword,
		})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	return items
}

func (s *Server) completeSelector(cp *types.CheckedProgram, text string, pos Position) ([]CompletionItem, bool) {
	// First check AST SelectorExprs covering cursor
	for expr, _ := range cp.ExprTypes {
		sel, ok := expr.(*parser.SelectorExpr)
		if !ok {
			continue
		}
		if pos.Line == sel.Span.End.Line && pos.Character >= sel.X.NodeSpan().End.Col && pos.Character <= sel.Span.End.Col+1 {
			if baseType := cp.ExprTypes[sel.X]; baseType != nil && baseType != types.Invalid {
				return itemsForReceiverType(baseType), true
			}
		}
	}

	// Fallback: inspect current line text up to cursor for `<ident>.<prefix>`
	lines := strings.Split(text, "\n")
	if pos.Line < 0 || pos.Line >= len(lines) {
		return nil, false
	}
	line := lines[pos.Line]
	col := pos.Character
	if col > len(line) {
		col = len(line)
	}
	prefix := line[:col]
	dotIdx := strings.LastIndexByte(prefix, '.')
	if dotIdx <= 0 {
		return nil, false
	}
	// Extract identifier immediately before '.'
	endIdent := dotIdx
	startIdent := endIdent
	for startIdent > 0 {
		ch := prefix[startIdent-1]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' {
			startIdent--
		} else {
			break
		}
	}
	if startIdent == endIdent {
		return nil, false
	}
	recvName := prefix[startIdent:endIdent]
	if sym := cp.TopScope.Lookup(recvName); sym != nil {
		return itemsForReceiverType(sym.Type), true
	}
	return nil, false
}

func itemsForReceiverType(t types.Type) []CompletionItem {
	var items []CompletionItem
	switch bt := t.(type) {
	case *types.ModuleType:
		for _, f := range bt.Fields {
			items = append(items, CompletionItem{
				Label:  f.Name,
				Kind:   CompletionKindField,
				Detail: f.Type.String(),
			})
		}
	case *types.GoPackageType:
		for _, fn := range bt.Funcs {
			var doc *MarkupContent
			if fn.Doc != "" {
				doc = &MarkupContent{Kind: "markdown", Value: fn.Doc}
			}
			items = append(items, CompletionItem{
				Label:         fn.Name,
				Kind:          CompletionKindFunction,
				Detail:        fn.SignatureString(),
				Documentation: doc,
			})
		}
		for _, vr := range bt.Vars {
			items = append(items, CompletionItem{
				Label:  vr.Name,
				Kind:   CompletionKindVariable,
				Detail: vr.WerkType.String(),
			})
		}
	case *types.MagePackageType:
		for _, mt := range bt.Ordered {
			items = append(items, CompletionItem{
				Label:  mt.Name,
				Kind:   CompletionKindFunction,
				Detail: "Mage target",
			})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	return items
}

func (s *Server) completeTargetDeps(cp *types.CheckedProgram, pos Position) ([]CompletionItem, bool) {
	for _, ti := range cp.TargetOrder {
		for _, st := range ti.Decl.Body {
			fd, ok := st.(*parser.FieldDecl)
			if !ok || (fd.Name != "deps" && fd.Name != "parallel_deps") || fd.Value == nil {
				continue
			}
			if !spanContains(fd.Value.NodeSpan(), pos) {
				continue
			}
			var items []CompletionItem
			for _, cand := range cp.TargetOrder {
				if cand.Name == ti.Name {
					continue
				}
				items = append(items, CompletionItem{
					Label:  cand.Name,
					Kind:   CompletionKindFunction,
					Detail: cand.Desc,
				})
			}
			for _, mp := range cp.MageImports {
				for _, mt := range mp.Ordered {
					label := mp.Alias + ":" + strings.ToLower(mt.Name)
					items = append(items, CompletionItem{
						Label:  label,
						Kind:   CompletionKindFunction,
						Detail: mt.Doc,
					})
				}
			}
			sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
			return items, true
		}
	}
	return nil, false
}

func (s *Server) completeStructAmend(cp *types.CheckedProgram, pos Position) ([]CompletionItem, bool) {
	var best *parser.StructAmendExpr
	for expr := range cp.ExprTypes {
		sa, ok := expr.(*parser.StructAmendExpr)
		if !ok || !spanContains(sa.Span, pos) {
			continue
		}
		if best == nil || sa.Span.Start.Offset >= best.Span.Start.Offset {
			best = sa
		}
	}
	if best == nil {
		return nil, false
	}
	mod, ok := cp.ExprTypes[best].(*types.ModuleType)
	if !ok {
		if bt, ok2 := cp.ExprTypes[best.Base].(*types.ModuleType); ok2 {
			mod = bt
		} else {
			return nil, false
		}
	}
	existing := make(map[string]bool)
	for _, fd := range best.Fields {
		existing[fd.Name] = true
	}
	var items []CompletionItem
	for _, f := range mod.Fields {
		if existing[f.Name] {
			continue
		}
		detail := f.Type.String()
		sortKey := "1_" + f.Name
		if f.Required {
			detail += " (required)"
			sortKey = "0_" + f.Name
		}
		items = append(items, CompletionItem{
			Label:      f.Name,
			Kind:       CompletionKindField,
			Detail:     detail,
			InsertText: f.Name + " = ",
			SortText:   sortKey,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].SortText < items[j].SortText })
	return items, true
}

// --- Document Symbols ---

func (s *Server) DocumentSymbols(uri string) []DocumentSymbol {
	cp, _ := s.getChecked(uri)
	if cp == nil || cp.Prog == nil {
		return []DocumentSymbol{}
	}

	var syms []DocumentSymbol
	for _, item := range cp.Prog.Items {
		switch n := item.(type) {
		case *parser.ImportDecl:
			name := n.Alias
			if name == "" {
				name = n.Path
			}
			syms = append(syms, DocumentSymbol{
				Name:           name,
				Detail:         n.Path,
				Kind:           SymbolKindModule,
				Range:          spanToRange(n.Span),
				SelectionRange: spanToRange(n.PathSpan),
			})
		case *parser.ConfigDecl:
			var children []DocumentSymbol
			for _, fd := range n.Fields {
				ft := cp.FieldTypes[fd]
				det := ""
				if ft != nil {
					det = ft.String()
				}
				children = append(children, DocumentSymbol{
					Name:           fd.Name,
					Detail:         det,
					Kind:           SymbolKindField,
					Range:          spanToRange(fd.Span),
					SelectionRange: spanToRange(fd.NameSpan),
				})
			}
			syms = append(syms, DocumentSymbol{
				Name:           n.Name,
				Detail:         "config",
				Kind:           SymbolKindStruct,
				Range:          spanToRange(n.Span),
				SelectionRange: spanToRange(n.NameSpan),
				Children:       children,
			})
		case *parser.FieldDecl:
			ft := cp.FieldTypes[n]
			det := ""
			if ft != nil {
				det = ft.String()
			}
			syms = append(syms, DocumentSymbol{
				Name:           n.Name,
				Detail:         det,
				Kind:           SymbolKindProperty,
				Range:          spanToRange(n.Span),
				SelectionRange: spanToRange(n.NameSpan),
			})
		case *parser.TargetDecl:
			det := "target"
			if ti, ok := cp.Targets[n.Name]; ok && ti.Desc != "" {
				det = ti.Desc
			}
			syms = append(syms, DocumentSymbol{
				Name:           n.Name,
				Detail:         det,
				Kind:           SymbolKindFunction,
				Range:          spanToRange(n.Span),
				SelectionRange: spanToRange(n.NameSpan),
			})
		}
	}
	return syms
}

// --- Formatting ---

func (s *Server) FormatDocument(uri string) []TextEdit {
	path := URIToPath(uri)
	s.mu.Lock()
	txt, ok := s.docs[path]
	s.mu.Unlock()
	if !ok || txt == "" {
		return []TextEdit{}
	}
	formatted, err := format.Source(path, txt)
	if err != nil || formatted == txt {
		return []TextEdit{}
	}
	lines := strings.Split(txt, "\n")
	endLine := len(lines)
	return []TextEdit{{
		Range: Range{
			Start: Position{Line: 0, Character: 0},
			End:   Position{Line: endLine + 1, Character: 0},
		},
		NewText: formatted,
	}}
}
