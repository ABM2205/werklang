# Imports & Ecosystem Interop: Pkl, Mage, and Go (v0.1)

## 1. Overview

Because Werklang transpiles to Go Magefiles (`//go:build mage`), it can interoperate natively with three ecosystems without bloating the core language:

1. **Werklang & Apple Pkl Modules (`.werk`, `.pkl`, `package://...`)** — imported as typed structs/configuration schemas.
2. **Mage Target Libraries (`mage:...`)** — imported as reusable build targets and DAG dependencies via Mage's `// mage:import` mechanism.
3. **Go Standard Library & Modules (`go:...`)** — imported for rich functions (`strings`, `path/filepath`, `os`, third-party packages) with automatic `(T, error)` unwrapping and primitive type bridging.

---

## 2. Unified `import` Syntax

To avoid adding extra reserved keywords (keeping `go` and `mage` free as normal identifiers/target names), imports use URI schemes modeled after Apple Pkl:

```werk
// 1. Local Werklang module (struct schema + defaults)
import "./deploy/service.werk" as Service

// 2. Apple Pkl module (local .pkl file, https://, or package:// URI)
import "./config/app.pkl" as AppConfig
import "package://pkg.pkl-lang.org/pkl-k8s/k8s@1.0.0#/Deployment.pkl" as Deployment

// 3. Shared Mage target package
import "mage:github.com/acme/buildtools/docker" as docker

// 4. Go standard library & third-party Go packages
import "go:strings"
import "go:path/filepath"
import "go:os"
import "go:github.com/Masterminds/semver/v3" as semver
```

### Aliasing Rules
- If `as <alias>` is omitted, the default identifier is the basename without extension:
  - `"go:path/filepath"` -> `filepath`
  - `"./config/app.pkl"` -> `app`
  - `"mage:github.com/acme/buildtools/docker"` -> `docker`

---

## 3. Importing Apple Pkl Modules (`.pkl`)

Werklang uses Apple's official Go bindings (`github.com/apple/pkl-go/pkl`) so existing `.pkl` configurations and schemas work seamlessly inside `.werk` scripts and `Werkfile`s.

### 3.1 How It Transpiles
1. **Schema Introspection & Go Struct Codegen**:
   - At transpile time, Werklang inspects the `.pkl` module's evaluated schema (via `pkl-go` / `pkl:reflect`) and generates a strongly-typed Go `struct` in the build cache.
2. **Runtime Evaluation**:
   - Generated initialization code uses `pkl.NewEvaluator` (`evaluator.EvaluateModule`) to evaluate the `.pkl` module into the generated Go struct before targets run.
3. **Amending in Werklang**:
   - Once imported, a `.pkl` module behaves identically to a `.werk` struct—fields are accessed with `.` and instances can be copied/amended with `{ ... }` blocks:

```werk
import "./config/app.pkl" as app

// Read evaluated Pkl fields directly
print("Default port: \(app.port)")

// Create an amended copy in Werklang
canary = app {
    port = 9090
    debug = true
}
```

### 3.2 Pkl-to-Werklang Type Mapping

| Apple Pkl Type | Werklang Type | Notes |
| :--- | :--- | :--- |
| `Int`, `Int8`..`Int64`, `UInt*` | `int` | Bridged to `int` |
| `Float` | `float` | 64-bit float |
| `String` | `string` | UTF-8 string |
| `Boolean` | `bool` | Strict boolean |
| `Listing<T>`, `List<T>`, `Set<T>` | `[]T` | Homogeneous slice |
| `Duration` | `int` | Nanoseconds (`int`), matching Go `time.Duration` |
| `DataSize` | `int` | Bytes (`int`) |
| `Module` / `Class` | Module struct | Nested generated Go struct |
| `Map<K, V>` / `Nullable<T>` | *Compile error if accessed* | Outside Werklang's minimal type subset unless projected in `.pkl` first |

---

## 4. Importing Mage Target Packages (`mage:...`)

Organizations with existing Go Magefiles or shared target libraries can import them directly into a `Werkfile`.

### 4.1 Target Re-export & CLI Namespacing
```werk
import "mage:github.com/acme/buildtools/docker" as docker
import "mage:github.com/acme/buildtools/k8s" as k8s
```
Transpiles to Mage's native import directive in `magefile_gen.go`:
```go
// mage:import docker
_ "github.com/acme/buildtools/docker"
// mage:import k8s
_ "github.com/acme/buildtools/k8s"
```
- Running `werk -l` automatically lists `docker:build`, `docker:push`, `k8s:deploy`, etc. alongside local `.werk` targets.
- Running `werk docker:build` invokes the imported Mage target directly.

### 4.2 Referencing Imported Targets in `deps` & Bodies
Imported Mage targets integrate directly into `deps` (`mg.SerialDeps`), `parallel_deps` (`mg.Deps`), and parameterized target invocations (`mg.F`):

```werk
import "mage:github.com/acme/buildtools/docker" as docker

target release {
    desc = "Test locally, then build and push container"
    deps = ["test", "docker:build"]

    // Parameterized Mage target: func Tag(version string) error
    // Compiles to: mg.SerialDeps(mg.F(docker.Tag, "v1.2.0"))
    docker.Tag("v1.2.0")
    docker.Push()
}
```
*(Note: Case mapping from `.werk` to exported Go target names is resolved automatically via Go package type inspection—both `docker:build` and `docker.Build` resolve to `docker.Build`.)*

---

## 5. Importing Go Libraries (`go:...`)

Go's standard library and ecosystem replace hundreds of brittle shell utilities (`sed`, `awk`, `jq`, `basename`, `realpath`, `sha256sum`) with cross-platform, type-checked calls.

Because Werklang intentionally omits pointers, `error` values, `[]byte`, and sized integers (`int32`/`int64`), the transpiler uses `golang.org/x/tools/go/packages` (`go/types`) to inspect imported Go signatures and generate **zero-boilerplate glue**.

### 5.1 Automatic Error Unwrapping
Werklang scripts fail fast (like `set -e` in shell, but structured):
- **`func(...) error`**: Automatically wrapped in `if err := pkg.Fn(...); err != nil { return err }`.
- **`func(...) (T, error)`**: Automatically unwraps `T` into the expression result and returns `err` immediately if non-nil.
- **`func(...) (T, bool)`**: If used in a value context where failing is unexpected, or checkable if bool-returning functions (`strings.CutPrefix`, etc.) are supported via simple helpers. *(Single-return and `(T, error)` functions are supported in v0.1; multi-value non-error returns require a thin helper or future tuple support.)*

#### Example
```werk
import "go:os"
import "go:strings"
import "go:path/filepath"

target version {
    // os.ReadFile returns ([]byte, error) in Go!
    // Werklang auto-unwraps error AND bridges []byte -> string:
    raw = os.ReadFile("VERSION")
    ver = strings.TrimSpace(raw)
    abs = filepath.Abs("bin/app-\(ver)")

    print("Artifact path: \(abs)")
}
```

Generated Go inside `func Version() error`:
```go
_tmp1, err := os.ReadFile("VERSION")
if err != nil {
	return err
}
raw := string(_tmp1)
ver := strings.TrimSpace(raw)
_tmp2, err := filepath.Abs(fmt.Sprintf("bin/app-%s", ver))
if err != nil {
	return err
}
abs := _tmp2
fmt.Println(fmt.Sprintf("Artifact path: %s", abs))
```

### 5.2 Go Boundary Type Bridging
To keep Werklang's 4 primitive types (`int`, `float`, `string`, `bool`) + slices (`[]T`) clean when calling Go functions:

| Go Parameter / Return Type | Werklang Type | Auto-Generated Conversion |
| :--- | :--- | :--- |
| `int`, `int8`..`int64`, `uint*` | `int` | `int(...)` / `int64(...)` cast |
| `float32`, `float64` | `float` | `float64(...)` / `float32(...)` cast |
| `string` | `string` | Direct |
| `[]byte` | `string` | `string(b)` on return; `[]byte(s)` when passed to Go |
| `bool` | `bool` | Direct |
| `[]T` | `[]T` | Direct (or element-converted slice helper if `[]int64` etc.) |
| `struct` / `*struct` | Read-only struct fields | Direct `.Field` access in `.werk` (auto-dereferencing non-nil struct pointers; nil check before access) |

---

## 6. Dependency Resolution & Caching (`go.mod` Handling)

Users should **not** have to manually manage a `go.mod` file just to run a `.werk` script in a non-Go repository:

1. **Stdlib-only `.werk` files**: Uses a pre-warmed cached module in `~/.cache/werk/` containing `github.com/magefile/mage`—zero network calls after first run.
2. **Inside an existing Go repo (`./go.mod` exists)**: Synthesizes the temporary Mage build workspace alongside or extending the workspace modules, plus any extra third-party `go:...` or `mage:...` imports.
3. **Standalone scripts / non-Go repos with third-party imports**:
   - Supports optional inline version specifiers in imports:
     ```werk
     import "go:github.com/Masterminds/semver/v3@v3.2.1" as semver
     import "mage:github.com/acme/buildtools/docker@v1.4.0" as docker
     ```
   - `werk` hashes the import set, generates `.werk/cache/<hash>/go.mod`, runs `go mod download` once, and reuses the compiled Mage binary on subsequent runs.

---

## 7. End-to-End Polyglot Example (`Werkfile`)

```werk
import "./config/release.pkl" as cfg
import "mage:github.com/acme/ci/docker" as docker
import "go:os"
import "go:strings"
import "go:path/filepath"

target verify {
    desc = "Validate release version file matches Pkl config"
    file_ver = strings.TrimSpace(os.ReadFile("VERSION"))
    if file_ver != cfg.version {
        fail("VERSION (\(file_ver)) does not match release.pkl (\(cfg.version))")
    }
}

target package {
    desc    = "Compile binary and build container"
    deps    = ["verify", "docker:login"]
    outputs = [filepath.Join("dist", cfg.app_name)]

    exec("go", ["build", "-o", filepath.Join("dist", cfg.app_name), "./cmd/app"])
    docker.BuildImage("\(cfg.registry)/\(cfg.app_name):\(cfg.version)")
}
```
