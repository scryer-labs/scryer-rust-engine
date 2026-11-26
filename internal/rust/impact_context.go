package rust

import (
	"fmt"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// Mask only callables represented in the comparison snapshot. An unprojected
// nested body also prevents masking its enclosing callable: otherwise malformed
// or ambiguous source changes could disappear from both evidence channels.
func (g *callGraph) contextTokens(file string, root *sitter.Node, source []byte) string {
	// Byte ranges are private syntax identity, not cross-snapshot correspondence.
	projectedRanges := map[sourceRange]bool{}
	for _, symbol := range g.Symbols {
		if symbol.Definition.File == file && symbol.impactKey != "" {
			projectedRanges[symbol.Definition] = true
		}
	}
	memo := map[uintptr]bool{}
	var maskable func(*sitter.Node) bool
	maskable = func(node *sitter.Node) bool {
		if value, ok := memo[node.Id()]; ok {
			return value
		}
		if isCallable(node) && !projectedRanges[nodeRange(file, node)] {
			memo[node.Id()] = false
			return false
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			if !maskable(node.NamedChild(i)) {
				memo[node.Id()] = false
				return false
			}
		}
		memo[node.Id()] = true
		return true
	}
	var collect func(*sitter.Node) string
	collect = func(node *sitter.Node) string {
		if node.Kind() == "line_comment" || node.Kind() == "block_comment" || isCallable(node) && maskable(node) {
			return ""
		}
		if node.ChildCount() == 0 {
			text := node.Utf8Text(source)
			return fmt.Sprintf("%d:%s;", len(text), text)
		}
		var result strings.Builder
		for i := uint(0); i < node.ChildCount(); i++ {
			result.WriteString(collect(node.Child(i)))
		}
		return result.String()
	}
	return collect(root)
}

func isCallable(node *sitter.Node) bool {
	return node.Kind() == "function_item" || node.Kind() == "closure_expression"
}
