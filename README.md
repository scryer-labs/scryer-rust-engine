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
  external module files are joined when exactly one candidate exists.
- `files` lists Rust files, their component IDs, module references, and syntactic
  `use` expressions.

The scan reads the checkout directly. Cargo dependency resolution, macro expansion,
conditional compilation, custom module paths, and Rust semantic resolution are
outside this slice. Tauri recognition will build on the Rust scanner in a separate
module.
