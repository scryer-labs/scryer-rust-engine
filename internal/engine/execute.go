package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/scryer-labs/scryer-rust-engine/internal/analysis"
	"github.com/scryer-labs/scryer-rust-engine/internal/framework/tauri"
	"github.com/scryer-labs/scryer-rust-engine/internal/ipc/windows/namedpipe"
	"github.com/scryer-labs/scryer-rust-engine/internal/rust"
)

const (
	MaxRequestBytes   = 1 << 20
	MaxRequestIDBytes = 256
)

type Request struct {
	ProtocolVersion int                      `json:"protocolVersion"`
	ID              string                   `json:"requestId"`
	Stack           string                   `json:"stack"`
	Operation       string                   `json:"operation"`
	Coverage        analysis.CoverageRequest `json:"coverage"`
	Impact          struct {
		Before, After, BeforeRoot, AfterRoot string
		Resolve                              bool
	} `json:"impact"`
	Scan struct {
		Repository string   `json:"repository"`
		Static     *bool    `json:"static"`
		Facts      []string `json:"facts"`
	} `json:"scan"`
}

func ParseRequest(data []byte) (Request, error) {
	var request Request
	if err := json.Unmarshal(data, &request); err != nil || request.ID == "" || len(request.ID) > MaxRequestIDBytes {
		return Request{}, errors.New("invalid request JSON or requestId")
	}
	return request, nil
}

type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type report struct {
	SchemaVersion int            `json:"schemaVersion"`
	Root          string         `json:"root"`
	Facts         map[string]any `json:"facts"`
}

func (r Request) Execute(ctx context.Context) ([]byte, error) {
	var result any
	var failure *protocolError
	if r.ProtocolVersion != 1 || r.Stack != "rust" {
		failure = &protocolError{"INVALID_REQUEST", "unsupported protocol version or stack"}
	} else {
		switch r.Operation {
		case "scan":
			result, failure = r.scan(ctx)
		case "coverage":
			value, err := rust.Coverage(ctx, r.Coverage)
			if err != nil {
				failure = &protocolError{"RUST_COVERAGE_ERROR", err.Error()}
			} else {
				result = value
			}
		case "impact":
			before, err := rust.Impact(ctx, r.Impact.BeforeRoot, r.Impact.Resolve)
			if err != nil {
				failure = &protocolError{"RUST_IMPACT_ERROR", "before: " + err.Error()}
				break
			}
			after, err := rust.Impact(ctx, r.Impact.AfterRoot, r.Impact.Resolve)
			if err != nil {
				failure = &protocolError{"RUST_IMPACT_ERROR", "after: " + err.Error()}
				break
			}
			result = analysis.ImpactFacts{SchemaVersion: 1, BeforeSHA: r.Impact.Before, AfterSHA: r.Impact.After, Before: before, After: after}
		default:
			failure = &protocolError{"UNSUPPORTED_OPERATION", "Rust engine does not support operation " + r.Operation}
		}
	}
	return json.Marshal(struct {
		ProtocolVersion int            `json:"protocolVersion"`
		ID              string         `json:"requestId"`
		Stack           string         `json:"stack"`
		Operation       string         `json:"operation"`
		Report          any            `json:"report"`
		Error           *protocolError `json:"error"`
	}{1, r.ID, "rust", r.Operation, result, failure})
}

func (r Request) scan(ctx context.Context) (*report, *protocolError) {
	if r.ProtocolVersion != 1 || r.Stack != "rust" {
		return nil, &protocolError{"INVALID_REQUEST", "unsupported protocol version or stack"}
	}
	if r.Operation != "scan" {
		return nil, &protocolError{"UNSUPPORTED_OPERATION", "Rust engine does not support operation " + r.Operation}
	}
	selected := r.Scan.Facts
	if len(selected) == 0 {
		selected = []string{"dependencies"}
	}
	for _, fact := range selected {
		if fact != "dependencies" && fact != "architecture" && fact != "files" && fact != "ipc" && fact != "call-graph" {
			return nil, &protocolError{"UNSUPPORTED_FACT", "Rust engine does not support " + fact}
		}
	}
	root := r.Scan.Repository
	if !filepath.IsAbs(root) {
		return nil, &protocolError{"INVALID_REQUEST", "scan requires an absolute repository directory"}
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, &protocolError{"INVALID_REQUEST", "scan repository must be an existing directory"}
	}
	var framework *tauri.Collector
	pipes := namedpipe.Collector{Endpoints: []namedpipe.Endpoint{}}
	var observers []rust.SourceObserver
	if slices.Contains(selected, "ipc") {
		framework = tauri.New(os.DirFS(root))
		observers = append(observers, framework.Observe, pipes.Observe)
	}
	scanFacts := selected
	if framework != nil && !slices.Contains(selected, "architecture") {
		scanFacts = append(slices.Clone(selected), "architecture")
	}
	resolveGraph := r.Scan.Static != nil && !*r.Scan.Static && (slices.Contains(selected, "call-graph") || slices.Contains(selected, "files"))
	if resolveGraph && !slices.Contains(scanFacts, "dependencies") {
		scanFacts = append(slices.Clone(scanFacts), "dependencies")
	}
	result, err := rust.Scan(ctx, root, scanFacts, observers...)
	if err != nil {
		return nil, &protocolError{"RUST_SCAN_ERROR", err.Error()}
	}
	if r.Scan.Static != nil && !*r.Scan.Static && slices.Contains(selected, "dependencies") {
		dependencies := result["dependencies"].(map[string]any)
		dependencies["resolution"] = rust.ResolveDependencies(ctx, root, dependencies["manifests"].([]rust.Manifest))
	}
	if resolveGraph {
		rust.ResolveGraph(ctx, root, result)
		if !slices.Contains(selected, "dependencies") {
			delete(result, "dependencies")
		}
	}
	if framework != nil {
		architecture := result["architecture"].(map[string]any)
		framework.ResolveRegistrations(architecture["modules"].([]rust.Module), architecture["components"].([]rust.Target))
		if !slices.Contains(selected, "architecture") {
			delete(result, "architecture")
		}
		ipc := framework.Facts()
		ipc["limitations"] = []string{"dynamic-channel-names", "tauri-imported-handlers", "tokio-wrapper-flow"}
		ipc["windows"] = map[string]any{"namedPipe": pipes}
		result["ipc"] = ipc
	}
	return &report{1, root, result}, nil
}
