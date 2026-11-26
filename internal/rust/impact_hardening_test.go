package rust

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scryer-labs/scryer-rust-engine/internal/analysis"
)

func rustFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "space 项目")
	for file, source := range files {
		target := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func Test_should_retain_closure_impact_and_stable_keys_when_comments_and_lines_move(t *testing.T) {
	source := `fn target() {}
fn run() { let callback = || { target(); }; callback(); }
`
	root := rustFixture(t, map[string]string{"Cargo.toml": "[package]\nname='closures'\nedition='2021'\n", "src/lib.rs": source})
	before, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	source = "// moved\r\n" + strings.ReplaceAll(source, "\n", "\r\n")
	if err := os.WriteFile(filepath.Join(root, "src", "lib.rs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	closures := 0
	for i, symbol := range after.Symbols {
		if !strings.Contains(symbol.Signature, "::closure[") {
			continue
		}
		closures++
		if symbol.Key != before.Symbols[i].Key || symbol.Fingerprint != before.Symbols[i].Fingerprint {
			t.Fatalf("unstable closure identity: %+v %+v", before.Symbols[i], symbol)
		}
		found := false
		for _, call := range after.Calls {
			if call.Caller == symbol.ID && call.Candidate != "" {
				found = true
			}
		}
		if !found {
			t.Fatal("closure body lost its caller or direct target")
		}
	}
	if closures != 1 {
		t.Fatalf("wanted closure projection: %+v", after.Symbols)
	}
	for _, call := range after.Calls {
		if call.Callee == "callback" && call.Candidate != "" {
			t.Fatal("function-valued binding guessed as invocation target")
		}
	}
}

func Test_should_not_pair_indistinguishable_repeated_closures(t *testing.T) {
	root := rustFixture(t, map[string]string{"src/lib.rs": `fn run() { register(|| {}); register(|| {}); }`})
	result, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Symbols) != 1 {
		t.Fatalf("ambiguous callback anchors were paired: %+v", result.Symbols)
	}
	if !strings.Contains(strings.Join(result.Notes, " "), "Indistinguishable") {
		t.Fatal("missing ambiguity limit")
	}
}

func Test_should_retain_nested_closures_and_distinct_argument_slots(t *testing.T) {
	root := rustFixture(t, map[string]string{"src/lib.rs": `fn run() { register(|| { let inner = || {}; }, || {}); }`})
	result, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Symbols) != 4 {
		t.Fatalf("wanted function plus three closures: %+v", result.Symbols)
	}
	keys := map[string]bool{}
	for _, symbol := range result.Symbols {
		if keys[symbol.Key] {
			t.Fatalf("duplicate key %s", symbol.Key)
		}
		keys[symbol.Key] = true
	}
}

func Test_should_expose_cargo_configuration_changes_as_context_not_callable_changes(t *testing.T) {
	root := rustFixture(t, map[string]string{"Cargo.toml": "[package]\nname='config'\n[features]\nfeature=[]\n", ".cargo/config.toml": "[build]\njobs=1\n", "src/lib.rs": "fn run() {}"})
	before, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Cargo.toml"), []byte("[package]\nname='config'\n[features]\nfeature=['other']\nother=[]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	contexts := func(snapshot analysis.ImpactSnapshot) map[string]string {
		out := map[string]string{}
		for _, ctx := range snapshot.Contexts {
			out[ctx.Path] = ctx.Fingerprint
		}
		return out
	}
	left, right := contexts(before), contexts(after)
	if left["Cargo.toml"] == right["Cargo.toml"] || left[".cargo/config.toml"] == "" {
		t.Fatal("configuration context missing")
	}
	if before.Symbols[0].Fingerprint != after.Symbols[0].Fingerprint {
		t.Fatal("config falsely changed callable")
	}
}

func Test_should_use_platform_executable_names_for_rust_llvm_tools(t *testing.T) {
	for _, name := range []string{"llvm-cov", "llvm-profdata"} {
		directory := filepath.Join(t.TempDir(), "工具 space")
		windows := llvmExecutable(directory, name, "windows")
		if filepath.Base(windows) != name+".exe" {
			t.Fatal(windows)
		}
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(windows, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(windows); err != nil {
			t.Fatal(err)
		}
		if filepath.Base(llvmExecutable(directory, name, "darwin")) != name {
			t.Fatal("Unix tool suffix changed")
		}
	}
}

func Test_should_preserve_unprojected_function_edits_as_context(t *testing.T) {
	root := rustFixture(t, map[string]string{"src/lib.rs": "fn stable() {}\nfn invalid() { let value = ; }\n"})
	before, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "lib.rs"), []byte("fn stable() {}\nfn invalid() { let changed = ; }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Symbols) != 1 || len(after.Symbols) != 1 || before.Symbols[0].Fingerprint != after.Symbols[0].Fingerprint {
		t.Fatalf("valid callable changed or malformed callable projected: %+v %+v", before, after)
	}
	if len(before.Contexts) != 1 || len(after.Contexts) != 1 || before.Contexts[0].Fingerprint == after.Contexts[0].Fingerprint {
		t.Fatal("malformed function body vanished from both symbols and context")
	}
}

func Test_should_preserve_ambiguous_nested_closure_edits_in_context(t *testing.T) {
	root := rustFixture(t, map[string]string{"src/lib.rs": "fn run() { register(|| { first(); }); register(|| { first(); }); }"})
	before, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "lib.rs"), []byte("fn run() { register(|| { second(); }); register(|| { first(); }); }"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Symbols) != 1 || len(after.Symbols) != 1 {
		t.Fatal("ambiguous closures unexpectedly projected")
	}
	if before.Contexts[0].Fingerprint == after.Contexts[0].Fingerprint {
		t.Fatal("unprojected nested closure was masked by enclosing function")
	}
}
