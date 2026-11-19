package rust

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/scryer-labs/scryer-rust-engine/internal/analysis"
)

func rustLLVM(ctx context.Context, root string) (string, string, error) {
	version, err := analysis.Command(ctx, root, nil, "rustc", "-vV")
	if err != nil {
		return "", "", err
	}
	match := regexp.MustCompile(`LLVM version: ([0-9]+)`).FindSubmatch(version)
	if len(match) != 2 {
		return "", "", fmt.Errorf("rustc did not report its LLVM version")
	}
	sysroot, err := analysis.Command(ctx, root, nil, "rustc", "--print", "sysroot")
	if err != nil {
		return "", "", err
	}
	host := regexp.MustCompile(`(?m)^host: (.+)$`).FindSubmatch(version)
	if len(host) != 2 {
		return "", "", fmt.Errorf("rustc did not report its host")
	}
	directories := []string{os.Getenv("SCRYER_RUST_LLVM_BIN"), filepath.Join(strings.TrimSpace(string(sysroot)), "lib", "rustlib", string(host[1]), "bin"), "/opt/homebrew/opt/llvm/bin", "/usr/local/opt/llvm/bin"}
	if found, err := exec.LookPath("llvm-cov"); err == nil {
		directories = append(directories, filepath.Dir(found))
	}
	for _, directory := range directories {
		if directory == "" {
			continue
		}
		cov, prof := filepath.Join(directory, "llvm-cov"), filepath.Join(directory, "llvm-profdata")
		output, err := analysis.Command(ctx, root, nil, cov, "--version")
		if err != nil {
			continue
		}
		reported := regexp.MustCompile(`LLVM version ([0-9]+)`).FindSubmatch(output)
		if len(reported) == 2 && bytes.Equal(reported[1], match[1]) {
			if _, err := os.Stat(prof); err == nil {
				return cov, prof, nil
			}
		}
	}
	return "", "", fmt.Errorf("LLVM tools matching rustc LLVM %s are required; install llvm-tools-preview for the active Rust toolchain or set SCRYER_RUST_LLVM_BIN", match[1])
}
func Coverage(ctx context.Context, request analysis.CoverageRequest) (analysis.CoverageReport, error) {
	root := request.Repository
	result := analysis.CoverageReport{SchemaVersion: 1, Root: root, Engine: "rust", Status: "unavailable", Runs: []analysis.CoverageRun{}}
	if !filepath.IsAbs(root) {
		return result, fmt.Errorf("Rust coverage requires an absolute repository")
	}
	if len(request.TestTargets) > 0 || len(request.TestBinaries) > 0 {
		return result, fmt.Errorf("Rust coverage uses Cargo test targets; CMake test selection is not supported by this engine")
	}
	if request.Artifact != "" {
		evidence, err := analysis.ImportLLVM(ctx, root, request.Artifact)
		if err != nil {
			return result, err
		}
		result.Status = "collected"
		result.Coverage, _ = json.Marshal(evidence)
		return result, nil
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cov, prof, err := rustLLVM(bounded, root)
	if err != nil {
		return result, err
	}
	facts, err := Scan(bounded, root, []string{"dependencies"})
	if err != nil {
		return result, err
	}
	manifests := facts["dependencies"].(map[string]any)["manifests"].([]Manifest)
	if len(manifests) == 0 {
		return result, fmt.Errorf("no Cargo.toml found")
	}
	work, err := os.MkdirTemp("", "scryer-rust-coverage-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(work)
	seen := map[string]bool{}
	objects := []string{}
	status := "collected"
	for _, manifest := range manifests {
		manifestPath := filepath.Join(root, filepath.FromSlash(manifest.File))
		metadata, err := analysis.Command(bounded, root, nil, "cargo", "metadata", "--frozen", "--no-deps", "--format-version", "1", "--manifest-path", manifestPath)
		if err != nil {
			return result, err
		}
		var model struct {
			WorkspaceRoot string `json:"workspace_root"`
		}
		if err = json.Unmarshal(metadata, &model); err != nil {
			return result, err
		}
		if seen[model.WorkspaceRoot] {
			continue
		}
		seen[model.WorkspaceRoot] = true
		env := map[string]string{"CARGO_TARGET_DIR": filepath.Join(work, "target"), "LLVM_PROFILE_FILE": filepath.Join(work, "%m-%p.profraw"), "RUSTFLAGS": os.Getenv("RUSTFLAGS") + " -C instrument-coverage -C link-dead-code"}
		if flags := os.Getenv("CARGO_ENCODED_RUSTFLAGS"); flags != "" {
			env["CARGO_ENCODED_RUSTFLAGS"] = flags + "\x1f-C\x1finstrument-coverage\x1f-C\x1flink-dead-code"
		}
		output, failure := analysis.Command(bounded, model.WorkspaceRoot, env, "cargo", "test", "--frozen", "--workspace", "--tests", "--no-fail-fast", "--message-format=json", "--manifest-path", manifestPath)
		if bounded.Err() != nil {
			return result, bounded.Err()
		}
		run := analysis.CoverageRun{Command: []string{"cargo", "test", "--frozen", "--workspace", "--tests", "--no-fail-fast", "--message-format=json", "--manifest-path", manifestPath}, Directory: filepath.ToSlash(filepath.Dir(manifest.File)), Configuration: map[string]string{"RUSTFLAGS": env["RUSTFLAGS"], "CARGO_ENCODED_RUSTFLAGS": env["CARGO_ENCODED_RUSTFLAGS"], "llvmCov": cov, "llvmProfdata": prof}, Name: "cargo test --workspace --tests: " + manifest.File, Status: "succeeded"}
		if failure != nil {
			run.Status = "failed"
			run.Message = failure.Error()
			status = "partial"
		}
		scanner := bufio.NewScanner(bytes.NewReader(output))
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			var artifact struct {
				Reason     string `json:"reason"`
				Executable string `json:"executable"`
				Profile    struct {
					Test bool `json:"test"`
				} `json:"profile"`
			}
			if json.Unmarshal(scanner.Bytes(), &artifact) == nil && artifact.Reason == "compiler-artifact" && artifact.Profile.Test && artifact.Executable != "" {
				objects = append(objects, artifact.Executable)
				run.Profiles++
			}
		}
		if err = scanner.Err(); err != nil {
			return result, err
		}
		result.Runs = append(result.Runs, run)
	}
	profiles, err := filepath.Glob(filepath.Join(work, "*.profraw"))
	if err != nil {
		return result, err
	}
	if len(profiles) == 0 || len(objects) == 0 {
		result.Message = "Cargo produced no test executable/profile evidence"
		return result, nil
	}
	merged := filepath.Join(work, "coverage.profdata")
	args := append([]string{"merge", "-sparse", "-o", merged}, profiles...)
	if _, err = analysis.Command(bounded, root, nil, prof, args...); err != nil {
		return result, err
	}
	args = []string{"export", "-instr-profile", merged, "-skip-expansions", "-skip-functions", objects[0]}
	for _, object := range objects[1:] {
		args = append(args, "-object", object)
	}
	raw, err := analysis.Command(bounded, root, nil, cov, args...)
	if err != nil {
		return result, err
	}
	path := filepath.Join(work, "llvm-cov.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		return result, err
	}
	evidence, err := analysis.ImportLLVM(bounded, root, path)
	if err != nil {
		return result, err
	}
	evidence.Artifact = "rust-llvm-cov.json"
	evidence.Format = "rust-llvm-cov"
	evidence.Notes = append(evidence.Notes, "Cargo --frozen --workspace --tests with instrumentation and a temporary target directory. Default host/features apply; doctests and individual-test attribution are not included.")
	result.Status = status
	result.Coverage, err = json.Marshal(evidence)
	return result, err
}
