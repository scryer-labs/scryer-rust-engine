package rust

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestTestContextRequiresLanguageEvidence(t *testing.T) {
	root := t.TempDir()
	source := `
use crate::domain::production;
#[cfg(all(test, unix))]
mod fixtures {
    use crate::{application::{self, service as runner}, domain::Model};
    fn exercise() { crate::application::run(); }
}
mod test_business {
    use crate::domain::business;
}
#[cfg(any(test, feature = "debug"))]
mod either { use crate::domain::either; }
#[cfg(any(all(test, unix), all(test, windows)))]
mod both { use crate::application::both; }
#[cfg(not(test))]
mod normal { use crate::domain::normal; }
#[test]
fn verify() { crate::application::verify(); }
fn after() { crate::domain::after(); }
`
	if err := os.WriteFile(filepath.Join(root, "lib.rs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	scan := func() map[string]any {
		facts, err := Scan(context.Background(), root, []string{"files"})
		if err != nil {
			t.Fatal(err)
		}
		return facts["files"].(map[string]any)
	}
	facts := scan()
	if !reflect.DeepEqual(facts, scan()) {
		t.Fatal("the same checkout produced different facts")
	}
	check := func(label string, scope *TestContext, want string) {
		t.Helper()
		if want == "" {
			if scope != nil {
				t.Fatalf("%s unexpectedly test-only: %+v", label, scope)
			}
			return
		}
		if scope == nil || scope.Scope != "test-only" || len(scope.Evidence) != 1 ||
			scope.Evidence[0].Kind != want || scope.Evidence[0].File != "lib.rs" || scope.Evidence[0].Line == 0 {
			t.Fatalf("%s missing test evidence: %+v", label, scope)
		}
	}
	imports := facts["imports"].([]Import)
	if len(imports) != 6 {
		t.Fatalf("unexpected imports: %+v", imports)
	}
	if want := []string{"crate::application", "crate::application::service", "crate::domain::Model"}; !slices.Equal(imports[1].Paths, want) {
		t.Fatalf("grouped imports: got %v, want %v", imports[1].Paths, want)
	}
	for i, want := range []string{"", "rust-cfg", "", "", "rust-cfg", ""} {
		check(imports[i].Expression, imports[i].TestContext, want)
	}
	references := facts["references"].([]Reference)
	if len(references) != 3 {
		t.Fatalf("unexpected references: %+v", references)
	}
	for i, want := range []string{"rust-cfg", "rust-test", ""} {
		check(references[i].Path, references[i].TestContext, want)
	}
}
