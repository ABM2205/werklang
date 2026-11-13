# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Go + Pkl hybrid script preprocessor:** Support combining standard Go scripting with a top-level `config [name] { ... }` Pkl template block in `.werk` files and `Werkfile` scripts.
- **Python-style top-level scripting (`golower`):** Synthesize `package main` and `func main()` from top-level statements while hoisting top-level `var` and `:=` bindings to package-level globals visible across helper functions, preserving `.werk` source line numbers via `//line` directives.
- **Compile-time Pkl evaluation & static Go codegen (`pklschema`):** Evaluate `config` templates and external `.pkl` overrides at compile time via `pkl:reflect` and `pkl-go` into plain Go `var` structs with deterministic `UpperFirst` field naming, inferred nested structs/slices/maps for untyped and `Dynamic` Pkl objects, and zero runtime Pkl dependency in compiled binaries.
- **`werk:` Pkl External Module Reader (`werk init` & `werk pkl-reader`):** Allow external `.pkl` files to `amends "werk:/path/to/file.werk"` directly with 1:1 line-preserving Pkl projections so `pkl eval` and Pkl editor tooling report `.werk` line numbers, hover docs, and completions without shadow files on disk.
- **Portable shebang & standalone single-file scripts:** Support direct execution (`#!/usr/bin/env werk` and `werk <script> [args...]`), content-addressed native binary caching (`syscall.Exec`), and automatic cached synthetic Go module workspaces (with `//werk:dep mod@ver` directives and `go mod tidy`) for scripts outside any `go.mod`.
- **Expanded CLI subcommands:** Add `werk init`, `werk pkl-reader`, `werk generate`, and `Werkfile` task argument forwarding (`werk check`, `werk install`, `werk changelog`, etc.), alongside updated `run`, `build`, `test`, `fmt`, and `lsp` commands (`-c`/`--config` override selection).
- **Hybrid Go + Pkl Language Server (`werk lsp`):** Multiplex virtual `.go` and `.pkl` documents across `gopls` and `pkl-lsp` with built-in `go/types` and `pkl:reflect` fallbacks for diagnostics, hover, completion, cross-language Go-to-Definition (from Go `config` field accesses to Pkl property declarations), document symbols, and formatting, plus isolated virtual packages when editing `Werkfile` alongside `main.go`.
- **v2 design documentation:** Add architecture, syntax/codegen, Pkl `werk:` amends, and editor tooling specifications under `design/001_overview.md`–`design/004_editor_tooling.md`.

### Changed
- **Tree-sitter grammar (`tree-sitter-werklang`):** Inherit directly from `tree-sitter-go` with an external C scanner (`src/scanner.c`) for `config` blocks and Pkl language injection (`queries/injections.scm`), allowing `config` to remain a valid Go identifier outside `config { ... }` headers.
- **Zed extension (`extensions/zed`):** Update extension to v0.2.0 with Go + Pkl Tree-sitter highlight, outline, runnable, and injection queries targeting the `werklang-2` grammar revision, plus automatic shebang detection (`^#!.*\bwerk\b`).
- **Self-hosted `Werkfile`:** Rewrite `Werkfile` as an executable v2 Go + Pkl script with `build`, `test`, `grammar`, `zed`, `check`, `install`, `changelog`, `clean`, and `list` tasks.

### Removed
- **Custom v1 DSL compiler & Mage runtime:** Remove the standalone v1 lexer, parser, typechecker, custom AST formatter, `target` keyword DSL, and `github.com/magefile/mage` code generator in favor of standard Go syntax + Pkl `config` blocks.

## [0.1.0] - 2026-11-12

### Added
- Initial Werklang v1 transpiler with `config` and `target` DSL blocks compiling to Magefiles.
- `go:` standard library and `pkl:` schema interop inspectors.
- Initial `werk` CLI (`run`, `build`, `fmt`, `lsp`), Tree-sitter grammar, and Zed editor extension.

[Unreleased]: https://github.com/ABM2205/werklang/compare/36bc85b...werklang-2
[0.1.0]: https://github.com/ABM2205/werklang/commit/36bc85b
