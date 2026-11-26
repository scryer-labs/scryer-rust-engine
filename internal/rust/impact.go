package rust

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/scryer-labs/scryer-rust-engine/internal/analysis"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

func tokens(node *sitter.Node, source []byte) string {
	if node == nil || node.Kind() == "line_comment" || node.Kind() == "block_comment" {
		return ""
	}
	if node.ChildCount() == 0 {
		text := node.Utf8Text(source)
		return fmt.Sprintf("%d:%s;", len(text), text)
	}
	var result strings.Builder
	for i := uint(0); i < node.ChildCount(); i++ {
		result.WriteString(tokens(node.Child(i), source))
	}
	return result.String()
}
func impactIdentity(file string, node *sitter.Node, source []byte) (string, string, string, string) {
	if node.Kind() == "closure_expression" {
		return closureImpactIdentity(file, node, source)
	}
	if node.Kind() != "function_item" || node.HasError() {
		return "", "", "", ""
	}
	name := node.ChildByFieldName("name")
	if name == nil {
		return "", "", "", ""
	}
	scope := []string{}
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Kind() {
		case "mod_item", "function_item":
			if n := parent.ChildByFieldName("name"); n != nil {
				scope = append([]string{n.Utf8Text(source)}, scope...)
			}
		case "impl_item":
			if n := parent.ChildByFieldName("type"); n != nil {
				qualifier := n.Utf8Text(source)
				if trait := parent.ChildByFieldName("trait"); trait != nil {
					qualifier = trait.Utf8Text(source) + " for " + qualifier
				}
				scope = append([]string{qualifier}, scope...)
			}
		}
	}
	signature := strings.Join(append(scope, name.Utf8Text(source)), "::")
	key := file + "|" + signature
	if params := node.ChildByFieldName("parameters"); params != nil {
		key += "|" + tokens(params, source)
	}
	return key, signature, analysis.Hash([]byte(tokens(node, source))), node.Utf8Text(source)
}

func Impact(ctx context.Context, root string, resolve bool) (analysis.ImpactSnapshot, error) {
	if !filepath.IsAbs(root) {
		return analysis.ImpactSnapshot{}, fmt.Errorf("impact requires absolute snapshot roots")
	}
	facts, err := Scan(ctx, root, []string{"call-graph", "dependencies", "files"})
	if err != nil {
		return analysis.ImpactSnapshot{}, err
	}
	graph := facts["call-graph"].(*callGraph)
	result := analysis.ImpactSnapshot{Symbols: []analysis.ImpactSymbol{}, Calls: []analysis.ImpactCall{}, Contexts: graph.contexts, Notes: []string{
		"Rust impact reuses scan's source graph. Cargo build validation does not provide rustc type-directed call binding; trait dispatch, macro expansion and inactive cfg paths remain boundaries or source candidates.",
	}}
	result.Notes = append(result.Notes, "Closure identities use scoped declaration/call anchors, not offsets. Indistinguishable repeated anchors are omitted from comparison; enclosing source changes remain unassessed. Passing or declaring a closure is not a proven invocation.")
	if err := appendConfigurationContexts(root, &result); err != nil {
		return result, err
	}
	if resolve {
		resolution := ResolveGraph(ctx, root, facts)
		result.Resolution, _ = json.Marshal(resolution)
		if resolution.Status != "resolved" {
			return result, fmt.Errorf("dynamic Rust scan did not resolve: %s", resolution.Message)
		}
	}
	for _, symbol := range graph.Symbols {
		if symbol.impactKey == "" {
			result.Notes = append(result.Notes, fmt.Sprintf("Unprojected callable at %s:%d:%d: malformed syntax or ambiguous correspondence; its changed body cannot be compared independently.", symbol.Definition.File, symbol.Definition.Start.Line, symbol.Definition.Start.Column))
			continue
		}
		raw, _ := json.Marshal(symbol.Definition)
		var loc analysis.ImpactRange
		_ = json.Unmarshal(raw, &loc)
		role := "PRODUCTION"
		var scope json.RawMessage
		if symbol.TestContext != nil {
			role = "TEST"
			scope, _ = json.Marshal(symbol.TestContext)
		}
		result.Symbols = append(result.Symbols, analysis.ImpactSymbol{ID: symbol.ID, Key: symbol.impactKey, Signature: symbol.impactSignature, Fingerprint: symbol.impactFingerprint, Source: symbol.impactSource, Location: loc, Role: role, TestContext: scope})
	}
	for _, call := range graph.Calls {
		raw, _ := json.Marshal(call.Site)
		var loc analysis.ImpactRange
		_ = json.Unmarshal(raw, &loc)
		result.Calls = append(result.Calls, analysis.ImpactCall{ID: call.ID, Caller: call.CallerID, Candidate: call.TargetID, Callee: call.Callee, Resolution: call.Resolution, Site: loc, Kind: "source-candidate"})
	}
	return result, nil
}
