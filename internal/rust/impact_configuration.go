package rust

import (
	"io/fs"
	"os"
	"path"

	"github.com/scryer-labs/scryer-rust-engine/internal/analysis"
)

func appendConfigurationContexts(root string, result *analysis.ImpactSnapshot) error {
	fsys := os.DirFS(root)
	err := fs.WalkDir(fsys, ".", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".orrery", ".scryer", "target", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		name := entry.Name()
		selected := name == "Cargo.toml" || name == "Cargo.lock" || name == "rust-toolchain" || name == "rust-toolchain.toml" || path.Base(path.Dir(file)) == ".cargo" && (name == "config" || name == "config.toml")
		if !selected {
			return nil
		}
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			return err
		}
		result.Contexts = append(result.Contexts, analysis.ImpactContext{Path: file, Fingerprint: analysis.Hash(data)})
		return nil
	})
	result.Notes = append(result.Notes, "Cargo manifests/locks, Rust toolchain and .cargo configuration are comparison contexts. A context change is unassessed impact, not proof of unchanged call targets; cfg/features and generated/macro code remain incomplete.")
	return err
}
