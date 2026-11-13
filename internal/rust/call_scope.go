package rust

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

func moduleContainer(node *sitter.Node) bool {
	return node.Kind() == "source_file" || node.Kind() == "declaration_list" && node.Parent() != nil && node.Parent().Kind() == "mod_item"
}

func guarded(attributes []sourceAttribute) bool {
	// Allow only attributes known here to preserve the declaration and name.
	for _, attr := range attributes {
		node := attr.node.NamedChild(0)
		if node == nil || node.NamedChild(0) == nil {
			return true
		}
		switch node.NamedChild(0).Kind() {
		case "identifier":
			switch strings.TrimSpace(strings.TrimPrefix(attr.text, "#[")) {
			case "inline]", "inline(always)]", "inline(never)]":
				continue
			}
		}
		return true
	}
	return false
}

func (g *callGraph) block(key moduleKey, item *sitter.Node, source []byte) {
	switch item.Kind() {
	case "const_item", "static_item", "type_item", "struct_item", "enum_item", "extern_crate_declaration", "union_item":
		name := item.ChildByFieldName("alias")
		if name == nil {
			name = item.ChildByFieldName("name")
		}
		if name != nil {
			if g.blockers[key] == nil {
				g.blockers[key] = map[string]bool{}
			}
			g.blockers[key][name.Utf8Text(source)] = true
		}
	}
}

func shadowsCall(call *sitter.Node, path string, source []byte) bool {
	name, _, qualified := strings.Cut(path, "::")
	for scope := call.Parent(); scope != nil && !moduleContainer(scope); scope = scope.Parent() {
		switch scope.Kind() {
		case "function_item", "closure_expression", "for_expression", "match_arm", "let_condition":
			for _, field := range []string{"parameters", "pattern", "type_parameters"} {
				if containsName(scope.ChildByFieldName(field), name, source) {
					return true
				}
			}
		case "if_expression", "while_expression":
			if containsName(scope.ChildByFieldName("condition"), name, source) {
				return true
			}
		case "block":
			for i := uint(0); i < scope.NamedChildCount(); i++ {
				item := scope.NamedChild(i)
				switch item.Kind() {
				case "let_declaration":
					if !qualified && containsName(item.ChildByFieldName("pattern"), name, source) {
						return true
					}
				case "use_declaration", "extern_crate_declaration":
					if !strings.HasPrefix(path, "crate::") && !strings.HasPrefix(path, "self::") && !strings.HasPrefix(path, "super::") {
						return true
					}
				case "function_item", "mod_item", "const_item", "static_item", "struct_item", "enum_item", "type_item":
					if containsName(item.ChildByFieldName("name"), name, source) {
						return true
					}
				}
			}
		case "impl_item", "trait_item":
			if containsName(scope.ChildByFieldName("type_parameters"), name, source) {
				return true
			}
		}
	}
	return false
}

func containsName(node *sitter.Node, name string, source []byte) bool {
	if node == nil {
		return false
	}
	if node.NamedChildCount() == 0 {
		return node.Utf8Text(source) == name
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		if containsName(node.NamedChild(i), name, source) {
			return true
		}
	}
	return false
}

func itemVisibility(node *sitter.Node, source []byte) string {
	for i := uint(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if child.Kind() == "visibility_modifier" {
			return sourcePath(child, source)
		}
	}
	return ""
}
