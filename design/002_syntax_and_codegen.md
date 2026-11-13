# 002 — `.werk` Script Semantics, `config` Syntax & Go Lowering

## 1. File Layout & Grammar

A `.werk` file is an executable UTF-8 script with a header section (`import`s and optional `config` block) followed by freely interleaved Go declarations and executable statements:

```ebnf
WerkFile   = [ Shebang ] { HeaderItem } { BodyItem } .
Shebang    = "#!" { ArbitraryCharExceptNewline } "\n" .
HeaderItem = GoImportDecl | ConfigDecl .
ConfigDecl = "config" [ Identifier ] "{" PklModuleBody "}" .
BodyItem   = GoTypeDecl | GoConstDecl | GoVarDecl | GoFuncDecl | GoMethodDecl | GoStatement .
```

### Shebang Support (`#!`)
- A `.werk` file (or extensionless executable script) may begin on **line 1, column 0** with `#!` (for example, `#!/usr/bin/env werk` or `#!/usr/bin/env -S werk run -c prod.pkl`).
- During scanning, line 1 is stripped and replaced with an empty line (`\n`), preserving exact 1:1 line numbers for both Go `//line` directives and Pkl projections from line 2 onward.
- CLI dispatch supports direct script invocation (`werk <script> [args...]`) whenever `os.Args[1]` is not a reserved subcommand and points to a `.werk` file or existing script path—making `#!/usr/bin/env werk` work portably on Linux and macOS without requiring `env -S`, while forwarding all trailing arguments untouched to the script's `os.Args[1:]`.

### Disallowed Go Boilerplate
To enforce a uniform Python-style scripting model:
1. **No `package` clause:** `package ...` is disallowed in `.werk` files (`werk` always synthesizes `package main`). Encountering `package` produces a compile error explaining that `.werk` scripts implicitly target `main`.
2. **No `func main()` declaration:** Defining `func main(...)` is disallowed (`werk` synthesizes `func main()` from the top-level script body).
3. **Header ordering:** `import` declarations and the optional `config` block appear in the file header (in any order relative to each other) before top-level declarations and executable statements.
4. **At most one `config` block per `.werk` file:** Having more than one `config` block in the same `.werk` file is a compile error so every `.werk` file maps 1:1 to a single amendable Pkl module (`werk:/path/to/file.werk`).

### Two-Mode Lexical Scanning
`werk`'s scanner partitions the `.werk` source before Go parsing:
- **Go Host Mode (outside `config {}`):** Tracks Go line comments (`//`), block comments (`/* ... */`), interpreted strings (`"..."`), raw strings (`` `...` ``), rune literals (`'...'`), and `{` / `}` brace depth so `config` inside strings, comments, structs, or function bodies is treated as normal Go text.
- **Pkl Body Mode (inside `config { ... }`):** Tracks Pkl line comments (`//`), **nested** block comments (`/* ... */`), single-line strings (`"..."`), multiline strings (`"""..."""`), custom-delimited strings (`#"..."#`), string interpolations (`\(...)`), and `{` / `}` depth until the matching closing `}` returns to Go Host Mode.

---

## 2. Script Execution & Python-Style Global Scoping

Below the header, `.werk` behaves like a Python script while compiling to 100% static Go:

### Declarations vs. Statements
- **Declarations (`type`, `const`, `func`, and `config`):** Hoisted to Go **package scope**, so functions, types, and constants can be referenced anywhere in the file (including before their textual definition if mutually recursive).
- **Executable statements (`if`, `for`, `switch`, `select`, `defer`, `go`, assignments, expression/call statements, bare `return`):** Emitted in **exact top-to-bottom source order** inside the synthesized `func main() { ... }`.

### Python-Style Global Variables (`var` and top-level `:=`)
Both `var` declarations and top-level short variable declarations (`:=`) create **package-global variables** visible to all top-level helper `func`s and statements—while their initializer expressions still execute **top-to-bottom inside `func main()`**:

```go
count := 0

func bump() {
    count++ // Reads and mutates top-level `count` directly (no `global` keyword needed!)
}

bump()
bump()
```

#### Lowering Strategy for Top-Level Variables
1. **Explicitly typed `var x T = expr` (or `var x T`):**
   - Hoists `var x T` to package scope.
   - If an initializer `= expr` is present, emits `x = expr` at its exact source position inside `func main()`.
2. **Inferred `var x = expr` and short `x := expr` at brace depth 0:**
   - **Referenced inside a top-level `func` body:** `werk` resolves `x`'s static Go type (via a fast syntactic check for literals/composite literals/`config` fields, falling back to `go/types` against imported packages and top-level `func` signatures), hoists `var x <Type>` to package scope, and rewrites the statement in `func main()` to `x = expr`.
   - **Only referenced in top-level statements (not captured by any `func`):** Kept directly inside `func main()` (`x := expr` / `var x = expr`), which preserves identical top-to-bottom semantics, supports unexported return types from external packages, and requires zero type synthesis.
   - **Multi-variable redeclaration (`a, err := f()` then `b, err := g()`):** Any variable hoisted to package scope (`var err error`) is declared once globally and assigned via `=` in `func main()`, preserving Go's redeclaration rules via `go/types`.

---

## 3. `config` Naming & Go Visibility

The optional identifier on `config [name] { ... }` controls the generated Go variable and struct type names using Go's capitalization rules:

| `.werk` Declaration | Generated Go Variable | Generated Go Struct Type | Visibility |
| :--- | :--- | :--- | :--- |
| `config { ... }` *(anonymous)* | `var config` | `type werkConfig struct` *(or `<fileStem>Config`)* | Package-private |
| `config app { ... }` | `var app` | `type appConfig struct` | Package-private |
| `config App { ... }` | `var App` | `type AppConfig struct` | Exported |

- Each `config` block emits a **plain package-level `var` struct value** (`var app = appConfig{ ... }`), initialized before `func main()` begins so both global helpers and top-level statements can read `app.HttpPort`, `&app.Host`, etc.
- Because `config` is not a reserved keyword in Go, anonymous `config { httpPort: UInt16 = 8080 }` lets script code read `config.HttpPort` naturally.

### Field & Struct Naming (`UpperFirst`)
- Pkl property names are converted to **exported Go struct fields** using deterministic **`UpperFirst`** casing (without Go initialism rewriting):
  - `camelCase` properties uppercase the leading character: `httpPort` -> `HttpPort`, `apiUrl` -> `ApiUrl`, `id` -> `Id`.
  - `snake_case` properties split on `_` and `UpperFirst` each segment: `max_conns` -> `MaxConns`, `api_url` -> `ApiUrl`.
- Nested generated struct types for untyped/`Dynamic` object blocks are named `<configPrefix><FieldPath>Config` matching the parent's visibility (e.g., `db { ... }` inside `config app` -> `type appDbConfig struct`).
- Pkl `local` and `hidden` properties inside `config {}` are evaluated by Pkl and **omitted** from the generated Go struct.

---

## 4. Pkl-to-Go Type Mapping & `Dynamic` Inference

`werk` combines static Pkl schema introspection (`pkl:reflect` on the template module) with evaluated value inspection (`pkl.Object` via `pkl-go`) so authors can freely mix explicit Pkl types/constraints with concise untyped properties and `Dynamic` blocks.

### Explicit Pkl Types

| Pkl Type | Generated Go Type | Notes |
| :--- | :--- | :--- |
| `String` | `string` | |
| `Boolean` | `bool` | |
| `Int` | `int` | |
| `Int8`, `Int16`, `Int32`, `Int64` | `int8`, `int16`, `int32`, `int64` | |
| `UInt`, `UInt8`, `UInt16`, `UInt32` | `uint`, `uint8`, `uint16`, `uint32` | `UInt` in Pkl is unsigned 64-bit non-negative `Int`; maps to `uint` (or `uint64` if `UInt64` alias used). |
| `Float` | `float64` | |
| `Duration` | `time.Duration` | Converted to nanoseconds (`_werk_time.Duration(<ns>)`) at compile time. |
| `DataSize` | `int64` | Converted to exact byte count (`int64(<bytes>)`) at compile time. |
| `Pair<A, B>` | `struct { First A; Second B }` | |
| `Listing<T>`, `List<T>`, `Set<T>` | `[]T` | Elements emitted in evaluation order. |
| `Mapping<K, V>`, `Map<K, V>` | `map[K]V` | Keys sorted deterministically during codegen. |
| `T?` *(Nullable)* | `*T` *(scalars/structs)* or `[]T` / `map[K]V` | Non-nil scalar pointers initialized via `new(val)` (Go 1.26+). |
| `class Foo { ... }` | `type Foo struct { ... }` | Prefixed if unexported to avoid collisions. |
| `"a" \| "b" \| "c"` *(String literal union / `typealias`)* | `string` *(or `type <Alias> string` + `const`s)* | Pkl enforces membership at compile time; named `typealias` emits a typed Go string + constants. |
| Constrained types (`UInt16(this > 1024)`, `String(!isBlank)`) | Underlying Go type (`uint16`, `string`) | Constraint validated by Pkl at compile time before Go codegen. |
| `Any` | Inferred concrete Go type from evaluated value (`any` if `null`) | |

### Untyped Properties & `Dynamic` Object Inference

When a property in `config {}` omits a type annotation (e.g., `host = "localhost"`, `db { ... }`, `tags { "web"; "api" }`) or is explicitly typed as `Dynamic`, Pkl evaluates it as `pkl.base#Dynamic` (`pkl.Object` with `Properties`, `Elements`, and `Entries`). `werk` infers static Go types from its evaluated shape:

| Untyped / `Dynamic` Pkl Shape | Example in `config {}` | Generated Go Type |
| :--- | :--- | :--- |
| **Untyped scalar** | `host = "localhost"` / `port = 5432` | Concrete Go scalar (`string`, `int`, `bool`, `float64`, `time.Duration`, `int64`) |
| **Object with properties** | `db { host = "localhost"; port = 5432 }` | Generated nested struct (`type appDbConfig struct { Host string; Port int }`) |
| **Object with homogeneous elements** | `tags { "web"; "api" }` | Typed Go slice (`[]string`, `[]int`, `[]appItemsElemConfig`, etc.) |
| **Object with heterogeneous elements** | `mixed { "web"; 42 }` | `[]any` |
| **Object with homogeneous entries** | `headers { ["X-Env"] = "prod" }` | Typed Go map (`map[string]string`, etc.) |
| **Object with heterogeneous entry values** | `meta { ["a"] = 1; ["b"] = "x" }` | `map[K]any` |
| **Mixed `Dynamic` object** *(properties + elements/entries)* | `node { name = "root"; "child1" }` | Struct with `UpperFirst` property fields plus `Elements []T` and/or `Entries map[K]V` |
| **Empty `Dynamic` object** | `extra {}` | `struct{}` |

> **Amending `Dynamic` objects:** Pkl natively merges nested `Dynamic` blocks on `amends` (e.g., `db { host = "db.prod.internal" }` in `prod.pkl` overrides `host` while preserving `port = 5432` from `server.werk`). If an amending `.pkl` file adds new properties to a `Dynamic` block, those properties are also included on the generated Go struct at build time.

---

## 5. Amending `.pkl` Selection & Compile-Time Resolution

When running `werk run`, `werk build`, `werk test`, or `werk generate`, `werk` determines what Pkl module to evaluate for each `.werk` file using the following precedence:

1. **Explicit CLI flag (`-c` / `--config <path.pkl>`, repeatable):**
   - `werk` parses the `amends` header of the supplied `.pkl` file and binds it to the `.werk` file it amends.
   - Passing a `.pkl` file via `-c` that does not `amends` any `.werk` file in the build is a compile error.
2. **Convention fallback (when `-c` is omitted):**
   - Look for `<stem>.pkl` in the same directory as `<stem>.werk` (e.g., `server.pkl` alongside `server.werk`) that amends it.
   - Otherwise look for `config.pkl` in the `.werk` file's directory or workspace root that amends it.
3. **Template defaults fallback:**
   - Evaluate the `.werk` file's `config {}` block directly. If required properties lack defaults and no amending `.pkl` file was provided or discovered, Pkl evaluation fails at compile time with a source-mapped diagnostic.

---

## 6. Preprocessed Go Code Generation

Given `server.werk` + `prod.pkl` from [001_overview.md](001_overview.md), `werk` synthesizes `package main`, emits the `app` config struct and value, hoists global variables (`banner`) and `func dbAddr()`, and lowers top-level execution into `func main()` with `//line` directives pointing back to `server.werk`:

```go
// Code generated by werk; DO NOT EDIT.
package main

//line server.werk:1
import "fmt"

type appDbConfig struct {
	Host string
	Port int
}

type appConfig struct {
	Host     string
	HttpPort uint16
	Env      string
	Db       appDbConfig
}

var app = appConfig{
	Host:     "0.0.0.0",
	HttpPort: 443,
	Env:      "prod",
	Db: appDbConfig{
		Host: "db.prod.internal",
		Port: 5432,
	},
}

var banner string

//line server.werk:24
func dbAddr() string {
	return fmt.Sprintf("%s:%d", app.Db.Host, app.Db.Port)
}

func main() {
//line server.werk:22
	banner = fmt.Sprintf("[%s] starting server", app.Env)
//line server.werk:29
	fmt.Println(banner)
	fmt.Printf("listening on %s:%d (db %s)\n", app.Host, app.HttpPort, dbAddr())
}
```

*(Note: in `server.werk`, if `banner` were referenced inside `dbAddr()`, hoisting `var banner string` makes it globally visible; if not referenced by any `func`, keeping `banner := ...` inside `main()` produces identical behavior.)*

### Build Integration Modes
- **Overlay / Ephemeral Mode (`werk <script>` / `werk run` / `werk build` / `werk test`):** Generates lowered `.go` files in a cache directory so the working tree stays clean of generated `.go` files.
- **Explicit Materialization (`werk generate`):** Writes `<stem>_werk.go` next to `<stem>.werk` when users want inspectable Go source or CI pipelines that run plain `go build` without `werk`.

---

## 7. Single-File Standalone Scripts & Execution Cache

`.werk` scripts work identically whether placed inside a full Go repository or dropped as a standalone single file anywhere on disk (e.g., `~/bin/deploy` or `/tmp/cleanup.werk` with **no `go.mod` and no `PklProject`**).

### Project Mode vs. Standalone Single-File Mode

1. **Project Mode (`go.mod` found in parent directory tree):**
   - Lowered Go code compiles within the enclosing Go module context, sharing its `go.mod`, `go.sum`, local packages, and `replace` directives.
   - Multiple `.werk` scripts in separate directories (or the same directory, when compiled individually via `werk run foo.werk` / `werk build foo.werk`) each act as an independent entrypoint.

2. **Standalone Single-File Mode (no `go.mod` in parent tree):**
   - `werk` provisions a deterministic per-script workspace under `$XDG_CACHE_HOME/werk/workspaces/<sha256(absScriptPath)>/` containing a synthesized `go.mod` (`module werkscript` targeting the installed Go version) and the lowered `main.go` (annotated with `//line /abs/path/to/script.werk:N`).
   - **Stdlib-only scripts:** Compile immediately offline with zero network requests.
   - **Third-party Go imports:** If the script imports external Go packages (e.g., `import "golang.org/x/sync/errgroup"`), `werk` automatically resolves dependencies via `go mod tidy` inside the cached workspace and reuses the resulting `go.mod` / `go.sum` on subsequent runs.
   - **Optional inline version pinning (`//werk:dep`):** Header comments of the form `//werk:dep github.com/charmbracelet/lipgloss@v1.1.0` pin exact module versions in the synthesized `go.mod` (similar to Python PEP 723 inline script metadata).
   - **Zero-`PklProject` execution:** Because `werk` registers its `werk:` Pkl `ModuleReader` in-process via `pkl-go`, standalone scripts evaluate `config {}` and amending `.pkl` files (`werk run -c prod.pkl ./script.werk`) relative to the script's parent directory without needing a `PklProject` file.

### Content-Addressed Native Binary Cache
To make `#!/usr/bin/env werk` scripts feel instantaneous on repeat invocations:
- `werk` computes a cache key over `(werkVersion, goToolchainVersion, scriptBytes, resolvedPklValuesHash, goSumBytes)`.
- Compiled binaries are stored at `$XDG_CACHE_HOME/werk/bin/<cacheKey>`.
- On a cache hit, `werk` replaces its own process image directly with the cached native binary via `syscall.Exec(cachedBin, argv, env)`—achieving native ~1–2ms startup with `os.Args[0]` set to the script path and `os.Args[1:]` set to the caller's arguments.
