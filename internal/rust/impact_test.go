package rust

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestImpactRetainsRustTestContextAndStableTokenKeys(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Cargo.toml"), []byte("[package]\nname='impact_fixture'\nversion='0.1.0'\nedition='2021'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := "pub fn value() -> i32 { 1 }\n#[cfg(test)] mod tests { #[test] fn case() { let _x=super::value(); } }\n"
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "src/lib.rs"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(source)
	before, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	write("// offset\n" + source)
	after, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Symbols) != 2 || len(after.Calls) != 1 || after.Calls[0].Candidate == "" {
		t.Fatal("missing Rust scan graph")
	}
	if before.Symbols[0].Key != after.Symbols[0].Key || before.Symbols[0].Fingerprint != after.Symbols[0].Fingerprint {
		t.Fatal("comments changed correspondence")
	}
	if after.Symbols[1].Role != "TEST" || len(after.Symbols[1].TestContext) == 0 {
		t.Fatal("test-only language evidence was lost")
	}
}
