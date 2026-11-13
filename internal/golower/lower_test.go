package golower

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/ABM2205/werklang/internal/scanner"
)

func TestLowerPythonStyleGlobalsAndExecutionOrder(t *testing.T) {
	src := `#!/usr/bin/env werk
import (
	"fmt"
	"net/http"
)

count := 0
client := http.DefaultClient

func bump(label string) string {
	count++
	_ = client.Timeout
	return fmt.Sprintf("%s:%d", label, count)
}

msg := bump("first")
fmt.Println(msg, bump("second"))
`
	scanRes := scanner.Scan("script.werk", []byte(src))
	res, err := Lower(LowerOptions{
		Filename:           "script.werk",
		Scan:               scanRes,
		EmitLineDirectives: true,
	})
	if err != nil {
		t.Fatalf("Lower failed: %v", err)
	}

	// Resulting Go must parse cleanly with standard `go/parser`!
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "script_werk.go", res.GoSource, parser.AllErrors); err != nil {
		t.Fatalf("lowered Go failed to parse:\n%s\nerr: %v", res.GoSource, err)
	}

	// `count` and `client` are referenced inside `func bump` -> hoisted to package globals!
	if !strings.Contains(res.GoSource, "var count int") {
		t.Errorf("expected `var count int` hoisted globally:\n%s", res.GoSource)
	}
	if !strings.Contains(res.GoSource, "var client *http.Client") {
		t.Errorf("expected `var client *http.Client` hoisted via go/types:\n%s", res.GoSource)
	}
	// `msg` is NOT referenced inside `func bump` -> stays `msg := bump("first")` inside `func main()`!
	if !strings.Contains(res.GoSource, `msg := bump("first")`) {
		t.Errorf("expected unreferenced `msg := ...` kept in main():\n%s", res.GoSource)
	}
}

func TestRejectPackageAndFuncMain(t *testing.T) {
	s1 := scanner.Scan("bad_pkg.werk", []byte("package foo\nimport \"fmt\"\nfmt.Println(1)\n"))
	_, err := Lower(LowerOptions{Filename: "bad_pkg.werk", Scan: s1})
	if err == nil || !strings.Contains(err.Error(), "package declarations are not allowed") {
		t.Fatalf("expected package error, got: %v", err)
	}

	s2 := scanner.Scan("bad_main.werk", []byte("import \"fmt\"\nfunc main() { fmt.Println(1) }\n"))
	_, err = Lower(LowerOptions{Filename: "bad_main.werk", Scan: s2})
	if err == nil || !strings.Contains(err.Error(), "cannot declare func main") {
		t.Fatalf("expected func main error, got: %v", err)
	}
}

func TestSpanMapBidirectional(t *testing.T) {
	src := `import "fmt"

config app {
    port = 8080
}

func helper() int {
    return 42
}

fmt.Println(helper())
`
	scanRes := scanner.Scan("span.werk", []byte(src))
	res, err := Lower(LowerOptions{
		Filename:           "span.werk",
		Scan:               scanRes,
		EmitLineDirectives: false,
	})
	if err != nil {
		t.Fatalf("Lower failed: %v", err)
	}

	// Line 10 (0-indexed) is `fmt.Println(helper())`
	goLine, goCol, ok := res.SpanMap.WerkToGo(10, 4)
	if !ok {
		t.Fatalf("expected WerkToGo mapping for line 10")
	}
	goLines := strings.Split(res.GoSource, "\n")
	if goLines[goLine] != "fmt.Println(helper())" {
		t.Fatalf("mapped to wrong Go line %d: %q", goLine, goLines[goLine])
	}
	wLine, wCol, ok := res.SpanMap.GoToWerk(goLine, goCol)
	if !ok || wLine != 10 || wCol != 4 {
		t.Fatalf("inverse GoToWerk = (%d, %d, %v), want (10, 4, true)", wLine, wCol, ok)
	}
}
