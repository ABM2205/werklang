package runner_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ABM2205/werklang/internal/runner"
)

func TestRunWerkModulesAndDerivedFields(t *testing.T) {
	dir := t.TempDir()

	serviceWerk := `name string
image string
replicas int = 1
debug bool = false
full_tag string = "\(image):\(if debug then "dev" else "latest")"
`
	if err := os.WriteFile(filepath.Join(dir, "service.werk"), []byte(serviceWerk), 0o644); err != nil {
		t.Fatal(err)
	}

	mainWerk := `import "./service.werk" as Service

api = Service {
    name = "gateway"
    image = "ghcr.io/acme/gateway"
}

canary = api {
    replicas = 2
    debug = true
}

print("\(api.name)=\(api.full_tag) (\(api.replicas))")
print("\(canary.name)=\(canary.full_tag) (\(canary.replicas))")
`
	mainPath := filepath.Join(dir, "run.werk")
	if err := os.WriteFile(mainPath, []byte(mainWerk), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code, err := runner.Run(runner.RunOptions{
		File:    mainPath,
		WorkDir: dir,
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if err != nil || code != 0 {
		t.Fatalf("run failed (code=%d, err=%v)\nstderr:\n%s", code, err, stderr.String())
	}

	got := strings.TrimSpace(stdout.String())
	want := "gateway=ghcr.io/acme/gateway:latest (1)\ngateway=ghcr.io/acme/gateway:dev (2)"
	if got != want {
		t.Fatalf("unexpected output:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestMissingRequiredModuleFieldErrorsAtCheckTime(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "service.werk"), []byte("name string\nport int = 80\n"), 0o644)
	badPath := filepath.Join(dir, "bad.werk")
	_ = os.WriteFile(badPath, []byte("import \"./service.werk\" as Service\ns = Service { port = 8080 }\n"), 0o644)

	_, _, err := runner.Transpile(badPath, dir)
	if err == nil || !strings.Contains(err.Error(), `missing required field "name"`) {
		t.Fatalf("expected missing required field error, got: %v", err)
	}
}

func TestAmendsWerkModule(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "base.werk"), []byte(`env_name string = "dev"
port int = 8080
banner string = "\(env_name):\(port)"
`), 0o644)

	prodPath := filepath.Join(dir, "prod.werk")
	_ = os.WriteFile(prodPath, []byte(`amends "./base.werk"

env_name = "prod"
port = 443
print(banner)
`), 0o644)

	var stdout, stderr bytes.Buffer
	code, err := runner.Run(runner.RunOptions{
		File:    prodPath,
		WorkDir: dir,
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if err != nil || code != 0 {
		t.Fatalf("amends run failed (code=%d, err=%v): %s", code, err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "prod:443" {
		t.Fatalf("expected prod:443, got %q", stdout.String())
	}
}

func TestImportApplePklAndGoStdlib(t *testing.T) {
	dir := t.TempDir()

	pklContent := `app_name = "billing"
port = 8080
ratio = 1.5
enabled = true
tags = new Listing {
  "v1"
  "pci"
}
timeout = 2.s
db {
  host = "db.internal"
  port = 5432
}
`
	if err := os.WriteFile(filepath.Join(dir, "app.pkl"), []byte(pklContent), 0o644); err != nil {
		t.Fatal(err)
	}

	werkSrc := `import "./app.pkl" as app
import "go:os"
import "go:strings"
import "go:path/filepath"

staging = app {
    port = 9090
}

target verify {
    desc = "Test Pkl + Go stdlib interop"
    ver_file = filepath.Join("ver.txt")
    os.WriteFile(ver_file, "  v2.4.0 \n", 420)
    ver = strings.TrimSpace(os.ReadFile(ver_file))
    print("\(app.app_name)|\(app.db.host):\(app.port)|staging:\(staging.port)|tags:\(app.tags[0]),\(app.tags[1])|ver:\(ver)")
}
`
	wf := filepath.Join(dir, "Werkfile")
	if err := os.WriteFile(wf, []byte(werkSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code, err := runner.Run(runner.RunOptions{
		File:    wf,
		WorkDir: dir,
		Targets: []string{"verify"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if err != nil || code != 0 {
		t.Fatalf("pkl+go run failed (code=%d, err=%v)\nstderr:\n%s", code, err, stderr.String())
	}

	got := strings.TrimSpace(stdout.String())
	want := "billing|db.internal:8080|staging:9090|tags:v1,pci|ver:v2.4.0"
	if got != want {
		t.Fatalf("unexpected output:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMageImportDAGIncrementalAndOverrides(t *testing.T) {
	dir := t.TempDir()

	// Create a local Go module with a shared Mage target package!
	gomod := "module example.com/testrepo\n\ngo 1.23\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	dockerDir := filepath.Join(dir, "ci", "docker")
	if err := os.MkdirAll(dockerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dockerGo := `package docker

import "fmt"

// Login authenticates with the container registry.
func Login() error {
	fmt.Println("mage:docker:login")
	return nil
}

// Tag tags an image with version.
func Tag(version string) error {
	fmt.Printf("mage:docker:tag:%s\n", version)
	return nil
}
`
	if err := os.WriteFile(filepath.Join(dockerDir, "docker.go"), []byte(dockerGo), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	wfSrc := `import "mage:example.com/testrepo/ci/docker" as docker

release bool = false

config cfg {
    mode string = if release then "prod" else "dev"
}

target prep {
    desc = "Prepare build"
    print("local:prep")
}

target build {
    desc = "Incremental build target"
    deps = ["prep", "docker:login"]
    inputs = ["input.txt"]
    outputs = ["output.txt"]

    print("building:\(cfg.mode)")
    docker.Tag(cfg.mode)
    sh("cp input.txt output.txt")
}
`
	wf := filepath.Join(dir, "Werkfile")
	if err := os.WriteFile(wf, []byte(wfSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Verify `List: true` shows both local targets and imported docker targets
	var listOut, listErr bytes.Buffer
	code, err := runner.Run(runner.RunOptions{
		File:    wf,
		WorkDir: dir,
		List:    true,
		Stdout:  &listOut,
		Stderr:  &listErr,
	})
	if err != nil || code != 0 {
		t.Fatalf("list failed (code=%d, err=%v): %s", code, err, listErr.String())
	}
	listed := listOut.String()
	for _, expected := range []string{"build", "prep", "docker:login", "docker:tag"} {
		if !strings.Contains(strings.ToLower(listed), expected) {
			t.Fatalf("expected `werk -l` output to contain %q, got:\n%s", expected, listed)
		}
	}

	// 2. First run with -D release=true should run prep, docker:login, build, and create output.txt
	var run1Out, run1Err bytes.Buffer
	code, err = runner.Run(runner.RunOptions{
		File:      wf,
		WorkDir:   dir,
		Targets:   []string{"build"},
		Overrides: map[string]string{"release": "true"},
		Stdout:    &run1Out,
		Stderr:    &run1Err,
	})
	if err != nil || code != 0 {
		t.Fatalf("run 1 failed (code=%d, err=%v): %s", code, err, run1Err.String())
	}
	out1 := strings.TrimSpace(run1Out.String())
	want1 := "local:prep\nmage:docker:login\nbuilding:prod\nmage:docker:tag:prod"
	if out1 != want1 {
		t.Fatalf("run 1 output mismatch:\ngot:\n%s\nwant:\n%s", out1, want1)
	}

	// 3. Second run without touching input.txt should skip `building:...` because output.txt is up to date!
	var run2Out, run2Err bytes.Buffer
	code, err = runner.Run(runner.RunOptions{
		File:    wf,
		WorkDir: dir,
		Targets: []string{"build"},
		Stdout:  &run2Out,
		Stderr:  &run2Err,
	})
	if err != nil || code != 0 {
		t.Fatalf("run 2 failed: %v %s", err, run2Err.String())
	}
	if strings.Contains(run2Out.String(), "building:") {
		t.Fatalf("expected incremental target to skip rebuild when output.txt is up to date, got:\n%s", run2Out.String())
	}

	// 4. Touch input.txt forward in time -> should rebuild!
	future := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(filepath.Join(dir, "input.txt"), future, future); err != nil {
		t.Fatal(err)
	}
	var run3Out, run3Err bytes.Buffer
	code, err = runner.Run(runner.RunOptions{
		File:    wf,
		WorkDir: dir,
		Targets: []string{"build"},
		Stdout:  &run3Out,
		Stderr:  &run3Err,
	})
	if err != nil || code != 0 {
		t.Fatalf("run 3 failed: %v %s", err, run3Err.String())
	}
	if !strings.Contains(run3Out.String(), "building:dev") {
		t.Fatalf("expected rebuild after input.txt mtime update, got:\n%s", run3Out.String())
	}
}
