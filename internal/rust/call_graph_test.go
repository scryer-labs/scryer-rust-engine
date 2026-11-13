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
		"src/main.rs":      "fn main() { sample_lib::run(); sample_lib::api::route(); foreign::run(); }",
		"src/lib.rs":       "pub fn run() {}\npub mod api { pub fn route() {} }\n#[cfg(test)] mod tests { #[test] fn check() { crate::run(); } }",
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
	if len(facts) != 1 || len(graph.Calls) != 4 {
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
		case "sample_lib::api::route":
			if site.Resolution != "resolved" || symbols[site.TargetID].Name != "route" {
				t.Fatalf("missing public library module edge: %+v", site)
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

func TestCallGraphResolvesModulePathsAndAliases(t *testing.T) {
	root := t.TempDir()
	for file, source := range map[string]string{
		"Cargo.toml": "[package]\nname='demo'\nedition='2021'\n",
		"src/lib.rs": `mod sub;
pub use crate::sub::work as exposed;
fn root() {}
fn start() {
    root();
    crate::sub::work();
    exposed();
    let indirect = || {};
    indirect();
}
fn shadow(root: fn()) { root(); }
#[cfg(unix)]
fn conditional() {}
fn check() { conditional(); }
`,
		"src/sub.rs": `use super::root as parent;
pub fn work() { parent(); self::helper(); }
fn helper() { crate::root(); }
`,
	} {
		target := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := Scan(context.Background(), root, []string{"call-graph"})
	if err != nil {
		t.Fatal(err)
	}
	graph := report["call-graph"].(*callGraph)
	names := map[string]string{}
	for _, fn := range graph.Symbols {
		names[fn.ID] = fn.Name
	}
	want := []string{"root", "work", "work", "", "", "", "root", "helper", "root"}
	if len(graph.Calls) != len(want) {
		t.Fatalf("calls: %+v", graph.Calls)
	}
	for i, call := range graph.Calls {
		if got := names[call.TargetID]; got != want[i] {
			t.Errorf("%s resolved to %q, want %q: %+v", call.Callee, got, want[i], call)
		}
	}
}

func TestCallGraphUsesExplicitLocalCargoDependency(t *testing.T) {
	root := t.TempDir()
	for file, source := range map[string]string{
		"Cargo.toml": `[package]
name = "sample"
edition = "2021"
[dependencies]
alias = { package = "helper", path = "helper" }
optional = { package = "helper", path = "helper", optional = true }
`,
		"src/lib.rs": `use alias::work as do_work;
fn run() { do_work(); alias::work(); alias::private(); optional::work(); }
struct Worker;
impl Worker {
    fn new() -> Self { Self }
    fn step(&self) {}
    fn run(&self) { Self::new(); self.step(); }
}
`,
		"helper/Cargo.toml": "[package]\nname='helper'\n",
		"helper/src/lib.rs": "pub fn work() {}\nfn private() {}",
	} {
		target := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := Scan(context.Background(), root, []string{"call-graph"})
	if err != nil {
		t.Fatal(err)
	}
	calls := report["call-graph"].(*callGraph).Calls
	if len(calls) != 6 {
		t.Fatalf("calls: %+v", calls)
	}
	for i, call := range calls {
		if i < 2 && (call.Resolution != "resolved" || call.TargetID != "helper/src/lib.rs:0") ||
			i >= 2 && i < 4 && (call.Resolution != "unresolved" || call.Reason == "") ||
			i >= 4 && (call.Resolution != "resolved" || call.TargetID == "") {
			t.Errorf("unexpected call %+v", call)
		}
	}
}
