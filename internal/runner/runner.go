package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ABM2205/werklang/internal/codegen"
	"github.com/ABM2205/werklang/internal/diag"
	"github.com/ABM2205/werklang/internal/interop/goinspect"
	pklinterop "github.com/ABM2205/werklang/internal/interop/pkl"
	"github.com/ABM2205/werklang/internal/types"
	"github.com/magefile/mage/mage"
)

type RunOptions struct {
	File       string            // Path to Werkfile or .werk script
	WorkDir    string            // Working directory where commands/targets execute
	Targets    []string          // Target names to run (empty -> Default)
	Overrides  map[string]string // -D key=value top-level property overrides
	List       bool              // -l / --list
	Verbose    bool              // -v / --verbose
	Debug      bool              // --debug
	Force      bool              // -f / --force rebuild
	CompileOut string            // --compile <output-binary>
	Stdout     io.Writer
	Stderr     io.Writer
	Stdin      io.Reader
}

func DefaultImportHooks() types.ImportHooks {
	return types.ImportHooks{
		InspectPkl:  pklinterop.InspectModule,
		InspectGo:   goinspect.InspectGoPackage,
		InspectMage: goinspect.InspectMagePackage,
	}
}

// Check loads and typechecks a .werk file with full Go, Mage, and Pkl interop enabled.
func Check(file, workDir string, overlay map[string]string) *types.CheckedProgram {
	return types.CheckFile(file, types.CheckOptions{
		WorkDir: workDir,
		Overlay: overlay,
		Hooks:   DefaultImportHooks(),
	})
}

// Transpile checks a .werk file and returns the generated Go Magefile source code.
func Transpile(file, workDir string) (string, *types.CheckedProgram, error) {
	cp := Check(file, workDir, nil)
	var errs []string
	for _, d := range cp.Diagnostics {
		if d.Severity == diag.SeverityError {
			errs = append(errs, d.String())
		}
	}
	if len(errs) > 0 {
		return "", cp, fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	pklBin, _ := pklinterop.FindPklBinary()
	goCode, err := codegen.Generate(cp, codegen.Options{
		DefaultPklBin: pklBin,
	})
	if err != nil {
		return "", cp, err
	}
	return goCode, cp, nil
}

// Run transpiles the .werk file into a cached Mage workspace and executes it via mage.Invoke.
func Run(opts RunOptions) (int, error) {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}

	absFile, err := filepath.Abs(opts.File)
	if err != nil {
		return 1, err
	}
	workDir := opts.WorkDir
	if workDir == "" {
		workDir, err = os.Getwd()
		if err != nil {
			workDir = filepath.Dir(absFile)
		}
	}

	goCode, cp, err := Transpile(absFile, workDir)
	if err != nil {
		return 1, err
	}

	wsDir, cacheDir, err := prepareMageWorkspace(absFile, workDir, goCode, cp)
	if err != nil {
		return 1, err
	}

	// Apply -D key=val overrides via environment variables for this process invocation
	var prevEnv []struct {
		key string
		val string
		had bool
	}
	for k, v := range opts.Overrides {
		envKey := "WERK_OVERRIDE_" + k
		old, had := os.LookupEnv(envKey)
		prevEnv = append(prevEnv, struct {
			key string
			val string
			had bool
		}{envKey, old, had})
		_ = os.Setenv(envKey, v)
	}
	defer func() {
		for _, p := range prevEnv {
			if p.had {
				_ = os.Setenv(p.key, p.val)
			} else {
				_ = os.Unsetenv(p.key)
			}
		}
	}()

	absWorkDir, err := filepath.Abs(workDir)
	if err == nil {
		workDir = absWorkDir
	}

	compileOut := opts.CompileOut
	if compileOut != "" && !filepath.IsAbs(compileOut) {
		compileOut = filepath.Join(workDir, compileOut)
	}

	// Mage's //mage:import parser invokes `go list` in the process's cwd rather than inv.Dir,
	// while the compiled target binary executes in inv.WorkDir. Temporarily chdir to wsDir.
	origCwd, cwdErr := os.Getwd()
	if cwdErr == nil {
		_ = os.Chdir(wsDir)
		defer func() { _ = os.Chdir(origCwd) }()
	}

	inv := mage.Invocation{
		Dir:        wsDir,
		WorkDir:    workDir,
		CacheDir:   cacheDir,
		List:       opts.List,
		Verbose:    opts.Verbose,
		Debug:      opts.Debug,
		Force:      opts.Force,
		CompileOut: compileOut,
		HashFast:   true,
		Stdout:     opts.Stdout,
		Stderr:     opts.Stderr,
		Stdin:      opts.Stdin,
		Args:       opts.Targets,
	}

	code := mage.Invoke(inv)
	return code, nil
}

func prepareMageWorkspace(absFile, workDir, goCode string, cp *types.CheckedProgram) (wsDir string, binCacheDir string, err error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		cacheRoot = os.TempDir()
	}
	werkCache := filepath.Join(cacheRoot, "werk")
	binCacheDir = filepath.Join(werkCache, "bin")
	if err := os.MkdirAll(binCacheDir, 0o755); err != nil {
		return "", "", err
	}

	localModName, localModDir := findEnclosingGoMod(workDir)

	var depSpecs []string
	depSpecs = append(depSpecs, "mage:v1.17.2")
	if len(cp.PklModules) > 0 {
		depSpecs = append(depSpecs, "pkl-go:v0.14.0")
	}
	for _, gp := range cp.GoImports {
		if strings.Contains(gp.ImportPath, ".") {
			depSpecs = append(depSpecs, gp.ImportPath+"@"+gp.Version)
		}
	}
	for _, mp := range cp.MageImports {
		if strings.Contains(mp.ImportPath, ".") {
			depSpecs = append(depSpecs, mp.ImportPath+"@"+mp.Version)
		}
	}
	if localModName != "" {
		depSpecs = append(depSpecs, "local:"+localModName+"=>"+localModDir)
	}
	sort.Strings(depSpecs)

	h := sha256.New()
	h.Write([]byte(absFile))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(depSpecs, "|")))
	wsHash := hex.EncodeToString(h.Sum(nil))[:16]

	wsDir = filepath.Join(werkCache, "workspaces", wsHash)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		return "", "", err
	}

	mageFilePath := filepath.Join(wsDir, "magefile_gen.go")
	if err := os.WriteFile(mageFilePath, []byte(goCode), 0o644); err != nil {
		return "", "", err
	}

	gomodPath := filepath.Join(wsDir, "go.mod")
	depsMarkerPath := filepath.Join(wsDir, ".deps_stamp")
	stamp := strings.Join(depSpecs, "\n")
	existingStamp, _ := os.ReadFile(depsMarkerPath)

	if _, statErr := os.Stat(gomodPath); os.IsNotExist(statErr) || string(existingStamp) != stamp {
		if err := initWorkspaceGoMod(wsDir, cp, localModName, localModDir); err != nil {
			return "", "", err
		}
		_ = os.WriteFile(depsMarkerPath, []byte(stamp), 0o644)
	}

	return wsDir, binCacheDir, nil
}

func initWorkspaceGoMod(wsDir string, cp *types.CheckedProgram, localModName, localModDir string) error {
	var b strings.Builder
	b.WriteString("module werkbuild\n\n")
	b.WriteString("go 1.23\n\n")
	b.WriteString("require (\n")
	b.WriteString("\tgithub.com/magefile/mage v1.17.2\n")
	if len(cp.PklModules) > 0 {
		b.WriteString("\tgithub.com/apple/pkl-go v0.14.0\n")
	}
	if localModName != "" && localModDir != "" {
		b.WriteString(fmt.Sprintf("\t%s v0.0.0\n", localModName))
	}
	b.WriteString(")\n")
	if localModName != "" && localModDir != "" {
		b.WriteString(fmt.Sprintf("\nreplace %s => %s\n", localModName, localModDir))
	}

	if err := os.WriteFile(filepath.Join(wsDir, "go.mod"), []byte(b.String()), 0o644); err != nil {
		return err
	}

	// Fetch any explicit third-party @version requirements
	for _, gp := range cp.GoImports {
		if strings.Contains(gp.ImportPath, ".") && (localModName == "" || !strings.HasPrefix(gp.ImportPath, localModName)) {
			ver := gp.Version
			if ver == "" {
				ver = "latest"
			}
			if err := runGoCmd(wsDir, "get", gp.ImportPath+"@"+ver); err != nil {
				return err
			}
		}
	}
	for _, mp := range cp.MageImports {
		if strings.Contains(mp.ImportPath, ".") && (localModName == "" || !strings.HasPrefix(mp.ImportPath, localModName)) {
			ver := mp.Version
			if ver == "" {
				ver = "latest"
			}
			if err := runGoCmd(wsDir, "get", mp.ImportPath+"@"+ver); err != nil {
				return err
			}
		}
	}

	// Run go mod tidy with -e / mage build tag awareness via a tiny stub so `//go:build mage` imports are resolved in go.sum!
	// Wait: `go mod tidy` ignores `//go:build mage` files UNLESS a non-tagged file or build constraint is considered?
	// Actually `go mod tidy` evaluates ALL build tags across all platforms and custom tags? NO! `go mod tidy` ignores custom build tags like `//go:build mage`!
	// Writing a temporary `deps_pin_gen.go` without build tags that blank-imports the exact external packages ensures `go mod tidy` populates `go.sum` 100%!
	var pin strings.Builder
	pin.WriteString("package main\nimport (\n")
	pin.WriteString("\t_ \"github.com/magefile/mage/mg\"\n")
	pin.WriteString("\t_ \"github.com/magefile/mage/sh\"\n")
	pin.WriteString("\t_ \"github.com/magefile/mage/target\"\n")
	if len(cp.PklModules) > 0 {
		pin.WriteString("\t_ \"github.com/apple/pkl-go/pkl\"\n")
	}
	seen := make(map[string]bool)
	for _, gp := range cp.GoImports {
		if !seen[gp.ImportPath] {
			seen[gp.ImportPath] = true
			pin.WriteString(fmt.Sprintf("\t_ %q\n", gp.ImportPath))
		}
	}
	for _, mp := range cp.MageImports {
		if !seen[mp.ImportPath] {
			seen[mp.ImportPath] = true
			pin.WriteString(fmt.Sprintf("\t_ %q\n", mp.ImportPath))
		}
	}
	pin.WriteString(")\n")

	pinFile := filepath.Join(wsDir, "deps_pin_gen.go")
	if err := os.WriteFile(pinFile, []byte(pin.String()), 0o644); err != nil {
		return err
	}
	tidyErr := runGoCmd(wsDir, "mod", "tidy")
	_ = os.Remove(pinFile)
	return tidyErr
}

func runGoCmd(dir string, args ...string) error {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func findEnclosingGoMod(startDir string) (modName, modDir string) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", ""
	}
	for {
		gm := filepath.Join(dir, "go.mod")
		if raw, err := os.ReadFile(gm); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "module ") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						return fields[1], dir
					}
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", ""
}
