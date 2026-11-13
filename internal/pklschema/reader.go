package pklschema

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ABM2205/werklang/internal/scanner"
	"github.com/apple/pkl-go/pkl"
)

// WerkModuleReader implements pkl.ModuleReader for the `werk:` URI scheme.
// It resolves paths relative to RootDir (rejecting path traversal outside RootDir),
// returning 1:1 line-preserving Pkl projections for `.werk` files and raw text
// for `.pkl` files (so relative imports inside `config {}` work naturally).
type WerkModuleReader struct {
	RootDir string

	mu      sync.RWMutex
	Overlay map[string][]byte // Optional unsaved file buffers keyed by clean abs path
}

var _ pkl.ModuleReader = (*WerkModuleReader)(nil)

func NewWerkModuleReader(rootDir string, overlay map[string][]byte) (*WerkModuleReader, error) {
	if rootDir == "" {
		rootDir = "."
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}
	cleanOverlay := make(map[string][]byte, len(overlay))
	for k, v := range overlay {
		if abs, err := filepath.Abs(k); err == nil {
			cleanOverlay[filepath.Clean(abs)] = v
		}
	}
	return &WerkModuleReader{
		RootDir: filepath.Clean(absRoot),
		Overlay: cleanOverlay,
	}, nil
}

func (r *WerkModuleReader) Scheme() string            { return "werk" }
func (r *WerkModuleReader) HasHierarchicalUris() bool { return true }
func (r *WerkModuleReader) IsLocal() bool             { return true }
func (r *WerkModuleReader) IsGlobbable() bool         { return true }

func (r *WerkModuleReader) resolveSandboxedPath(u url.URL) (string, error) {
	rel := u.Path
	if rel == "" && u.Opaque != "" {
		rel = u.Opaque
	}
	rel = strings.TrimPrefix(rel, "/")
	target := filepath.Clean(filepath.Join(r.RootDir, filepath.FromSlash(rel)))

	relCheck, err := filepath.Rel(r.RootDir, target)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("werk URI %q resolves outside project root %s", u.String(), r.RootDir)
	}
	return target, nil
}

func (r *WerkModuleReader) readBytes(absPath string) ([]byte, error) {
	r.mu.RLock()
	if raw, ok := r.Overlay[absPath]; ok {
		r.mu.RUnlock()
		return raw, nil
	}
	r.mu.RUnlock()
	return os.ReadFile(absPath)
}

func (r *WerkModuleReader) Read(u url.URL) (string, error) {
	fullPath, err := r.resolveSandboxedPath(u)
	if err != nil {
		return "", err
	}
	raw, err := r.readBytes(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", pkl.ResourceNotFound
		}
		return "", err
	}

	if strings.HasSuffix(strings.ToLower(fullPath), ".pkl") {
		return string(raw), nil
	}

	scanned := scanner.Scan(fullPath, raw)
	return scanned.PklProjection(), nil
}

func (r *WerkModuleReader) ListElements(u url.URL) ([]pkl.PathElement, error) {
	dirPath, err := r.resolveSandboxedPath(u)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, pkl.ResourceNotFound
		}
		return nil, err
	}
	var out []pkl.PathElement
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, pkl.NewPathElement(e.Name(), e.IsDir()))
	}
	return out, nil
}

// RunExternalReader starts the stdio MessagePack server implementing Pkl's
// External Module Reader protocol (`werk pkl-reader`).
func RunExternalReader(rootDir string) error {
	if rootDir == "" {
		rootDir = FindProjectRoot(".")
	}
	reader, err := NewWerkModuleReader(rootDir, nil)
	if err != nil {
		return err
	}
	client, err := pkl.NewExternalReaderClient(
		pkl.WithExternalClientModuleReader(reader),
	)
	if err != nil {
		return err
	}
	return client.Run()
}

// FindProjectRoot walks upward from startDir looking for `PklProject` first,
// then `go.mod`. If neither exists, returns the absolute path of startDir.
func FindProjectRoot(startDir string) string {
	if startDir == "" {
		startDir = "."
	}
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return startDir
	}
	info, err := os.Stat(abs)
	if err == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}

	// First pass: look for PklProject
	for cur := abs; ; {
		if st, err := os.Stat(filepath.Join(cur, "PklProject")); err == nil && !st.IsDir() {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}

	// Second pass: look for go.mod
	for cur := abs; ; {
		if st, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil && !st.IsDir() {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}

	return abs
}
