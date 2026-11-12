package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ABM2205/werklang/internal/diag"
	"github.com/ABM2205/werklang/internal/format"
	"github.com/ABM2205/werklang/internal/lsp"
	"github.com/ABM2205/werklang/internal/runner"
)

const version = "0.1.0"

func main() {
	os.Exit(runCLI(os.Args[1:]))
}

func runCLI(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "lsp":
			srv := lsp.NewServer(os.Stdin, os.Stdout)
			if err := srv.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "werk lsp error: %v\n", err)
				return 1
			}
			return 0

		case "fmt":
			return cmdFmt(args[1:])

		case "check":
			return cmdCheck(args[1:])

		case "emit-go":
			return cmdEmitGo(args[1:])

		case "version", "--version", "-V":
			fmt.Printf("werk %s\n", version)
			return 0

		case "help", "--help", "-h":
			printUsage()
			return 0

		case "run":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "usage: werk run <file.werk> [targets...]")
				return 1
			}
			return cmdExec(append([]string{"-f", args[1]}, args[2:]...))
		}
	}

	return cmdExec(args)
}

func cmdExec(args []string) int {
	opts := runner.RunOptions{
		Overrides: make(map[string]string),
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		Stdin:     os.Stdin,
	}

	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-l" || a == "--list":
			opts.List = true
		case a == "-v" || a == "--verbose":
			opts.Verbose = true
		case a == "--debug":
			opts.Debug = true
		case a == "--force":
			opts.Force = true
		case a == "-f" || a == "--file":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: -f requires a file path")
				return 1
			}
			i++
			opts.File = args[i]
		case strings.HasPrefix(a, "-f="):
			opts.File = strings.TrimPrefix(a, "-f=")
		case a == "-C" || a == "--dir":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: -C requires a directory path")
				return 1
			}
			i++
			opts.WorkDir = args[i]
		case a == "--compile":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --compile requires an output binary path")
				return 1
			}
			i++
			opts.CompileOut = args[i]
		case strings.HasPrefix(a, "--compile="):
			opts.CompileOut = strings.TrimPrefix(a, "--compile=")
		case a == "-D":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: -D requires key=value")
				return 1
			}
			i++
			if !parseOverride(opts.Overrides, args[i]) {
				return 1
			}
		case strings.HasPrefix(a, "-D"):
			if !parseOverride(opts.Overrides, strings.TrimPrefix(a, "-D")) {
				return 1
			}
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "unknown flag: %s (run 'werk --help' for usage)\n", a)
			return 1
		default:
			positional = append(positional, a)
		}
	}

	// Support direct `werk script.werk` or `#!/usr/bin/env werk` execution
	if opts.File == "" && len(positional) > 0 && looksLikeScriptFile(positional[0]) {
		opts.File = positional[0]
		positional = positional[1:]
	}

	if opts.File == "" {
		found, err := findDefaultWerkfile(opts.WorkDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		opts.File = found
	}

	opts.Targets = positional
	code, err := runner.Run(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		if code == 0 {
			return 1
		}
	}
	return code
}

func looksLikeScriptFile(arg string) bool {
	if strings.HasSuffix(arg, ".werk") || strings.ContainsRune(arg, filepath.Separator) || strings.HasPrefix(arg, ".") {
		if st, err := os.Stat(arg); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

func findDefaultWerkfile(workDir string) (string, error) {
	dir := workDir
	if dir == "" {
		dir = "."
	}
	for _, name := range []string{"Werkfile", "werkfile", "build.werk"} {
		cand := filepath.Join(dir, name)
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	return "", fmt.Errorf("no Werkfile found in %s (specify a file with '-f <file.werk>' or 'werk <script.werk>')", dir)
}

func parseOverride(dst map[string]string, kv string) bool {
	idx := strings.IndexByte(kv, '=')
	if idx <= 0 {
		fmt.Fprintf(os.Stderr, "invalid -D override %q (expected key=value)\n", kv)
		return false
	}
	dst[kv[:idx]] = kv[idx+1:]
	return true
}

func cmdFmt(args []string) int {
	checkOnly := false
	var files []string
	for _, a := range args {
		if a == "--check" {
			checkOnly = true
		} else {
			files = append(files, a)
		}
	}
	if len(files) == 0 {
		wf, err := findDefaultWerkfile(".")
		if err != nil {
			fmt.Fprintln(os.Stderr, "usage: werk fmt [--check] <files...>")
			return 1
		}
		files = []string{wf}
	}

	unformatted := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading %s: %v\n", f, err)
			return 1
		}
		formatted, err := format.Source(f, string(raw))
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		if formatted != string(raw) {
			if checkOnly {
				fmt.Println(f)
				unformatted++
			} else {
				if err := os.WriteFile(f, []byte(formatted), 0o644); err != nil {
					fmt.Fprintf(os.Stderr, "error writing %s: %v\n", f, err)
					return 1
				}
			}
		}
	}
	if checkOnly && unformatted > 0 {
		return 1
	}
	return 0
}

func cmdCheck(args []string) int {
	file := ""
	if len(args) > 0 {
		file = args[0]
	} else {
		var err error
		file, err = findDefaultWerkfile(".")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}
	wd, _ := os.Getwd()
	cp := runner.Check(file, wd, nil)
	hasErr := false
	for _, d := range cp.Diagnostics {
		fmt.Fprintln(os.Stderr, d.String())
		if d.Severity == diag.SeverityError {
			hasErr = true
		}
	}
	if hasErr {
		return 1
	}
	return 0
}

func cmdEmitGo(args []string) int {
	file := ""
	if len(args) > 0 {
		file = args[0]
	} else {
		var err error
		file, err = findDefaultWerkfile(".")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}
	wd, _ := os.Getwd()
	goCode, _, err := runner.Transpile(file, wd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	fmt.Print(goCode)
	return 0
}

func printUsage() {
	fmt.Print(`werk — Go + Apple Pkl scripting & Mage-powered build language

Usage:
  werk [flags] [targets...]         Run target(s) in ./Werkfile
  werk <script.werk>                Run a standalone .werk script
  werk fmt [--check] [files...]     Format .werk files canonically
  werk check [file.werk]            Typecheck and validate imports/DAG
  werk emit-go [file.werk]          Print transpiled Go Magefile source
  werk lsp                          Start Language Server Protocol (stdio)

Flags:
  -l, --list             List available targets (including mage:... imports)
  -f, --file <path>      Specify Werkfile or .werk script path
  -C, --dir <dir>        Change working directory before running
  -D <key=value>         Override top-level property (int, float, string, bool)
  -v, --verbose          Verbose Mage execution logs
  --force                Force recompilation of cached Mage binary
  --compile <out>        Compile .werk file into a standalone static binary
  -V, --version          Show werk version
`)
}
