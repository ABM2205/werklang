package parser_test

import (
	"testing"

	"github.com/ABM2205/werklang/internal/format"
	"github.com/ABM2205/werklang/internal/parser"
)

func TestParseAndFormatPolyglotWerkfile(t *testing.T) {
	src := `#!/usr/bin/env werk
amends "./base.werk"

import "./deploy/service.werk" as Service
import "./config/app.pkl" as app
import "mage:github.com/acme/ci/docker" as docker
import "go:path/filepath"

release bool = false
tags []string = ["cli", "go"]

config build_cfg {
    name string = "werk"
    port int = 8080
    tag string = "\(name):\(if release then "latest" else "dev")"
}

api = Service {
    name = "gateway"
    ports = [80, 443]
}

target test {
    desc = "Run unit tests"
    for pkg in ["lexer", "parser"] {
        sh("go test ./internal/\(pkg)/...")
    }
}

target build {
    desc = "Build binary"
    deps = ["test", "docker:login"]
    if !exists("bin") {
        mkdir("bin")
    } else {
        print("ready")
    }
    exec("go", ["build", "-o", filepath.Join("bin", build_cfg.name), "."])
}
`
	prog, diags := parser.ParseFile("Werkfile", src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if prog.Amends == nil || prog.Amends.Path != "./base.werk" {
		t.Fatalf("expected amends ./base.werk, got %+v", prog.Amends)
	}
	if len(prog.Imports) != 4 {
		t.Fatalf("expected 4 imports, got %d", len(prog.Imports))
	}
	if len(prog.Configs) != 1 || prog.Configs[0].Name != "build_cfg" {
		t.Fatalf("unexpected configs: %+v", prog.Configs)
	}
	if len(prog.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(prog.Targets))
	}

	formatted, err := format.Source("Werkfile", src)
	if err != nil {
		t.Fatalf("format error: %v", err)
	}
	// Formatting must be idempotent
	formatted2, err := format.Source("Werkfile", formatted)
	if err != nil {
		t.Fatalf("second format error: %v", err)
	}
	if formatted != formatted2 {
		t.Fatalf("formatter not idempotent:\n--- pass 1 ---\n%s\n--- pass 2 ---\n%s", formatted, formatted2)
	}
}

func TestParserErrorRecoveryForLSP(t *testing.T) {
	// Incomplete selector (`filepath.`) in the middle of file should recover and still parse subsequent target!
	src := `import "go:path/filepath"

x = filepath.

target build {
    sh("echo ok")
}
`
	prog, diags := parser.ParseFile("Werkfile", src)
	if len(diags) == 0 {
		t.Fatalf("expected diagnostic for incomplete selector")
	}
	if len(prog.Targets) != 1 || prog.Targets[0].Name != "build" {
		t.Fatalf("expected parser to recover and parse 'build' target, got %+v", prog.Targets)
	}
}
