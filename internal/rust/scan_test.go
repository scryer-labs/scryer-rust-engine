package rust

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestScanCargoTargetsAndModuleOwnership(t *testing.T) {
	root := t.TempDir()
	for file, contents := range map[string]string{
		"app/Cargo.toml": `[package]
name = "sample-app"
[dependencies]
local = { package = "renamed", path = "../shared" }
[target.'cfg(unix)'.dependencies]
libc = "0.2"
[dev-dependencies]
tempfile = "3"
`,
		"app/src/main.rs":          "mod server; use server::start;",
		"app/src/server.rs":        "mod routes; pub fn start() {}",
		"app/src/server/routes.rs": "pub fn run() {}",
		"app/target/generated.rs":  "mod unwanted;",
	} {
		target := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := Scan(context.Background(), root, []string{"dependencies", "architecture", "files"})
	if err != nil {
		t.Fatal(err)
	}
	manifest := facts["dependencies"].(map[string]any)["manifests"].([]Manifest)[0]
	for _, want := range []struct{ name, scope, condition string }{
		{"local", "normal", ""}, {"tempfile", "dev", ""}, {"libc", "normal", "cfg(unix)"},
	} {
		if !slices.ContainsFunc(manifest.Dependencies, func(dep Dependency) bool {
			return dep.Name == want.name && dep.Scope == want.scope && dep.Condition == want.condition
		}) {
			t.Fatalf("missing dependency %+v in %+v", want, manifest.Dependencies)
		}
	}
	if len(manifest.Targets) != 1 || manifest.Targets[0].Name != "sample-app" {
		t.Fatalf("unexpected targets: %+v", manifest.Targets)
	}
	files := facts["files"].(map[string]any)["files"].([]SourceFile)
	if len(files) != 3 {
		t.Fatalf("unexpected files: %+v", files)
	}
	for _, file := range files {
		if !slices.Equal(file.ComponentIDs, []string{manifest.Targets[0].ID}) {
			t.Fatalf("missing target ownership for %+v", file)
		}
	}
}
