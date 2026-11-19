# scryer-rust-engine

A Scryer language engine for Rust projects.

## License

[Apache License 2.0](LICENSE)

## Install and scan

With a current Scryer CLI, install or upgrade the official macOS arm64 or
Windows amd64 package:

```sh
scryer engine install rust
scryer engine upgrade rust
scryer project scan /path/to/project --facts dependencies,architecture,files,call-graph,ipc --json --compact
```

For engine development, build with the Go version in `go.mod`, cgo and a C
compiler. Install the resulting local package into Scryer:

```sh
./scripts/build
scryer engine install rust --from "$PWD/build/install/scryer-rust-engine"
```

## Static facts

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

`--facts dependencies --resolve` adds Cargo's effective dependency graph
under `dependencies.resolution`. It uses `cargo metadata --frozen` for the
host target, without downloading packages or updating the lockfile. The
declared dependency facts remain available for manifests that cannot be
resolved offline. `--facts files,call-graph --resolve` also runs
`cargo check --frozen --tests` with a temporary target directory. It validates
build selection without executing tests and records `basis: "cargo-check"` and
`bindingBasis: "source-candidates"`. Rust symbol and method resolution remain source-based.

## Coverage and change impact

With a current Core CLI and this engine:

```sh
scryer project analyze --operations coverage -o coverage.html
scryer project analyze --resolve --operations impact,coverage -o impact.html
scryer project analyze --json --compact
```

Impact defaults to static scan evidence; `--resolve` also runs native tests and
collects after-snapshot coverage. Failed dynamic resolution/tests/coverage stop
analysis without static fallback. Core owns snapshot comparison, traversal and
report rendering; the engine supplies shared scan projections and normalized
JSON coverage. Multi-engine projects use the same command; CMake target selectors
belong to C++ and are not interpreted here.

Requires Cargo/rustc, a frozen dependency set and LLVM coverage tools matching
rustc's LLVM major. `SCRYER_RUST_LLVM_BIN` selects their directory. Cargo tests
run with coverage instrumentation and temporary target/profile directories;
LLVM JSON imports are also accepted. Doctests and per-test attribution are not
collected. Impact reuses scan's source graph; `--resolve` adds Cargo check for
both snapshots, not rustc type-directed binding. Candidate edges remain explicit.

Native runtime acceptance currently targets macOS. Windows package builds and
ABI smoke checks do not establish equivalent runtime toolchain acceptance.
Coverage is aggregate execution evidence, not assertion effectiveness or a
causal test-to-caller path. The common JSON contract is maintained in Scryer
Core's `docs/contracts/COVERAGE.md` and `docs/contracts/IMPACT.md`.

## Known gaps

- Cargo validation is not rustc semantic binding. Trait/dynamic dispatch,
  macro expansion, registry/Git-crate targets and generated or inactive cfg code
  can remain unresolved or source candidates.
- Named-pipe detection currently covers the documented direct Tokio patterns;
  arbitrary wrappers, dynamic channel values and shared-memory APIs are not
  implemented. Tauri has the registration/data-flow limits described above.
- Runtime coverage excludes doctests and individual-test attribution. Test
  effectiveness, code-smell rules and ownership/lifetime analysis are not implemented.
- Custom feature/target selection and full Windows runtime acceptance need
  further work; package availability does not imply those capabilities.

## Release verification

The [GitHub workflow](.github/workflows/ci.yml) builds the platform packages
and exercises scan, static impact and coverage import through the ABI before publishing a version tag.
