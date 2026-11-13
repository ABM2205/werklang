package pklschema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ABM2205/werklang/internal/scanner"
)

func TestUpperFirst(t *testing.T) {
	cases := map[string]string{
		"httpPort":  "HttpPort",
		"apiUrl":    "ApiUrl",
		"id":        "Id",
		"max_conns": "MaxConns",
		"api_url":   "ApiUrl",
		"x-env":     "XEnv",
	}
	for in, want := range cases {
		if got := UpperFirst(in); got != want {
			t.Errorf("UpperFirst(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEvaluateTemplateAndExternalAmends(t *testing.T) {
	dir := t.TempDir()
	werkSrc := `#!/usr/bin/env werk
import "fmt"

config app {
    /// Server host
    host: String = "127.0.0.1"
    httpPort: UInt16 = 8080
    apiUrl: String
    timeout: Duration = 5.s
    hidden secret = "internal"
    db {
        host = "localhost"
        port = 5432
    }
    tags {
        "web"
        "api"
    }
}

fmt.Println(app.Host)
`
	werkPath := filepath.Join(dir, "server.werk")
	if err := os.WriteFile(werkPath, []byte(werkSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	prodPkl := `amends "werk:/server.werk"

host = "0.0.0.0"
httpPort = 443
apiUrl = "https://api.example.com"

db {
    host = "db.prod.internal"
}
`
	prodPath := filepath.Join(dir, "prod.pkl")
	if err := os.WriteFile(prodPath, []byte(prodPkl), 0o644); err != nil {
		t.Fatal(err)
	}

	scanned := scanner.Scan(werkPath, []byte(werkSrc))
	if scanned.Diags.HasErrors() {
		t.Fatalf("scan errors: %v", scanned.Diags)
	}

	// 1. Test SchemaOnly mode WITHOUT prod.pkl (even though `apiUrl: String` has no default!)
	schemaOnlyCfg, err := EvaluateAndGenerate(EvalOptions{
		ScriptPath: werkPath,
		Scan:       scanned,
		SchemaOnly: true,
	})
	if err != nil {
		t.Fatalf("SchemaOnly EvaluateAndGenerate failed: %v", err)
	}
	if !strings.Contains(schemaOnlyCfg.GoDecls, "HttpPort uint16") || !strings.Contains(schemaOnlyCfg.GoDecls, "ApiUrl string") {
		t.Fatalf("expected HttpPort and ApiUrl in SchemaOnly GoDecls:\n%s", schemaOnlyCfg.GoDecls)
	}

	// 2. Test full compile-time evaluation WITH `-c prod.pkl`
	compiled, err := EvaluateAndGenerate(EvalOptions{
		ScriptPath:      werkPath,
		Scan:            scanned,
		ExplicitConfigs: []string{prodPath},
	})
	if err != nil {
		t.Fatalf("EvaluateAndGenerate with prod.pkl failed: %v", err)
	}

	checks := []string{
		"type appDbConfig struct",
		"type appConfig struct",
		"HttpPort uint16",
		"ApiUrl string",
		"Timeout _werk_time.Duration",
		"Tags []string",
		`Host: "0.0.0.0"`,
		"HttpPort: uint16(443)",
		`ApiUrl: "https://api.example.com"`,
		`Host: "db.prod.internal"`,
		"Port: 5432",
		`[]string{"web", "api"}`,
	}
	for _, want := range checks {
		if !strings.Contains(compiled.GoDecls, want) {
			t.Errorf("missing %q in generated GoDecls:\n%s", want, compiled.GoDecls)
		}
	}
	if strings.Contains(compiled.GoDecls, "Secret") {
		t.Errorf("hidden property 'secret' should be omitted from GoDecls:\n%s", compiled.GoDecls)
	}
	if !compiled.NeedsTimeImport {
		t.Errorf("expected NeedsTimeImport = true for Duration field")
	}
	if compiled.RootFields["Host"] == nil || compiled.RootFields["Host"].Doc != "Server host" {
		t.Errorf("expected doc comment on Host field, got %+v", compiled.RootFields["Host"])
	}
}
