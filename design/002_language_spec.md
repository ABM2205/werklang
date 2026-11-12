# Werklang Language Specification (v0.2)

## 1. Goals & Architecture

**Werklang** (`.werk` / `Werkfile`) is a small, statically-typed scripting and build language designed to replace **shell scripts** and **Makefiles**.

It blends three pieces into one cohesive system:
1. **Go Subset**: Basic types (`int`, `float`, `string`, `bool`), typed slices (`[]string`), Go-style field declarations (`port int = 8080`), and imperative `if` / `for` blocks—**with no user-defined `func` declarations**.
2. **Apple Pkl Modules as Structs**: Every `.werk` file (or inline `config` block) is a struct schema and default instance. Configuration is composed via `amends` and `{ ... }` object blocks, with `\(expr)` string interpolation and required-field checks.
3. **Mage Under the Hood (`github.com/magefile/mage`)**: Werklang transpiles `.werk` files into Go Magefiles and executes them through `mage/mage`. Targets map directly to Mage functions, `deps` / `parallel_deps` map to `mg.SerialDeps` / `mg.Deps`, shell commands map to `mage/sh`, and file staleness checks map to `mage/target`.

### Key Benefits of Transpiling to Mage
- **Zero traditional functions needed**: Reusable logic is modeled cleanly as composable `target` blocks (executed at most once per run via Mage's DAG) and declarative Pkl modules.
- **Sequential by default, parallel on demand**: `deps` compiles to `mg.SerialDeps(...)` for predictable execution; `parallel_deps` compiles to `mg.Deps(...)` for concurrent builds.
- **Cached native speed & standalone binaries**: Mage caches compiled binaries automatically and even allows `werk --compile ./bin/task` to compile any `.werk` script into a zero-dependency static binary.

---

## 2. Lexical Elements & Keywords

### Reserved Keywords (see `001_keywords.md`)

| Category | Keywords |
| :--- | :--- |
| **Types** | `int`, `float`, `string`, `bool` |
| **Modules & Targets** | `amends`, `import`, `as`, `config`, `target` |
| **Control Flow** | `if`, `then`, `else`, `for`, `in` |
| **Literals** | `true`, `false` |

*(Note: `func` and `return` are intentionally omitted.)*

### Comments & Literals
- Line comments: `// comment` (plus `#!` shebang ignored on line 1 for executable `.werk` scripts)
- Numbers: `42` (`int` -> Go `int64` / `int`), `3.14` (`float` -> Go `float64`)
- Strings:
  - Double-quoted with Pkl-style `\(expr)` interpolation: `"listening on \(host):\(port)"`
  - Multiline / raw strings using triple quotes `"""..."""`
- Slices: `["lint", "test"]`, `[80, 443]`

---

## 3. Type System (Minimal Go Subset)

| Werklang Type | Generated Go Type | Notes |
| :--- | :--- | :--- |
| `int` | `int` | Signed integer |
| `float` | `float64` | 64-bit IEEE-754 float |
| `string` | `string` | UTF-8 string |
| `bool` | `bool` | Strict booleans (`true` / `false`) |
| `[]T` | `[]T` | Homogeneous typed slice (e.g., `[]string`, `[]int`, `[]Service`) |
| Module | Generated Go `struct` | Defined by a `.werk` file or `config` block |

*(Explicitly excluded: maps, pointers, `nil`, interfaces, channels, generics, user-defined functions.)*

### Declarations (Go-style Types, Pkl-style Defaults)
 Declarations use Go-style `<name> <type>` without colons, or omit the type when inferred from `=`:

```werk
// Inferred type
app_name = "werk"
retries  = 3
tags     = ["cli", "go"]

// Explicit Go-style type + default value
port int = 8080
ratio float = 0.75

// Required module field (no default; must be supplied when amended)
api_token string
```

---

## 4. Modules as Structs (The Pkl Model)

Instead of `type Foo struct { ... }`, **every `.werk` file is a module that compiles to a Go struct + constructor/amend helper**.

### 4.1 Defining a Module (`deploy/service.werk`)
```werk
// deploy/service.werk
name string
image string
replicas int = 1
ports []int = [8080]
debug bool = false

// Derived field (evaluated after overrides are applied)
full_tag string = "\(image):\(if debug then "dev" else "latest")"
```

### 4.2 Amending a Module File (`deploy/prod.werk`)
A `.werk` file can `amends` another `.werk` module to inherit its schema and override values:
```werk
// deploy/prod.werk
amends "service.werk"

name = "billing-api"
image = "ghcr.io/acme/billing"
replicas = 4
```

### 4.3 Importing & Instantiating Modules as Structs
Scripts and `Werkfile`s can `import` a module and instantiate or amend it using `{ ... }` blocks (see `003_imports_and_interop.md` for importing `.pkl` files, `mage:...` target packages, and `go:...` libraries):

```werk
import "deploy/service.werk" as Service

api = Service {
    name = "gateway"
    image = "ghcr.io/acme/gateway"
    ports = [80, 443]
}

// Amend an existing struct value (returns a new modified copy)
canary = api {
    replicas = 1
    debug = true
}
```

### 4.4 Inline `config` Blocks
For single-file `Werkfile`s, `config` defines an inline struct instance without needing a separate file:

```werk
config build {
    goos string   = env("GOOS", "linux")
    goarch string = env("GOARCH", "amd64")
    bin string    = "bin/werk"
}
```

---

## 5. Expressions & Control Flow

### Conditional Expression (`if ... then ... else ...`)
Used inside configuration properties, variable bindings, and arguments:
```werk
ldflags = if release then "-s -w" else ""
```

### Imperative Control Flow (Go-style)
Valid inside `target` bodies and top-level scripts:
```werk
if !exists("bin") {
    mkdir("bin")
} else {
    print("bin/ already exists")
}

for pkg in ["lexer", "parser", "transpiler"] {
    sh("go test ./internal/\(pkg)/...")
}
```

### Operators
- Arithmetic / Concat: `+` (numbers, strings, slices), `-`, `*`, `/`, `%`
- Comparison: `==`, `!=`, `<`, `<=`, `>`, `>=`
- Logical: `&&`, `||`, `!`

---

## 6. Shell Execution & Built-ins (Powered by `mage/sh`)

Commands fail fast by default: in generated Go code, errors return immediately from the Mage target (triggering Mage's clean error reporting and non-zero exit code).

| Built-in | Signature | Generated Mage / Go Mapping |
| :--- | :--- | :--- |
| `sh` | `sh(cmd string)` | `sh.RunV("sh", "-c", cmd)` (streams output, fails on non-zero) |
| `exec` | `exec(prog string, args []string)` | `sh.RunV(prog, args...)` (no shell parsing/quoting issues) |
| `capture` | `capture(cmd string) -> string` | `sh.Output("sh", "-c", cmd)` (returns trimmed stdout) |
| `try_sh` | `try_sh(cmd string) -> int` | `sh.ExitStatus(sh.Run("sh", "-c", cmd))` (never aborts) |
| `rm` | `rm(path string)` | `sh.Rm(path)` (recursive safe remove, like `rm -rf`) |
| `mkdir` | `mkdir(path string)` | `os.MkdirAll(path, 0o755)` |
| `env` | `env(key string, fallback string = "") -> string` | `os.Getenv` with fallback if empty |
| `glob` | `glob(pattern string) -> []string` | `filepath.Glob` / recursive `**` globbing |
| `exists` | `exists(path string) -> bool` | `os.Stat` existence check |
| `print` | `print(val any)` | `fmt.Println(val)` |
| `fail` | `fail(msg string)` | `mg.Fatal(1, msg)` |

---

## 7. Target System (Powered by Mage)

```werk
target fmt {
    desc = "Check Go formatting"
    out = capture("gofmt -l .")
    if out != "" {
        fail("unformatted files:\n\(out)")
    }
}

target lint {
    desc = "Run go vet"
    exec("go", ["vet", "./..."])
}

target test {
    desc = "Run unit tests after fmt and lint"
    parallel_deps = ["fmt", "lint"]
    sh("go test -v ./...")
}

target build {
    desc    = "Compile \(app.name)"
    deps    = ["test"]
    inputs  = glob("internal/**/*.go") + ["main.go", "go.mod"]
    outputs = [app.out]

    exec("go", ["build", "-ldflags", app.ldflags, "-o", app.out, "."])
}
```

### How Targets Transpile to Mage (`magefile.go`)

1. **`desc`** -> Emitted as the Go doc comment `// Compile ...` above `func Build() error`, so `werk -l` (via Mage) prints formatted target descriptions automatically.
2. **`deps`** -> Emitted as `mg.SerialDeps(Test)` at the start of the target function (sequential execution, deduplicated across the run).
3. **`parallel_deps`** -> Emitted as `mg.Deps(Fmt, Lint)` (concurrent execution via Mage's goroutine scheduler, deduplicated).
4. **`inputs` & `outputs`** -> Emitted using `github.com/magefile/mage/target`:
   - If omitted, target always runs (phony by default—no `.PHONY` needed!).
   - If both are present, checks `target.Path(out, inputs...)` for each output; if nothing is newer, returns `nil` early!
5. **Default Target & Standalone Scripts**:
   - If a `Werkfile` defines `target default` (or if not, the first declared `target`), `var Default = <Target>` is emitted for bare `werk` invocations.
   - If a `.werk` script has **top-level imperative statements and no `target` blocks** (e.g., [test.werk](file:///home/amccann/Projects/werklang/test/test.werk)), the transpiler automatically wraps those statements into a default Mage target so standalone shell-replacement scripts run through the exact same pipeline!

---

## 8. Transpilation Pipeline

```
  Werkfile / *.werk
         │
   [1. Lexer]       (internal/lexer)
         │
   [2. Parser]      (internal/parser -> AST)
         │
   [3. Checker]     (internal/types  -> resolve imports/amends, check types & required fields)
         │
   [4. Codegen]     (internal/codegen -> emit //go:build mage Go source in workdir cache)
         │
   [5. Mage Runner] (github.com/magefile/mage/mage -> ParseAndRun / compile / execute)
```

### Example Transpilation (`test/test.werk` -> Go Magefile)

Input (`test/test.werk`):
```werk
foo = "hello world"
bar = "from pkl"

print(foo)
print(bar)
```

Generated Go (`magefile_gen.go`):
```go
//go:build mage

package main

import "fmt"

var Default = Run

func Run() error {
	foo := "hello world"
	bar := "from pkl"
	fmt.Println(foo)
	fmt.Println(bar)
	return nil
}
```
