# scryer-rust-engine

A Scryer language engine for Rust projects.

## License

[Apache License 2.0](LICENSE)

## Static Rust scan

Build a local macOS package with Go and cgo, then install it into Scryer:

```sh
./scripts/build
scryer engine install rust --from "$PWD/build/install/scryer-rust-engine"
scryer project scan /path/to/project --facts dependencies,architecture,files --json
```

- `dependencies` lists Cargo manifests, declared dependencies and workspace member
  patterns. It preserves dependency tables, scopes, version requirements and target
  conditions as written.
- `architecture` lists Cargo targets and Rust module declarations. Conventional
  external module files are joined when exactly one candidate exists. A literal
  `#[path = "..."]` can locate an alternate source file.
- `files` lists Rust files, their component IDs, module references, and syntactic
  `use` expressions.
- `call-graph` records functions, closures and syntactic call sites. It can
  resolve direct module-level functions, explicit module paths and supported
  aliases, public functions in the same Cargo package library or explicit
  unconditional local path dependencies, and `self`/`Self` calls within the same
  inherent implementation. Calls without sufficient evidence keep their source
  location, `unresolved` status and `resolutionReason`.

The scan reads the checkout directly. Call graphs declare limitations such as
Cargo feature selection, macro expansion, registry or Git crate symbols, and
type-directed method or trait dispatch. Test-only attributes can propagate into
external module files when every known Cargo target route requires the guard.

## Tauri IPC

`--facts ipc` reports Rust-side Tauri command declarations, registrations,
and literal event emissions. The collector lives in `internal/framework/tauri`
and consumes the syntax trees produced by the generic Rust scanner. The
result is source evidence grouped by Tauri config path. Explicit module
paths in registrations can identify the declaration in the Cargo target's
module tree. Imported aliases, dynamic names and conditional modules can leave
the registration without declaration evidence.

## Windows named pipes

`--facts ipc` also collects Tokio Windows named-pipe endpoints in
`windows.namedPipe`. The collector is in `internal/ipc/windows/namedpipe`.
It recognizes directly chained `ClientOptions::new().open(...)` and
`ServerOptions::new().create(...)`, including explicit read/write settings
and local pipe names from literals or unique constants in source scope.
It records the source locations and normalizes access modes for the same
Core matcher used by C++. It does not build or run the target.
