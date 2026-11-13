package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// childLSP manages a persistent child Language Server subprocess (`gopls` or `pkl-lsp`).
type childLSP struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	writeMu sync.Mutex

	nextID  atomic.Int64
	pendMu  sync.Mutex
	pending map[int64]chan rpcMessage

	onDiagnostics func(uri string, diags []Diagnostic)
}

func startChildLSP(name string, binPath string, args []string, rootDir string, onDiag func(string, []Diagnostic)) (*childLSP, error) {
	cmd := exec.Command(binPath, args...)
	if rootDir != "" {
		cmd.Dir = rootDir
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	c := &childLSP{
		name:          name,
		cmd:           cmd,
		stdin:         stdin,
		pending:       make(map[int64]chan rpcMessage),
		onDiagnostics: onDiag,
	}
	go c.readLoop(bufio.NewReader(stdout))

	rootURI := PathToURI(rootDir)
	initParams := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"synchronization": map[string]any{"didSave": true},
				"completion":      map[string]any{},
				"hover":           map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"definition":      map[string]any{},
				"publishDiagnostics": map[string]any{},
			},
		},
	}
	if _, err := c.call("initialize", initParams, 5*time.Second); err != nil {
		c.close()
		return nil, err
	}
	_ = c.notify("initialized", map[string]any{})
	return c, nil
}

func (c *childLSP) readLoop(r *bufio.Reader) {
	for {
		raw, err := readMessage(r)
		if err != nil {
			c.pendMu.Lock()
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.pendMu.Unlock()
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		// Response to one of our requests
		if msg.ID != nil && msg.Method == "" {
			var id int64
			if err := json.Unmarshal(*msg.ID, &id); err == nil {
				c.pendMu.Lock()
				ch, ok := c.pending[id]
				if ok {
					delete(c.pending, id)
				}
				c.pendMu.Unlock()
				if ok {
					ch <- msg
				}
			}
			continue
		}

		// Server-initiated request (e.g. workspace/configuration): reply null so server doesn't block
		if msg.ID != nil && msg.Method != "" {
			c.writeMu.Lock()
			_ = writeMessage(c.stdin, rpcResponse{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result:  nil,
			})
			c.writeMu.Unlock()
			continue
		}

		// Notification (e.g. textDocument/publishDiagnostics)
		if msg.Method == "textDocument/publishDiagnostics" && c.onDiagnostics != nil {
			var p PublishDiagnosticsParams
			if err := json.Unmarshal(msg.Params, &p); err == nil {
				c.onDiagnostics(p.URI, p.Diagnostics)
			}
		}
	}
}

func (c *childLSP) notify(method string, params any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeMessage(c.stdin, rpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

func (c *childLSP) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	idRaw, _ := json.Marshal(id)
	idPtr := json.RawMessage(idRaw)

	rawParams, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	ch := make(chan rpcMessage, 1)
	c.pendMu.Lock()
	c.pending[id] = ch
	c.pendMu.Unlock()

	c.writeMu.Lock()
	err = writeMessage(c.stdin, map[string]any{
		"jsonrpc": "2.0",
		"id":      idPtr,
		"method":  method,
		"params":  json.RawMessage(rawParams),
	})
	c.writeMu.Unlock()
	if err != nil {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("%s closed", c.name)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("%s error %d: %s", c.name, resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-time.After(timeout):
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return nil, fmt.Errorf("%s %s timed out", c.name, method)
	}
}

func (c *childLSP) close() {
	if c == nil {
		return
	}
	_ = c.stdin.Close()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
}

func findOptionalBinary(name string, zedSubdir string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidates := []string{
		filepath.Join(home, "go", "bin", name),
		filepath.Join(home, ".local", "bin", name),
		filepath.Join("/usr/local/bin", name),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if zedSubdir != "" {
		zedLangDir := filepath.Join(home, ".local", "share", "zed", "languages", zedSubdir)
		var found string
		_ = filepath.WalkDir(zedLangDir, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Name() == name {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found
		}
	}
	return ""
}
