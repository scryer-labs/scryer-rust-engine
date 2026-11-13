package namedpipe

import (
	"context"
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
	grammar "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

func TestCollectTokioWindowsPipeAccess(t *testing.T) {
	source := []byte(`
use tokio::net::windows::named_pipe::{ClientOptions as Client, ServerOptions};
const PIPE: &str = r#"\\.\pipe\demo"#;
fn run() {
    Client::new().open(PIPE);
    Client::new().read(false).open(PIPE);
    ServerOptions::new().access_outbound(false).create(PIPE);
    Client::new().read(dynamic()).open(PIPE);
    Client::new().write(false).write(true).open(PIPE);
    unrelated::ClientOptions::new().open(PIPE);
    Client::new().open(dynamic_name());
}
fn shadow(PIPE: &str) { Client::new().open(PIPE); }
fn local() { let PIPE = dynamic_name(); Client::new().open(PIPE); }
`)
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(grammar.Language())); err != nil {
		t.Fatal(err)
	}
	tree := parser.Parse(source, nil)
	defer tree.Close()
	var collector Collector
	if err := collector.Observe(context.Background(), "src/lib.rs", tree.RootNode(), source); err != nil {
		t.Fatal(err)
	}
	want := []struct{ role, mode string }{
		{"client", "read-write"}, {"client", "write"}, {"server", "read"},
		{"client", "unknown"}, {"client", "read-write"},
	}
	if len(collector.Endpoints) != len(want) {
		t.Fatalf("unexpected endpoints: %+v", collector.Endpoints)
	}
	for i, endpoint := range collector.Endpoints {
		if endpoint.Name != `\\.\pipe\demo` || endpoint.Role != want[i].role ||
			endpoint.AccessMode != want[i].mode || len(endpoint.Evidence) != 2 ||
			endpoint.Evidence[0].Line != 3 || endpoint.Evidence[1].File != "src/lib.rs" {
			t.Fatalf("unexpected endpoint %d: %+v", i, endpoint)
		}
	}
}
