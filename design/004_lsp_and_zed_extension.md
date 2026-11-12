# Language Server (`werk lsp`) & Zed Editor Extension (v0.1)

## 1. Overview

First-class editor tooling makes Werklang feel like a modern, safe replacement for Makefiles and shell scripts—catching bad target names, missing Pkl fields, or mistyped Go stdlib calls before a command ever runs.

Tooling consists of three tightly integrated components:
1. **`werk lsp`**: Built directly into the `werk` Go binary (communicating over `stdio` via JSON-RPC 2.0), reusing the exact same lexer, parser, and typechecker as the transpiler.
2. **`tree-sitter-werklang`**: A fast, incremental Tree-sitter grammar providing instant syntax highlighting, `\(...)` string interpolation highlighting, embedded Bash highlighting inside `sh(...)`, code folding, and runnable target detection.
3. **Zed Extension (`extensions/zed`)**: A native Zed extension (Rust -> WebAssembly via `zed_extension_api`) wiring together `tree-sitter-werklang`, `werk lsp`, format-on-save (`werk fmt`), and gutter **▶ Run** buttons for targets.

---

## 2. Compiler Frontend Requirements for LSP

To allow the CLI transpiler and `werk lsp` to share 100% of `internal/lexer`, `internal/parser`, and `internal/types`:

1. **Source Spans on All Tokens & AST Nodes**:
   - Every `Token` and AST node carries a `Span { File, StartPos, EndPos }` with 0-indexed line, UTF-8 byte offset, and UTF-16 column (required by LSP).
2. **Zero `panic`s — Error-Recovering Frontend**:
   - Instead of panicking on unexpected input, the lexer emits `TokIllegal` tokens and the parser records structured `Diagnostic` values, synchronizing at newline / `}` / top-level keyword boundaries (`import`, `amends`, `config`, `target`).
   - This ensures an incomplete line like `api = Service { ` still produces a valid partial AST for field autocompletion in Zed.
3. **In-Memory Overlay Workspace**:
   - `werk lsp` maintains an in-memory file overlay for open buffers (`textDocument/didOpen`, `didChange`, `didClose`) while caching expensive introspection of imported `go:...`, `mage:...`, and `.pkl` schemas on disk/in memory.

---

## 3. `werk lsp` Capabilities

Invoked via `werk lsp` (stdio transport).

| LSP Feature | Method | Behavior in Werklang |
| :--- | :--- | :--- |
| **Diagnostics** | `publishDiagnostics` | Syntax errors, type mismatches (`port int = "abc"`), missing required module fields, unknown or cyclic `deps` / `parallel_deps` targets, invalid `go:` / `mage:` / `.pkl` imports. |
| **Completion** | `textDocument/completion` | • Module fields inside `Service { <cursor> }` and dot access (`app.<cursor>`)<br>• Target names inside `deps = ["<cursor>"]` (both local and `mage:...` targets)<br>• Exported Go funcs/fields on `go:...` aliases (`filepath.<cursor>`)<br>• Built-ins (`sh`, `exec`, `capture`, `try_sh`, `env`, `glob`, `exists`, `rm`, `mkdir`, `print`, `fail`) & target metadata (`desc`, `deps`, `parallel_deps`, `inputs`, `outputs`). |
| **Hover** | `textDocument/hover` | • Variable/field resolved type & default value<br>• `target` description (`desc`), dependencies, and inputs/outputs summary<br>• Imported Go function doc comment + bridged Werklang signature (showing unwrapped `(T, error)` -> `T`)<br>• Imported `.pkl` property type & doc comment. |
| **Go to Definition** | `textDocument/definition` | • Jump from `deps = ["test"]` -> `target test`<br>• Jump from `import "./service.werk"` or field override -> definition in `.werk` / `.pkl` / Go source file. |
| **Find References** | `textDocument/references` | Find all usages of a module, variable, config field, or `target` across the workspace. |
| **Document Symbols** | `textDocument/documentSymbol` | Structured symbol tree for `config` blocks, `target` blocks, imports, and module fields. |
| **Formatting** | `textDocument/formatting` | Canonical `gofmt`-style formatting (same engine as `werk fmt`), aligning `=` in contiguous field/target metadata blocks if desired. |

---

## 4. Tree-sitter Grammar (`tree-sitter-werklang`)

Zed uses Tree-sitter (rather than TextMate grammars) for all syntactic editor features. Keeping Werklang's grammar small (17 keywords, LL-friendly structure) makes `grammar.js` straightforward:

### Core Node Types in `grammar.js`
- `source_file` -> `(shebang)? (amends_clause)? (import_decl | config_decl | target_decl | field_decl | statement)*`
- `amends_clause` -> `'amends' string_literal`
- `import_decl` -> `'import' string_literal ('as' identifier)?`
- `config_decl` -> `'config' identifier block`
- `target_decl` -> `'target' identifier block`
- `field_decl` -> `identifier (type_annotation)? ('=' expression)?`
- `type_annotation` -> `primitive_type | slice_type | identifier`
- `string_literal` -> `'"' (string_content | escape_sequence | interpolation)* '"'`
  - `interpolation` -> `'\(' expression ')'`
- `if_expr` (`'if' expression 'then' expression 'else' expression`) vs. `if_stmt` (`'if' expression block ('else' (if_stmt | block))?`)
- `for_stmt` -> `'for' identifier 'in' expression block`
- `struct_amend_expr` -> `expression '{' (field_decl)* '}'`

---

## 5. Zed Extension Architecture

Repository layout (can live in `extensions/zed/` during development and `tree-sitter-werklang/` for the grammar):

```text
extensions/zed/
├── extension.toml                 # Zed extension manifest
├── Cargo.toml                     # Rust -> wasm32-wasip1 crate using zed_extension_api
├── src/
│   └── lib.rs                     # Launches `werk lsp`
└── languages/
    └── werklang/
        ├── config.toml            # File associations (.werk, Werkfile, #!/usr/bin/env werk)
        ├── highlights.scm         # Syntax highlighting rules
        ├── injections.scm         # Bash highlighting inside sh("...") / capture("...")!
        ├── brackets.scm           # Bracket matching ({}, [], (), "", \())
        ├── indents.scm            # Auto-indentation rules
        ├── outline.scm            # Symbol outline & breadcrumbs
        └── runnables.scm          # Gutter ▶ Run buttons for `target <name>`
```

### 5.1 File Detection (`languages/werklang/config.toml`)
Matches `*.werk`, `Werkfile`, and extensionless scripts starting with a `werk` shebang:

```toml
name = "Werklang"
grammar = "werklang"
path_suffixes = ["werk", "Werkfile"]
first_line_pattern = "^#!.*\\bwerk\\b"
line_comments = ["// "]
autoclose_before = ";:.,=}])>"
brackets = [
    { start = "{", end = "}", close = true, newline = true },
    { start = "[", end = "]", close = true, newline = true },
    { start = "(", end = ")", close = true, newline = false },
    { start = "\"", end = "\"", close = true, newline = false, not_in = ["string", "comment"] },
]
```

### 5.2 Syntax Highlighting (`languages/werklang/highlights.scm`)
Maps Werklang constructs onto Zed's standard theme capture names:

```scheme
; Comments
(comment) @comment
(shebang) @comment.doc

; Keywords
[
  "amends"
  "import"
  "as"
  "config"
  "target"
] @keyword

[
  "if"
  "then"
  "else"
  "for"
  "in"
] @keyword.control

; Types
(primitive_type) @type.builtin          ; int, float, string, bool
(type_annotation (identifier) @type)    ; Imported .werk / .pkl module types

; Declarations
(target_decl name: (identifier) @function.definition)
(config_decl name: (identifier) @type.definition)
(field_decl name: (identifier) @property)
(import_decl alias: (identifier) @namespace)

; Built-in calls
((call_expr
  function: (identifier) @function.builtin)
  (#any-of? @function.builtin
    "sh" "exec" "capture" "try_sh" "rm" "mkdir" "env" "glob" "exists" "print" "fail"))

; General calls & member access
(call_expr function: (selector_expr field: (identifier) @function.method))
(selector_expr field: (identifier) @property)
(struct_amend_expr type: (identifier) @type)

; Literals & String Interpolation
(string_literal) @string
(escape_sequence) @string.escape
(interpolation "\\(" @punctuation.special ")" @punctuation.special) @embedded
(int_literal) @number
(float_literal) @number
(bool_literal) @boolean
```

### 5.3 Shell Language Injection (`languages/werklang/injections.scm`)
One of Zed's nicest Tree-sitter capabilities: commands inside `sh("...")`, `capture("...")`, and `try_sh("...")` automatically receive **Bash syntax highlighting**:

```scheme
((call_expr
  function: (identifier) @_fn
  arguments: (argument_list (string_literal (string_content) @injection.content)))
  (#any-of? @_fn "sh" "capture" "try_sh")
  (#set! injection.language "bash"))
```

### 5.4 Gutter Play Buttons (`languages/werklang/runnables.scm`)
Renders clickable **▶** buttons in Zed's gutter next to every `target <name> { ... }`, invoking `werk <name>`:

```scheme
(target_decl
  "target" @run
  name: (identifier) @_werk_target
  (#set! tag werk-target))
```

### 5.5 Rust WASM Extension Glue (`src/lib.rs`)
Minimal Rust glue locating `werk` on `$PATH` (or via Zed's `lsp.werklang-lsp.binary` setting) and launching `werk lsp`:

```rust
use zed_extension_api::{self as zed, LanguageServerId, Result};

struct WerklangExtension;

impl zed::Extension for WerklangExtension {
    fn new() -> Self {
        Self
    }

    fn language_server_command(
        &mut self,
        _language_server_id: &LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<zed::Command> {
        let path = worktree
            .which("werk")
            .ok_or_else(|| "werk binary not found on PATH; install werklang first".to_string())?;

        Ok(zed::Command {
            command: path,
            args: vec!["lsp".to_string()],
            env: worktree.shell_env(),
        })
    }
}

zed::register_extension!(WerklangExtension);
```

---

## 6. Phased Rollout Plan for Editor Tooling

1. **Milestone 1 — Instant Zed Highlighting (Dev Extension)**:
   - Write `tree-sitter-werklang/grammar.js` and `extensions/zed/languages/werklang/*.scm`.
   - Load immediately in Zed via **`zed: install dev extension`** -> select `extensions/zed` (highlights `.werk` and `Werkfile`, Bash injections, outline, and bracket matching even before the LSP is complete).
2. **Milestone 2 — Core `werk lsp` (Diagnostics, Hover, Go-to-Def, Formatting)**:
   - Build `internal/lsp` in Go (`stdio` JSON-RPC) hooked into the error-recovering lexer/parser/typechecker + `werk fmt`.
3. **Milestone 3 — Polyglot Autocompletion**:
   - Wire `go/types`, `mage:...` target discovery, and `.pkl` schema caches into `textDocument/completion` and `textDocument/hover`.
