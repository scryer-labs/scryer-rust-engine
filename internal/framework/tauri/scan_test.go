package tauri

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/scryer-labs/scryer-rust-engine/internal/rust"
	sitter "github.com/tree-sitter/go-tree-sitter"
	grammar "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

func TestCollectTauriEndpointsWithSourceEvidence(t *testing.T) {
	source := []byte(`
use tauri::{AppHandle, Emitter};

#[tauri::command]
fn select() {}
fn setup() {
    tauri::Builder::default().invoke_handler(tauri::generate_handler![select, commands::other]);
}
fn render(app: &AppHandle) {
    app.emit("render_update", ());
    app.emit(dynamic_name(), ());
}
fn unrelated(app: &Logger) { app.emit("not_tauri", ()); }
`)
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(grammar.Language())); err != nil {
		t.Fatal(err)
	}
	tree := parser.Parse(source, nil)
	defer tree.Close()
	collector := New(fstest.MapFS{"app/tauri.conf.json": {Data: []byte("{}")}})
	for _, file := range []string{"app/src/lib.rs", "other/src/lib.rs"} {
		if err := collector.Observe(context.Background(), file, tree.RootNode(), source); err != nil {
			t.Fatal(err)
		}
	}
	if len(collector.apps) != 1 {
		t.Fatalf("unexpected apps: %+v", collector.apps)
	}
	app := collector.apps[0]
	if app.Config != "app/tauri.conf.json" || len(app.Endpoints) != 4 {
		t.Fatalf("unexpected app facts: %+v", app)
	}
	want := []struct{ kind, role, name, symbol string }{
		{"command", "declaration", "select", "select"},
		{"command", "registration", "select", "select"},
		{"command", "registration", "other", "commands::other"},
		{"event", "emit", "render_update", ""},
	}
	for i, endpoint := range app.Endpoints {
		expected := want[i]
		if endpoint.Kind != expected.kind || endpoint.Role != expected.role || endpoint.Name != expected.name ||
			endpoint.Symbol != expected.symbol || endpoint.File != "app/src/lib.rs" || endpoint.Line == 0 {
			t.Fatalf("unexpected endpoint: %+v", endpoint)
		}
	}
}

func TestResolveModuleQualifiedRegistration(t *testing.T) {
	root := t.TempDir()
	for file, contents := range map[string]string{
		"Cargo.toml":      "[package]\nname='fixture'\n",
		"tauri.conf.json": "{}",
		"src/lib.rs": `mod commands;
fn setup() {
	tauri::Builder::default().invoke_handler(tauri::generate_handler![crate::commands::save]);
}`,
		"src/commands.rs": `#[tauri::command]
pub fn save() {}`,
	} {
		target := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	collector := New(os.DirFS(root))
	result, err := rust.Scan(context.Background(), root, []string{"architecture"}, collector.Observe)
	if err != nil {
		t.Fatal(err)
	}
	architecture := result["architecture"].(map[string]any)
	collector.ResolveRegistrations(architecture["modules"].([]rust.Module), architecture["components"].([]rust.Target))
	endpoints := collector.apps[0].Endpoints
	if len(endpoints) != 2 || endpoints[1].Declaration == nil || endpoints[1].Declaration.File != "src/commands.rs" {
		t.Fatalf("missing declaration location: %+v", endpoints)
	}
}
