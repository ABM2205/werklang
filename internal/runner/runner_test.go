package runner

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandaloneSingleFileScriptAndAmendsAndCache(t *testing.T) {
	// Place in a fresh temp dir outside any `go.mod` to exercise Standalone Single-File Mode!
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))

	scriptSrc := `#!/usr/bin/env werk
import (
	"fmt"
	"os"
)

config app {
	greeting: String = "hello"
	httpPort: UInt16 = 8080
	db {
		host = "localhost"
		port = 5432
	}
}

counter := 0

func nextBanner(arg string) string {
	counter++
	return fmt.Sprintf("#%d %s %s on :%d (%s:%d)",
		counter, app.Greeting, arg, app.HttpPort, app.Db.Host, app.Db.Port)
}

target := "world"
if len(os.Args) > 1 {
	target = os.Args[1]
}
fmt.Println(nextBanner(target))
fmt.Println(nextBanner("again"))
`
	scriptPath := filepath.Join(dir, "hello.werk")
	if err := os.WriteFile(scriptPath, []byte(scriptSrc), 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. Run with template defaults
	var out1, err1 bytes.Buffer
	code, err := Run(RunOptions{
		ScriptPath: scriptPath,
		ScriptArgs: []string{"werk"},
		Stdout:     &out1,
		Stderr:     &err1,
	})
	if err != nil || code != 0 {
		t.Fatalf("Run defaults failed (code=%d, err=%v): %s", code, err, err1.String())
	}
	want1 := "#1 hello werk on :8080 (localhost:5432)\n#2 hello again on :8080 (localhost:5432)"
	if strings.TrimSpace(out1.String()) != want1 {
		t.Fatalf("got:\n%s\nwant:\n%s", out1.String(), want1)
	}

	// 2. Run with external amending .pkl file (`-c prod.pkl`)
	prodPkl := `amends "werk:/hello.werk"

greeting = "welcome"
httpPort = 443
db {
	host = "prod-db.internal"
}
`
	prodPath := filepath.Join(dir, "prod.pkl")
	if err := os.WriteFile(prodPath, []byte(prodPkl), 0o644); err != nil {
		t.Fatal(err)
	}

	var out2, err2 bytes.Buffer
	code, err = Run(RunOptions{
		ScriptPath:  scriptPath,
		ConfigFiles: []string{prodPath},
		ScriptArgs:  []string{"ops"},
		Stdout:      &out2,
		Stderr:      &err2,
	})
	if err != nil || code != 0 {
		t.Fatalf("Run with prod.pkl failed (code=%d, err=%v): %s", code, err, err2.String())
	}
	want2 := "#1 welcome ops on :443 (prod-db.internal:5432)\n#2 welcome again on :443 (prod-db.internal:5432)"
	if strings.TrimSpace(out2.String()) != want2 {
		t.Fatalf("got:\n%s\nwant:\n%s", out2.String(), want2)
	}

	// 3. Build standalone binary with `-c prod.pkl` and verify zero Pkl dependency at runtime
	binPath := filepath.Join(dir, "hello-bin")
	if err := Build(BuildOptions{
		Target:      scriptPath,
		OutputPath:  binPath,
		ConfigFiles: []string{prodPath},
		Stderr:      &err2,
	}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	cmd := exec.Command(binPath, "binary")
	cmd.Env = []string{"PATH=/nonexistent"} // Prove zero runtime dependency on `pkl` or `go`!
	binOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("compiled binary failed: %v\n%s", err, string(binOut))
	}
	want3 := "#1 welcome binary on :443 (prod-db.internal:5432)\n#2 welcome again on :443 (prod-db.internal:5432)"
	if strings.TrimSpace(string(binOut)) != want3 {
		t.Fatalf("binary output got:\n%s\nwant:\n%s", string(binOut), want3)
	}
}

func TestConventionFallbackPkl(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))

	scriptPath := filepath.Join(dir, "svc.werk")
	if err := os.WriteFile(scriptPath, []byte(`import "fmt"
config {
	port: UInt16
}
fmt.Println(config.Port)
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Matching `<stem>.pkl` (`svc.pkl`) discovered automatically without `-c`!
	if err := os.WriteFile(filepath.Join(dir, "svc.pkl"), []byte(`amends "werk:/svc.werk"
port = 9090
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	code, err := Run(RunOptions{
		ScriptPath: scriptPath,
		Stdout:     &out,
		Stderr:     &errBuf,
	})
	if err != nil || code != 0 {
		t.Fatalf("convention fallback run failed: %v\n%s", err, errBuf.String())
	}
	if strings.TrimSpace(out.String()) != "9090" {
		t.Fatalf("got %q, want 9090", out.String())
	}
}
