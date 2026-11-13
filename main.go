package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ABM2205/werklang/internal/golower"
	"github.com/ABM2205/werklang/internal/lsp"
	"github.com/ABM2205/werklang/internal/pklschema"
	"github.com/ABM2205/werklang/internal/runner"
)

const version = "0.2.0"

func main() {
	os.Exit(runCLI(os.Args[1:], true))
}

func runCLI(args []string, allowExecReplace bool) int {
	if len(args) == 0 {
		for _, def := range []string{"Werkfile", "werkfile"} {
			if st, err := os.Stat(def); err == nil && !st.IsDir() {
				return execDirectScript(def, nil, allowExecReplace)
			}
		}
		printUsage()
		return 0
	}

	switch args[0] {
	case "init":
		return cmdInit(args[1:])
	case "run":
		return cmdRun(args[1:], allowExecReplace)
	case "build":
		return cmdBuild(args[1:])
	case "test":
		return cmdTest(args[1:])
	case "generate":
		return cmdGenerate(args[1:])
	case "fmt":
		return cmdFmt(args[1:])
	case "pkl-reader":
		return cmdPklReader(args[1:])
	case "lsp":
		srv := lsp.NewServer(os.Stdin, os.Stdout)
		if err := srv.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "werk lsp: %v\n", err)
			return 1
		}
		return 0
	case "version", "--version", "-V":
		fmt.Printf("werk %s\n", version)
		return 0
	case "help", "--help", "-h":
		printUsage()
		return 0
	}

	// Direct script execution (`werk ./script.werk [args...]` or `#!/usr/bin/env werk`)
	if isDirectScriptCandidate(args[0]) {
		return execDirectScript(args[0], args[1:], allowExecReplace)
	}

	// If a Werkfile exists in the current directory, forward task arguments to it (`werk check`, `werk install`, etc.)
	if !strings.HasPrefix(args[0], "-") && !strings.Contains(args[0], "/") && !strings.HasSuffix(args[0], ".werk") {
		for _, def := range []string{"Werkfile", "werkfile"} {
			if st, err := os.Stat(def); err == nil && !st.IsDir() {
				return execDirectScript(def, args, allowExecReplace)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "werk: unknown command or missing script %q (run 'werk --help' for usage)\n", args[0])
	return 1
}

func isDirectScriptCandidate(arg string) bool {
	if strings.HasPrefix(arg, "-") {
		return false
	}
	if strings.HasSuffix(arg, ".werk") {
		return true
	}
	if st, err := os.Stat(arg); err == nil && !st.IsDir() {
		return true
	}
	return false
}

func execDirectScript(scriptPath string, scriptArgs []string, allowExecReplace bool) int {
	code, err := runner.Run(runner.RunOptions{
		ScriptPath:  scriptPath,
		ScriptArgs:  scriptArgs,
		ExecReplace: allowExecReplace,
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		if code == 0 {
			return 1
		}
	}
	return code
}

func cmdInit(args []string) int {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	path, changed, err := pklschema.InitProject(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "werk init: %v\n", err)
		return 1
	}
	if changed {
		fmt.Printf("Configured Pkl external module reader 'werk' in %s\n", path)
	} else {
		fmt.Printf("PklProject already configured in %s\n", path)
	}
	return 0
}

func cmdRun(args []string, allowExecReplace bool) int {
	var cfgs []string
	rebuild := false
	var target string
	var scriptArgs []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if target != "" {
			if a == "--" && len(scriptArgs) == 0 {
				scriptArgs = append(scriptArgs, args[i+1:]...)
				break
			}
			scriptArgs = append(scriptArgs, a)
			continue
		}
		switch {
		case a == "-c" || a == "--config":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "werk run: -c requires a .pkl file path")
				return 1
			}
			i++
			cfgs = append(cfgs, args[i])
		case strings.HasPrefix(a, "-c="):
			cfgs = append(cfgs, strings.TrimPrefix(a, "-c="))
		case strings.HasPrefix(a, "--config="):
			cfgs = append(cfgs, strings.TrimPrefix(a, "--config="))
		case a == "--rebuild":
			rebuild = true
		case a == "-h" || a == "--help":
			fmt.Fprintln(os.Stdout, "usage: werk run [-c file.pkl] [--rebuild] <file.werk|dir> [-- args...]")
			return 0
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "werk run: unknown flag %s\n", a)
			return 1
		default:
			target = a
		}
	}

	if target == "" {
		fmt.Fprintln(os.Stderr, "usage: werk run [-c file.pkl] [--rebuild] <file.werk|dir> [-- args...]")
		return 1
	}

	st, err := os.Stat(target)
	if err == nil && st.IsDir() {
		entries, _ := os.ReadDir(target)
		found := ""
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".werk") {
				found = filepath.Join(target, e.Name())
				break
			}
		}
		if found == "" {
			for _, def := range []string{"Werkfile", "werkfile"} {
				cand := filepath.Join(target, def)
				if info, sErr := os.Stat(cand); sErr == nil && !info.IsDir() {
					found = cand
					break
				}
			}
		}
		if found != "" {
			target = found
		}
	}

	code, err := runner.Run(runner.RunOptions{
		ScriptPath:  target,
		ConfigFiles: cfgs,
		ScriptArgs:  scriptArgs,
		Rebuild:     rebuild,
		ExecReplace: allowExecReplace,
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		if code == 0 {
			return 1
		}
	}
	return code
}

func cmdBuild(args []string) int {
	var cfgs []string
	var outPath string
	var goArgs []string
	var positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c" || a == "--config":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "werk build: -c requires a .pkl file path")
				return 1
			}
			i++
			cfgs = append(cfgs, args[i])
		case strings.HasPrefix(a, "-c="):
			cfgs = append(cfgs, strings.TrimPrefix(a, "-c="))
		case strings.HasPrefix(a, "--config="):
			cfgs = append(cfgs, strings.TrimPrefix(a, "--config="))
		case a == "-o":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "werk build: -o requires an output path")
				return 1
			}
			i++
			outPath = args[i]
		case strings.HasPrefix(a, "-o="):
			outPath = strings.TrimPrefix(a, "-o=")
		case a == "-ldflags" || a == "-tags" || a == "-gcflags" || a == "-asmflags" || a == "-mod":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "werk build: %s requires a value\n", a)
				return 1
			}
			goArgs = append(goArgs, a, args[i+1])
			i++
		case a == "-h" || a == "--help":
			fmt.Fprintln(os.Stdout, "usage: werk build [-c file.pkl] [-o output] [go build flags...] [file.werk|dir]")
			return 0
		case strings.HasPrefix(a, "-"):
			goArgs = append(goArgs, a)
		default:
			positional = append(positional, a)
		}
	}

	target := "."
	if len(positional) > 0 {
		target = positional[len(positional)-1]
	}

	if err := runner.Build(runner.BuildOptions{
		Target:      target,
		OutputPath:  outPath,
		ConfigFiles: cfgs,
		GoBuildArgs: goArgs,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	return 0
}

func cmdTest(args []string) int {
	var cfgs []string
	var goArgs []string
	var targets []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c" || a == "--config":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "werk test: -c requires a .pkl file path")
				return 1
			}
			i++
			cfgs = append(cfgs, args[i])
		case strings.HasPrefix(a, "-c="):
			cfgs = append(cfgs, strings.TrimPrefix(a, "-c="))
		case strings.HasPrefix(a, "--config="):
			cfgs = append(cfgs, strings.TrimPrefix(a, "--config="))
		case a == "-run" || a == "-bench" || a == "-timeout" || a == "-count" || a == "-tags":
			if i+1 < len(args) {
				goArgs = append(goArgs, a, args[i+1])
				i++
			} else {
				goArgs = append(goArgs, a)
			}
		case strings.HasPrefix(a, "-"):
			goArgs = append(goArgs, a)
		default:
			targets = append(targets, a)
		}
	}

	code, err := runner.Test(runner.TestOptions{
		Targets:     targets,
		ConfigFiles: cfgs,
		GoTestArgs:  goArgs,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		if code == 0 {
			return 1
		}
	}
	return code
}

func cmdGenerate(args []string) int {
	var cfgs []string
	var targets []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c" || a == "--config":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "werk generate: -c requires a .pkl file path")
				return 1
			}
			i++
			cfgs = append(cfgs, args[i])
		case strings.HasPrefix(a, "-c="):
			cfgs = append(cfgs, strings.TrimPrefix(a, "-c="))
		case strings.HasPrefix(a, "--config="):
			cfgs = append(cfgs, strings.TrimPrefix(a, "--config="))
		default:
			targets = append(targets, a)
		}
	}

	written, err := runner.Generate(runner.GenerateOptions{
		Targets:     targets,
		ConfigFiles: cfgs,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "werk generate: %v\n", err)
		return 1
	}
	for _, w := range written {
		fmt.Println(w)
	}
	return 0
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
		fmt.Fprintln(os.Stderr, "usage: werk fmt [--check] <files...>")
		return 1
	}
	code := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "werk fmt: %v\n", err)
			return 1
		}
		fmted, err := golower.FormatWerk(f, raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		if checkOnly {
			if !bytes.Equal(raw, fmted) {
				fmt.Fprintf(os.Stderr, "%s: not formatted\n", f)
				code = 1
			}
		} else if !bytes.Equal(raw, fmted) {
			if err := os.WriteFile(f, fmted, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "werk fmt: %v\n", err)
				return 1
			}
		}
	}
	return code
}

func cmdPklReader(args []string) int {
	rootDir := ""
	for i := 0; i < len(args); i++ {
		if (args[i] == "--root" || args[i] == "-C") && i+1 < len(args) {
			rootDir = args[i+1]
			i++
		}
	}
	if err := pklschema.RunExternalReader(rootDir); err != nil {
		fmt.Fprintf(os.Stderr, "werk pkl-reader: %v\n", err)
		return 1
	}
	return 0
}

func printUsage() {
	fmt.Print(`werk — Go + Pkl hybrid scripting & configuration tool

Usage:
  werk <script.werk> [args...]                 Run a .werk script directly (supports #!/usr/bin/env werk)
  werk run [-c file.pkl] <script.werk> [args]  Preprocess, cache, and run a .werk script
  werk build [-c file.pkl] [-o bin] [target]   Compile a .werk script into a native Go binary
  werk test [-c file.pkl] [pkgs...]            Run go test
  werk generate [-c file.pkl] [targets...]     Materialize lowered *_werk.go files on disk
  werk fmt [--check] <files...>                Format .werk scripts
  werk init [dir]                              Configure PklProject with the werk: external module reader
  werk pkl-reader                              Run Pkl External Module Reader over stdio
  werk lsp                                     Start hybrid Go + Pkl Language Server
  werk version                                 Print werk version
`)
}
