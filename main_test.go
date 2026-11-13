package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ABM2205/werklang/internal/pklschema"
)

func TestEndToEndPklExternalReaderAndShebangExecution(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpDir, "cache"))

	// 1. Build the real `werk` binary into tmpBinDir
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	werkBin := filepath.Join(binDir, "werk")
	buildCmd := exec.Command("go", "build", "-o", werkBin, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("building werk binary failed: %v\n%s", err, string(out))
	}

	// Prepend `binDir` (and `~/.local/bin` for `pkl`) onto PATH
	pklBin, err := pklschema.FindPklBinary()
	if err != nil {
		t.Skip("pkl binary not installed")
	}
	newPath := binDir + string(os.PathListSeparator) + filepath.Dir(pklBin) + string(os.PathListSeparator) + os.Getenv("PATH")

	// 2. Scaffold a clean workspace with `werk init` -> creates `PklProject`!
	projDir := filepath.Join(tmpDir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initCmd := exec.Command(werkBin, "init", projDir)
	initCmd.Env = append(os.Environ(), "PATH="+newPath)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("werk init failed: %v\n%s", err, string(out))
	}

	// 3. Create an executable shebang `.werk` script (`deploy.werk`)
	deployWerk := `#!/usr/bin/env werk
import (
	"fmt"
	"os"
)

config app {
	/// Target cluster name
	cluster: String = "dev-local"
	replicas: UInt16(this >= 1) = 2
}

fmt.Printf("cluster=%s replicas=%d args=%v\n", app.Cluster, app.Replicas, os.Args[1:])
`
	deployPath := filepath.Join(projDir, "deploy.werk")
	if err := os.WriteFile(deployPath, []byte(deployWerk), 0o755); err != nil {
		t.Fatal(err)
	}

	// 4. Create `prod.pkl` amending `werk:/deploy.werk` and evaluate it directly with Apple's `pkl eval` CLI!
	// This proves `pkl` spawns `werk pkl-reader` via `PklProject` and validates constraints & defaults!
	prodPkl := `amends "werk:/deploy.werk"

cluster = "us-east-prod"
replicas = 5
`
	prodPath := filepath.Join(projDir, "prod.pkl")
	if err := os.WriteFile(prodPath, []byte(prodPkl), 0o644); err != nil {
		t.Fatal(err)
	}

	pklEvalCmd := exec.Command(pklBin, "eval", "-f", "json", prodPath)
	pklEvalCmd.Dir = projDir
	pklEvalCmd.Env = append(os.Environ(), "PATH="+newPath)
	pklOut, err := pklEvalCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("standalone `pkl eval prod.pkl` via PklProject externalModuleReaders failed: %v\n%s", err, string(pklOut))
	}
	if !strings.Contains(string(pklOut), `"cluster": "us-east-prod"`) || !strings.Contains(string(pklOut), `"replicas": 5`) {
		t.Fatalf("unexpected `pkl eval` output:\n%s", string(pklOut))
	}

	// 5. Also verify Pkl constraint errors in `bad.pkl` point to exact line 10 of `deploy.werk`!
	badPklPath := filepath.Join(projDir, "bad.pkl")
	if err := os.WriteFile(badPklPath, []byte("amends \"werk:/deploy.werk\"\nreplicas = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	badCmd := exec.Command(pklBin, "eval", badPklPath)
	badCmd.Dir = projDir
	badCmd.Env = append(os.Environ(), "PATH="+newPath)
	badOut, badErr := badCmd.CombinedOutput()
	if badErr == nil {
		t.Fatalf("expected constraint violation when replicas = 0")
	}
	if !strings.Contains(string(badOut), "10 | replicas:") {
		t.Fatalf("expected Pkl error stack trace to point to line 10 of deploy.werk, got:\n%s", string(badOut))
	}

	// 6. Execute `./deploy.werk --dry-run fast` directly via OS kernel `#!` shebang!
	shebangCmd := exec.Command(deployPath, "--dry-run", "fast")
	shebangCmd.Dir = projDir
	shebangCmd.Env = append(os.Environ(), "PATH="+newPath, "XDG_CACHE_HOME="+filepath.Join(tmpDir, "cache"))
	shebangOut, err := shebangCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("direct shebang ./deploy.werk execution failed: %v\n%s", err, string(shebangOut))
	}
	wantShebang := "cluster=dev-local replicas=2 args=[--dry-run fast]"
	if strings.TrimSpace(string(shebangOut)) != wantShebang {
		t.Fatalf("shebang output got %q, want %q", strings.TrimSpace(string(shebangOut)), wantShebang)
	}
}
