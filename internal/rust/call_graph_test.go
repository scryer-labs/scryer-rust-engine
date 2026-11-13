package rust

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCallGraphConnectsBinaryToOwnCargoLibrary(t *testing.T) {
	root := t.TempDir()
	for file, source := range map[string]string{
		"Cargo.toml":       "[package]\nname = \"sample\"\n[lib]\nname = \"sample_lib\"\n",
		"src/main.rs":      "fn main() { sample_lib::run(); foreign::run(); }",
		"src/lib.rs":       "pub fn run() {}\n#[cfg(test)] mod tests { #[test] fn check() { crate::run(); } }",
		"other/Cargo.toml": "[package]\nname = \"sample_lib\"\n",
		"other/src/lib.rs": "pub fn run() {}",
	} {
		file := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := Scan(context.Background(), root, []string{"call-graph"})
	if err != nil {
		t.Fatal(err)
	}
	graph := facts["call-graph"].(*callGraph)
	if len(facts) != 1 || len(graph.Calls) != 3 {
		t.Fatalf("unexpected report: %+v", graph)
	}
	symbols := map[string]functionSymbol{}
	for _, symbol := range graph.Symbols {
		symbols[symbol.ID] = symbol
	}
	for _, site := range graph.Calls {
		switch site.Callee {
		case "sample_lib::run":
			if site.Resolution != "resolved" || symbols[site.CallerID].Name != "main" ||
				symbols[site.TargetID].Definition.File != "src/lib.rs" || site.Site.File != "src/main.rs" {
				t.Fatalf("missing Cargo library edge: %+v", site)
			}
		case "foreign::run":
			if site.TargetID != "" || site.Resolution != "unresolved" {
				t.Fatalf("matched foreign crate by function name: %+v", site)
			}
		case "crate::run":
			if site.TestContext == nil || site.TestContext.Scope != "test-only" {
				t.Fatalf("lost test context: %+v", site)
			}
		default:
			t.Fatalf("unexpected call: %+v", site)
		}
	}
}
