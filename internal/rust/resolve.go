package rust

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type cargoResolution struct {
	Manifest string          `json:"manifest"`
	Status   string          `json:"status"`
	Message  string          `json:"message,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

func ResolveDependencies(ctx context.Context, root string, manifests []Manifest) any {
	bounded, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cargo, err := exec.LookPath("cargo")
	target := hostTarget()
	results := make([]cargoResolution, 0, len(manifests))
	for _, manifest := range manifests {
		result := cargoResolution{Manifest: manifest.File, Status: "fallback"}
		switch {
		case err != nil:
			result.Message = "cargo is unavailable"
		case bounded.Err() != nil:
			result.Message = bounded.Err().Error()
		default:
			args := []string{"metadata", "--format-version", "1", "--frozen", "--manifest-path", filepath.Join(root, filepath.FromSlash(manifest.File))}
			if target != "" {
				args = append(args, "--filter-platform", target)
			}
			command := exec.CommandContext(bounded, cargo, args...)
			command.Dir = filepath.Join(root, filepath.Dir(manifest.File))
			command.WaitDelay = time.Second
			var stderr bytes.Buffer
			command.Stderr = &stderr
			output, failure := command.Output()
			if failure != nil {
				result.Message = strings.TrimSpace(stderr.String())
				if result.Message == "" {
					result.Message = failure.Error()
				}
			} else if !json.Valid(output) {
				result.Message = "Cargo returned invalid metadata JSON"
			} else {
				result.Status, result.Metadata = "resolved", output
			}
		}
		results = append(results, result)
	}
	return map[string]any{"basis": "cargo-metadata", "target": target, "manifests": results}
}

func hostTarget() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return "aarch64-apple-darwin"
	case "darwin/amd64":
		return "x86_64-apple-darwin"
	case "linux/amd64":
		return "x86_64-unknown-linux-gnu"
	case "linux/arm64":
		return "aarch64-unknown-linux-gnu"
	}
	return ""
}
