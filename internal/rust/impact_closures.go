package rust

import (
	"fmt"
	"strings"

	"github.com/scryer-labs/scryer-rust-engine/internal/analysis"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

// The enclosing declaration and expression skeleton identify a closure without
// using byte offsets or its mutable body. Repeated indistinguishable anchors are
// deliberately not paired across snapshots.
func closureImpactIdentity(file string, node *sitter.Node, source []byte) (string, string, string, string) {
	if node.HasError() {
		return "", "", "", ""
	}
	owner := node.Parent()
	for owner != nil && owner.Kind() != "function_item" && owner.Kind() != "closure_expression" && owner.Kind() != "source_file" {
		owner = owner.Parent()
	}
	if owner == nil {
		return "", "", "", ""
	}
	ownerKey, ownerName := file+"|module", "module"
	if owner.Kind() != "source_file" {
		key, name, _, _ := impactIdentity(file, owner, source)
		if key == "" {
			return "", "", "", ""
		}
		ownerKey, ownerName = key, name
	}
	anchor := closureAnchor(node, owner, source)
	matches := 0
	var visit func(*sitter.Node)
	visit = func(candidate *sitter.Node) {
		if candidate.Id() != owner.Id() && candidate.Kind() == "closure_expression" {
			if closureAnchor(candidate, owner, source) == anchor {
				matches++
			}
			return
		}
		if candidate.Id() != owner.Id() && candidate.Kind() == "function_item" {
			return
		}
		for i := uint(0); i < candidate.NamedChildCount(); i++ {
			visit(candidate.NamedChild(i))
		}
	}
	visit(owner)
	if matches != 1 {
		return "", "", "", ""
	}
	signature := ownerName + "::closure[" + analysis.Hash([]byte(anchor))[:12] + "]"
	return ownerKey + "|closure:" + analysis.Hash([]byte(anchor)), signature, analysis.Hash([]byte(tokens(node, source))), node.Utf8Text(source)
}

func closureAnchor(node, owner *sitter.Node, source []byte) string {
	anchor := node
	// Include the nearest declaration or call, preserving the argument/field slot.
	for parent := node.Parent(); parent != nil && parent.Id() != owner.Id(); parent = parent.Parent() {
		anchor = parent
		switch parent.Kind() {
		case "let_declaration", "const_item", "static_item", "call_expression", "field_initializer", "return_expression":
			return closureSkeleton(anchor, node, source)
		case "block":
			return closureSkeleton(node, node, source)
		}
	}
	return closureSkeleton(anchor, node, source)
}

func closureSkeleton(node, selected *sitter.Node, source []byte) string {
	if node.Kind() == "line_comment" || node.Kind() == "block_comment" {
		return ""
	}
	if node.Kind() == "closure_expression" {
		if node.Id() == selected.Id() {
			return "<selected-closure>"
		}
		return "<closure>"
	}
	if node.ChildCount() == 0 {
		text := node.Utf8Text(source)
		return fmt.Sprintf("%d:%s;", len(text), text)
	}
	var result strings.Builder
	for i := uint(0); i < node.ChildCount(); i++ {
		result.WriteString(closureSkeleton(node.Child(i), selected, source))
	}
	return result.String()
}
