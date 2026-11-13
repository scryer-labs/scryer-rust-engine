package rust

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

type Reference struct {
	Location
	Path        string       `json:"path"`
	ModuleID    string       `json:"moduleId,omitempty"`
	TestContext *TestContext `json:"testContext,omitempty"`
}

func importPaths(node *sitter.Node, prefix string, source []byte) []string {
	switch node.Kind() {
	case "scoped_use_list":
		if qualifier := node.ChildByFieldName("path"); qualifier != nil {
			prefix += sourcePath(qualifier, source) + "::"
		}
		if list := node.ChildByFieldName("list"); list != nil {
			return importPaths(list, prefix, source)
		}
	case "use_list":
		var paths []string
		for i := uint(0); i < node.NamedChildCount(); i++ {
			paths = append(paths, importPaths(node.NamedChild(i), prefix, source)...)
		}
		return paths
	case "use_as_clause":
		if target := node.ChildByFieldName("path"); target != nil {
			return importPaths(target, prefix, source)
		}
	case "self":
		if prefix != "" {
			return []string{strings.TrimSuffix(prefix, "::")}
		}
		return []string{"self"}
	case "crate", "super", "identifier", "scoped_identifier", "use_wildcard":
		return []string{prefix + sourcePath(node, source)}
	}
	return nil
}

func sourcePath(node *sitter.Node, source []byte) string {
	if node.Kind() == "line_comment" || node.Kind() == "block_comment" {
		return ""
	}
	if node.ChildCount() == 0 {
		return node.Utf8Text(source)
	}
	var text strings.Builder
	for i := uint(0); i < node.ChildCount(); i++ {
		text.WriteString(sourcePath(node.Child(i), source))
	}
	return text.String()
}

func explicitCratePath(node *sitter.Node, source []byte) string {
	if node.Kind() != "scoped_identifier" && node.Kind() != "scoped_type_identifier" {
		return ""
	}
	value := sourcePath(node, source)
	if strings.HasPrefix(value, "crate::") {
		return value
	}
	return ""
}
