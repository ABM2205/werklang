package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/ABM2205/werklang/internal/golower"
	"github.com/ABM2205/werklang/internal/pklschema"
	"github.com/ABM2205/werklang/internal/scanner"
)

const cacheSchemaVersion = "werk-v2.0"

// CacheRoot returns the user cache directory used for standalone script workspaces and compiled binaries.
func CacheRoot() string {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "werk")
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "werk")
	}
	return filepath.Join(os.TempDir(), "werk-cache")
}

// RunOptions configures executing a `.werk` script.
type RunOptions struct {
	ScriptPath  string
	ConfigFiles []string
	ScriptArgs  []string
	WorkDir     string
	Rebuild     bool
	ExecReplace bool // Replace current process via syscall.Exec on Unix
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
}

// BuildOptions configures compiling a `.werk` script or package to a native Go binary.
type BuildOptions struct {
	Target      string // .werk file path or directory (defaults to ".")
	OutputPath  string // -o binary path
	ConfigFiles []string
	GoBuildArgs []string
	Stdout      io.Writer
	Stderr      io.Writer
}

// GenerateOptions configures materializing `*_werk.go` files on disk.
type GenerateOptions struct {
	Targets     []string
	ConfigFiles []string
}

// TestOptions configures running `go test` with `.werk` overlays applied.
type TestOptions struct {
	Targets     []string
	ConfigFiles []string
	GoTestArgs  []string
	Stdout      io.Writer
	Stderr      io.Writer
}

// EnsureStandaloneWorkspace creates or updates the cached synthetic Go module
// workspace for a single-file `.werk` script outside any `go.mod` directory.
// Used by both `runner` and `werk lsp`.
func EnsureStandaloneWorkspace(absScript string, deps []scanner.DepDirective, imports []string) (string, error) {
	wsHash := shortHash("ws|" + absScript)
	wsDir := filepath.Join(CacheRoot(), "workspaces", wsHash)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		return "", err
	}

	goVer := goMajorMinor()
	var modBuf strings.Builder
	modBuf.WriteString("module werkscript\n\n")
	modBuf.WriteString("go " + goVer + "\n")
	if len(deps) > 0 {
		modBuf.WriteString("\nrequire (\n")
		for _, d := range deps {
			modBuf.WriteString(fmt.Sprintf("\t%s %s\n", d.Module, d.Version))
		}
		modBuf.WriteString(")\n")
	}
	desiredMod := modBuf.String()

	goModPath := filepath.Join(wsDir, "go.mod")
	existingMod, _ := os.ReadFile(goModPath)
	if len(existingMod) == 0 || (len(deps) > 0 && !strings.Contains(string(existingMod), deps[0].Module)) {
		if err := os.WriteFile(goModPath, []byte(desiredMod), 0o644); err != nil {
			return "", err
		}
	}

	return wsDir, nil
}

// FindGoModRoot walks upward from startDir looking for `go.mod`. Returns "" if none exists.
func FindGoModRoot(startDir string) string {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return ""
	}
	if st, err := os.Stat(abs); err == nil && !st.IsDir() {
		abs = filepath.Dir(abs)
	}
	for cur := abs; ; {
		if st, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil && !st.IsDir() {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

type preparedScript struct {
	absScript  string
	loweredGo  string
	imports    []string
	deps       []scanner.DepDirective
	valuesHash string
	goModRoot  string
}

func prepareScript(scriptPath string, configFiles []string) (*preparedScript, error) {
	absScript, err := filepath.Abs(scriptPath)
	if err != nil {
		return nil, err
	}
	absScript = filepath.Clean(absScript)

	src, err := os.ReadFile(absScript)
	if err != nil {
		return nil, err
	}

	scanRes := scanner.Scan(absScript, src)
	if scanRes.Diags.HasErrors() {
		return nil, scanRes.Diags
	}

	var compiledCfg *pklschema.CompiledConfig
	valuesHash := "no-config"
	if scanRes.Config != nil {
		compiledCfg, err = pklschema.EvaluateAndGenerate(pklschema.EvalOptions{
			ScriptPath:      absScript,
			Scan:            scanRes,
			ExplicitConfigs: configFiles,
		})
		if err != nil {
			return nil, err
		}
		valuesHash = compiledCfg.ValuesHash
	} else if len(configFiles) > 0 {
		return nil, fmt.Errorf("%s has no config block to amend with -c %s", scriptPath, configFiles[0])
	}

	goModRoot := FindGoModRoot(filepath.Dir(absScript))
	workDir := goModRoot
	if workDir == "" {
		wsDir, wsErr := EnsureStandaloneWorkspace(absScript, scanRes.Deps, nil)
		if wsErr != nil {
			return nil, wsErr
		}
		workDir = wsDir
	}

	lowerRes, err := golower.Lower(golower.LowerOptions{
		Filename:           absScript,
		WorkDir:            workDir,
		Scan:               scanRes,
		Config:             compiledCfg,
		EmitLineDirectives: true,
	})
	if err != nil {
		return nil, err
	}

	return &preparedScript{
		absScript:  absScript,
		loweredGo:  lowerRes.GoSource,
		imports:    lowerRes.Imports,
		deps:       scanRes.Deps,
		valuesHash: valuesHash,
		goModRoot:  goModRoot,
	}, nil
}

func compilePreparedToBinary(prep *preparedScript, outBin string, extraBuildArgs []string, stderr io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(outBin), 0o755); err != nil {
		return err
	}

	// Case 1: Standalone single-file mode (no go.mod in parent tree)
	if prep.goModRoot == "" {
		wsDir, err := EnsureStandaloneWorkspace(prep.absScript, prep.deps, prep.imports)
		if err != nil {
			return err
		}
		mainGoPath := filepath.Join(wsDir, "main.go")
		if err := os.WriteFile(mainGoPath, []byte(prep.loweredGo), 0o644); err != nil {
			return err
		}

		if hasThirdPartyImport(prep.imports) || len(prep.deps) > 0 {
			tidyMarker := filepath.Join(wsDir, ".werk_tidy_hash")
			wantHash := shortHash(strings.Join(prep.imports, ",") + "|" + depsString(prep.deps))
			gotHash, _ := os.ReadFile(tidyMarker)
			if string(gotHash) != wantHash {
				tidyCmd := exec.Command("go", "mod", "tidy")
				tidyCmd.Dir = wsDir
				tidyCmd.Stderr = stderr
				if err := tidyCmd.Run(); err != nil {
					return fmt.Errorf("resolving standalone script Go dependencies: %w", err)
				}
				// Re-run lowering if needed now that external packages are cached in go.sum!
				if rePrep, reErr := prepareScriptInWorkDir(prep, wsDir); reErr == nil {
					_ = os.WriteFile(mainGoPath, []byte(rePrep), 0o644)
				}
				_ = os.WriteFile(tidyMarker, []byte(wantHash), 0o644)
			}
		}

		args := []string{"build"}
		args = append(args, extraBuildArgs...)
		args = append(args, "-o", outBin, ".")
		cmd := exec.Command("go", args...)
		cmd.Dir = wsDir
		var errBuf strings.Builder
		if stderr != nil {
			cmd.Stderr = io.MultiWriter(stderr, &errBuf)
		} else {
			cmd.Stderr = &errBuf
		}
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go build failed: %w\n%s", err, strings.TrimSpace(errBuf.String()))
		}
		return nil
	}

	// Case 2: Project Mode (`go.mod` exists) — use `go build -overlay` so zero temp files touch the repo!
	tmpDir, err := os.MkdirTemp("", "werk-overlay-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	stagedGo := filepath.Join(tmpDir, "lowered.go")
	if err := os.WriteFile(stagedGo, []byte(prep.loweredGo), 0o644); err != nil {
		return err
	}

	scriptDir := filepath.Dir(prep.absScript)
	var virtualGoPath string
	var pkgTarget string

	// If sibling `.go` files in scriptDir already define `func main()` (e.g. repo root `main.go` next to `Werkfile`),
	// compile inside an isolated virtual subpackage under `goModRoot`!
	if DirHasGoFuncMain(scriptDir) {
		virtPkgName := ".werk_exec_" + shortHash(prep.absScript)[:12]
		virtualGoPath = filepath.Join(prep.goModRoot, virtPkgName, "main.go")
		pkgTarget = "./" + virtPkgName
	} else {
		stem := strings.TrimSuffix(filepath.Base(prep.absScript), filepath.Ext(prep.absScript))
		virtualGoPath = filepath.Join(scriptDir, stem+"_werk.go")
		relPkg, relErr := filepath.Rel(prep.goModRoot, scriptDir)
		if relErr != nil || relPkg == "." {
			pkgTarget = "."
		} else {
			pkgTarget = "./" + filepath.ToSlash(relPkg)
		}
	}

	overlayPayload := struct {
		Replace map[string]string `json:"Replace"`
	}{
		Replace: map[string]string{
			virtualGoPath: stagedGo,
		},
	}
	overlayBytes, err := json.Marshal(overlayPayload)
	if err != nil {
		return err
	}
	overlayPath := filepath.Join(tmpDir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlayBytes, 0o644); err != nil {
		return err
	}

	args := []string{"build", "-overlay", overlayPath}
	args = append(args, extraBuildArgs...)
	args = append(args, "-o", outBin, pkgTarget)
	cmd := exec.Command("go", args...)
	cmd.Dir = prep.goModRoot
	var errBuf strings.Builder
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, &errBuf)
	} else {
		cmd.Stderr = &errBuf
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build failed: %w\n%s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

func prepareScriptInWorkDir(prep *preparedScript, wsDir string) (string, error) {
	src, err := os.ReadFile(prep.absScript)
	if err != nil {
		return "", err
	}
	scanRes := scanner.Scan(prep.absScript, src)
	var cfg *pklschema.CompiledConfig
	if scanRes.Config != nil {
		cfg, _ = pklschema.EvaluateAndGenerate(pklschema.EvalOptions{
			ScriptPath: prep.absScript,
			Scan:       scanRes,
		})
	}
	lr, err := golower.Lower(golower.LowerOptions{
		Filename:           prep.absScript,
		WorkDir:            wsDir,
		Scan:               scanRes,
		Config:             cfg,
		EmitLineDirectives: true,
	})
	if err != nil {
		return "", err
	}
	return lr.GoSource, nil
}

// Run compiles (with content-addressed binary caching) and executes a `.werk` script.
func Run(opts RunOptions) (int, error) {
	prep, err := prepareScript(opts.ScriptPath, opts.ConfigFiles)
	if err != nil {
		return 1, err
	}

	cacheKey := computeBinaryCacheKey(prep)
	binDir := filepath.Join(CacheRoot(), "bin")
	cachedBin := filepath.Join(binDir, cacheKey)

	if opts.Rebuild || !fileExists(cachedBin) {
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			return 1, err
		}
		tmpBin := fmt.Sprintf("%s.tmp.%d", cachedBin, os.Getpid())
		defer os.Remove(tmpBin)
		if err := compilePreparedToBinary(prep, tmpBin, nil, opts.Stderr); err != nil {
			return 1, err
		}
		_ = os.Chmod(tmpBin, 0o755)
		if err := os.Rename(tmpBin, cachedBin); err != nil {
			return 1, err
		}
	}

	if opts.ExecReplace && opts.WorkDir == "" {
		argv := append([]string{opts.ScriptPath}, opts.ScriptArgs...)
		if err := syscall.Exec(cachedBin, argv, os.Environ()); err != nil {
			return 1, err
		}
	}

	cmd := exec.Command(cachedBin, opts.ScriptArgs...)
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// Build compiles a `.werk` script (or single `.werk` file in a directory) into an output binary.
func Build(opts BuildOptions) error {
	target := opts.Target
	if target == "" {
		target = "."
	}
	scriptPath, err := resolveScriptTarget(target)
	if err != nil {
		return err
	}

	prep, err := prepareScript(scriptPath, opts.ConfigFiles)
	if err != nil {
		return err
	}

	outPath := opts.OutputPath
	if outPath == "" {
		base := filepath.Base(prep.absScript)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		if stem == "" {
			stem = "out"
		}
		outPath = stem
	}
	absOut, err := filepath.Abs(outPath)
	if err != nil {
		return err
	}
	if opts.OutputPath == "" && absOut == prep.absScript {
		absOut = prep.absScript + ".bin"
	}

	return compilePreparedToBinary(prep, absOut, opts.GoBuildArgs, opts.Stderr)
}

// Generate writes `<stem>_werk.go` next to each `.werk` file target.
func Generate(opts GenerateOptions) ([]string, error) {
	targets := opts.Targets
	if len(targets) == 0 {
		targets = []string{"."}
	}

	var werkFiles []string
	for _, t := range targets {
		found, err := collectWerkFiles(t)
		if err != nil {
			return nil, err
		}
		werkFiles = append(werkFiles, found...)
	}
	if len(werkFiles) == 0 {
		return nil, fmt.Errorf("no .werk files found in %v", targets)
	}

	var written []string
	for _, wf := range werkFiles {
		prep, err := prepareScript(wf, opts.ConfigFiles)
		if err != nil {
			return written, err
		}
		stem := strings.TrimSuffix(filepath.Base(prep.absScript), filepath.Ext(prep.absScript))
		outGo := filepath.Join(filepath.Dir(prep.absScript), stem+"_werk.go")
		if err := os.WriteFile(outGo, []byte(prep.loweredGo), 0o644); err != nil {
			return written, err
		}
		written = append(written, outGo)
	}
	return written, nil
}

// Test invokes `go test` across targets, overlaying any non-conflicting `.werk` files in tested packages.
func Test(opts TestOptions) (int, error) {
	targets := opts.Targets
	if len(targets) == 0 {
		targets = []string{"./..."}
	}
	args := []string{"test"}
	args = append(args, opts.GoTestArgs...)
	args = append(args, targets...)

	cmd := exec.Command("go", args...)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// --- Helpers ---

func resolveScriptTarget(target string) (string, error) {
	st, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return target, nil
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return "", err
	}
	var candidates []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".werk") {
			candidates = append(candidates, filepath.Join(target, e.Name()))
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) > 1 {
		return "", fmt.Errorf("multiple .werk files in %s (%s); specify which file to build", target, strings.Join(candidates, ", "))
	}
	for _, fallback := range []string{"Werkfile", "werkfile"} {
		cand := filepath.Join(target, fallback)
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand, nil
		}
	}
	return "", fmt.Errorf("no .werk file found in %s", target)
}

func collectWerkFiles(target string) ([]string, error) {
	if strings.HasSuffix(target, "/...") {
		base := strings.TrimSuffix(target, "/...")
		if base == "" {
			base = "."
		}
		var out []string
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != "." {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".werk") {
				out = append(out, path)
			}
			return nil
		})
		return out, err
	}
	st, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []string{target}, nil
	}
	var out []string
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".werk") {
			out = append(out, filepath.Join(target, e.Name()))
		}
	}
	return out, nil
}

// DirHasGoFuncMain reports whether any non-test `.go` file in dir already declares `func main()`.
func DirHasGoFuncMain(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		pf, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			continue
		}
		for _, d := range pf.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name != nil && fn.Name.Name == "main" {
				return true
			}
		}
	}
	return false
}

func computeBinaryCacheKey(prep *preparedScript) string {
	h := sha256.New()
	h.Write([]byte(cacheSchemaVersion))
	h.Write([]byte("|" + runtime.Version() + "|" + runtime.GOOS + "/" + runtime.GOARCH + "|"))
	h.Write([]byte(prep.loweredGo))
	h.Write([]byte("|" + prep.valuesHash + "|"))
	if prep.goModRoot != "" {
		if modBytes, err := os.ReadFile(filepath.Join(prep.goModRoot, "go.mod")); err == nil {
			h.Write(modBytes)
		}
		if sumBytes, err := os.ReadFile(filepath.Join(prep.goModRoot, "go.sum")); err == nil {
			h.Write(sumBytes)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hasThirdPartyImport(imports []string) bool {
	for _, imp := range imports {
		first := imp
		if slash := strings.IndexByte(imp, '/'); slash >= 0 {
			first = imp[:slash]
		}
		if strings.ContainsRune(first, '.') {
			return true
		}
	}
	return false
}

func depsString(deps []scanner.DepDirective) string {
	var parts []string
	for _, d := range deps {
		parts = append(parts, d.Module+"@"+d.Version)
	}
	return strings.Join(parts, ",")
}

func goMajorMinor() string {
	v := strings.TrimPrefix(runtime.Version(), "go")
	parts := strings.Split(v, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return "1.24"
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
