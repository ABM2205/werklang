# 003 — Amending `.werk` Templates from Pkl

## Goal

An external `.pkl` file must be able to `amends` the `config {}` template inside a `.werk` file **as if the `.werk` file were a native `.pkl` module**, with:
- Full `pkl eval` / `pkl test` CLI support.
- Full `pkl-lsp` support in editors (autocomplete of template properties, type checking, constraint validation, hover documentation from `///` doc comments, and diagnostics).
- **Zero generated shadow `.pkl` files** littering the repository.

Werklang achieves this using Pkl's **External Module Reader** protocol (`externalModuleReaders`), supported natively in Pkl 0.27+ and `github.com/apple/pkl-go`.

---

## 1. The `werk:` Module URI Scheme

Werklang registers a custom Pkl module scheme named `werk`:

```pkl
amends "werk:/main.werk"
```

or for nested packages:

```pkl
amends "werk:/cmd/server/main.werk"
```

### Reader Capabilities (`ModuleReaderSpec`)
`werk pkl-reader` advertises the following capabilities to Pkl during the external reader handshake:

| Property | Value | Meaning |
| :--- | :--- | :--- |
| `scheme` | `"werk"` | Handles `werk:...` module URIs. |
| `hasHierarchicalUris` | `true` | Supports path hierarchy, `/` resolution, and relative imports within `werk:`. Requires URIs to begin with `werk:/...`. |
| `isLocal` | `true` | Marks modules as local to the filesystem (enables Pkl `...` triple-dot imports if used). |
| `isGlobbable` | `true` | Supports `import* "werk:/**/*.werk"` by walking workspace `.werk` files that contain a `config` block. |

### Path Resolution & Sandboxing
- Paths in `werk:/<relpath>` resolve relative to the **project root** (the directory containing `PklProject`, or the target script's parent directory / current working directory when running standalone single-file scripts without a `PklProject`).
- `werk pkl-reader` canonicalizes the target path and rejects any URI that resolves outside the root directory.
- For `*.werk` paths (or shebang scripts), `werk pkl-reader` returns the 1:1 line-mapped `config {}` projection (with any line-1 `#!` shebang and Go code blanked out). For `*.pkl` paths, it returns the `.pkl` file contents verbatim—allowing relative `import "./shared.pkl"` inside a `.werk` `config {}` block to resolve naturally under the `werk:` scheme.

---

## 2. `PklProject` Configuration (`werk init`)

For both the `pkl` CLI and `pkl-lsp` to automatically spawn `werk pkl-reader` and permit the `werk:` URI scheme, the workspace root uses a standard `PklProject` file.

Running `werk init` scaffolds (or merges into an existing) `PklProject`:

```pkl
amends "pkl:Project"

evaluatorSettings {
  allowedModules {
    "pkl:"
    "repl:"
    "file:"
    "https:"
    "package:"
    "projectpackage:"
    "werk:"
  }
  externalModuleReaders {
    ["werk"] {
      executable = "werk"
      arguments {
        "pkl-reader"
      }
    }
  }
}
```

> **Note on `allowedModules`:** When `allowedModules` is specified in `PklProject`, Pkl replaces its default list rather than appending to it. `werk init` therefore preserves all six standard Pkl module schemes alongside `"werk:"`.

When `werk build` / `werk run` invokes Pkl internally via `pkl-go`, it also registers the in-process `werkModuleReader` directly via `pkl.WithModuleReader(...)`, so `werk build` works even before `werk init` is run—while `werk init` unlocks standalone `pkl eval` and `pkl-lsp` in editors.

---

## 3. 1:1 Line-Preserving Template Extraction

When `pkl` or `pkl-lsp` requests `werk:/cmd/server/main.werk` from `werk pkl-reader`, `werk` does **not** merely slice out the substring inside `config { ... }`. Instead, it produces a **line-preserving projection**:

1. Every character outside the `config [name] { ... }` body (including the `package` header, Go `import`s, the `config app {` opener, the closing `}`, and all Go functions) is replaced with spaces, **preserving every `\n` newline character verbatim**.
2. Every character inside the `config` body (properties, Pkl `import`s, `class` definitions, `typealias`es, constraints, and `///` doc comments) remains at its **exact original `(line, column)` offset**.

### Example Projection

**Input (`server.werk`):**
```go
1: import "fmt"
2:
3: config app {
4:     /// Port to listen on.
5:     port: UInt16(this > 1024) = 8080
6: }
7:
8: fmt.Println(app.Port)
```

**Output returned by `werk pkl-reader` for `werk:/server.werk`:**
```pkl
1:
2:
3:
4:     /// Port to listen on.
5:     port: UInt16(this > 1024) = 8080
6:
7:
8:
```

### Why Line Preservation Matters
- **Accurate Diagnostics:** If `prod.pkl` sets `port = 80`, Pkl's constraint violation stack trace points to `werk:/server.werk` **line 5**—the exact line of `port: UInt16(this > 1024)` in `server.werk`, not line 2 of a stripped snippet.
- **Doc Comments & Hover:** `/// Port to listen on.` stays directly attached to `port` for `pkl-lsp` hover popups and autocomplete documentation when editing `prod.pkl`.
- **Shared Logic with `werk lsp`:** The exact same line-preserving Pkl projection feeds `pkl-lsp` for `.werk` files (see [004_editor_tooling.md](004_editor_tooling.md)).

---

## 4. Implementation via `pkl-go`

`github.com/apple/pkl-go/pkl` provides first-party support for both sides of the External Module Reader protocol:

```go
// `werk pkl-reader` command implementation sketch:
client, err := pkl.NewExternalReaderClient(
    pkl.WithExternalClientModuleReader(&WerkModuleReader{RootDir: projectRoot}),
)
if err != nil {
    return err
}
return client.Run() // Speaks Pkl MessagePack protocol over stdin/stdout
```

And inside `werk build`:

```go
evaluator, err := pkl.NewProjectEvaluator(
    ctx,
    projectRootURL,
    pkl.PreconfiguredOptions,
    pkl.WithModuleReader(&WerkModuleReader{RootDir: projectRoot}),
)
```
