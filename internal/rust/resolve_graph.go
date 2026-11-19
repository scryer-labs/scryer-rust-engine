package rust

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/scryer-labs/scryer-rust-engine/internal/analysis"
)

type GraphResolution struct {
	Status       string `json:"status"`
	Basis        string `json:"basis"`
	BindingBasis string `json:"bindingBasis"`
	Message      string `json:"message,omitempty"`
	Metadata     any    `json:"metadata,omitempty"`
}

// ResolveGraph validates the Cargo-selected build while retaining the source
// graph's weaker binding semantics. This path is shared by scan and impact.
func ResolveGraph(ctx context.Context, root string, facts map[string]any) GraphResolution {
	result := GraphResolution{Status: "fallback", Basis: "cargo-check", BindingBasis: "source-candidates"}
	defer func() {
		if graph, ok := facts["call-graph"].(*callGraph); ok {
			graph.Semantic = result
		}
		if files, ok := facts["files"].(map[string]any); ok {
			files["semantic"] = result
		}
	}()
	dependencies := facts["dependencies"].(map[string]any)
	manifests := dependencies["manifests"].([]Manifest)
	if len(manifests) == 0 {
		result.Message = "no Cargo manifest found"
		return result
	}
	result.Metadata = ResolveDependencies(ctx, root, manifests)
	work, err := os.MkdirTemp("", "scryer-rust-resolve-")
	if err != nil {
		result.Message = err.Error()
		return result
	}
	defer os.RemoveAll(work)
	bounded, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for _, manifest := range manifests {
		dir := filepath.Join(root, filepath.Dir(manifest.File))
		_, err := analysis.Command(bounded, dir, map[string]string{"CARGO_TARGET_DIR": work}, "cargo", "check", "--frozen", "--tests", "--manifest-path", filepath.Join(root, filepath.FromSlash(manifest.File)))
		if err != nil {
			result.Status = "partial"
			result.Message = fmt.Sprintf("%s: %v", manifest.File, err)
			return result
		}
	}
	result.Status = "resolved"
	return result
}
