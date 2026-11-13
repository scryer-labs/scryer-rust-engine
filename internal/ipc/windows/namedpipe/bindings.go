package namedpipe

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

type bindings struct {
	parent    *bindings
	imports   map[string][]string
	constants map[string][]*sitter.Node
	blocked   map[string]bool
}

func readBindings(node *sitter.Node, source []byte) *bindings {
	scope := &bindings{imports: map[string][]string{}, constants: map[string][]*sitter.Node{}, blocked: map[string]bool{}}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if child.HasError() {
			continue
		}
		switch child.Kind() {
		case "use_declaration":
			if arg := child.ChildByFieldName("argument"); arg != nil {
				scope.addImports(arg, "", source)
			}
		case "const_item":
			name, value := child.ChildByFieldName("name"), child.ChildByFieldName("value")
			if name != nil {
				id := name.Utf8Text(source)
				scope.constants[id] = append(scope.constants[id], value)
			}
		case "mod_item", "struct_item", "enum_item", "type_item", "static_item":
			if name := child.ChildByFieldName("name"); name != nil {
				scope.blocked[name.Utf8Text(source)] = true
			}
		}
	}
	return scope
}

func blockBindings(node *sitter.Node, source []byte, parent *bindings) *bindings {
	scope := readBindings(node, source)
	scope.parent = parent
	if enclosing := node.Parent(); enclosing != nil && (enclosing.Kind() == "function_item" || enclosing.Kind() == "closure_expression") {
		if params := enclosing.ChildByFieldName("parameters"); params != nil {
			blockPattern(params, source, scope.blocked)
		}
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		if child := node.NamedChild(i); child.Kind() == "let_declaration" {
			if pattern := child.ChildByFieldName("pattern"); pattern != nil {
				blockPattern(pattern, source, scope.blocked)
			}
		}
	}
	return scope
}

func blockPattern(node *sitter.Node, source []byte, blocked map[string]bool) {
	if node.Kind() == "identifier" || node.Kind() == "shorthand_field_identifier" {
		blocked[node.Utf8Text(source)] = true
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		blockPattern(node.NamedChild(i), source, blocked)
	}
}

func (s *bindings) addImports(node *sitter.Node, prefix string, source []byte) {
	switch node.Kind() {
	case "scoped_use_list":
		if qualifier := node.ChildByFieldName("path"); qualifier != nil {
			prefix += pathText(qualifier, source) + "::"
		}
		if list := node.ChildByFieldName("list"); list != nil {
			s.addImports(list, prefix, source)
		}
	case "use_list":
		for i := uint(0); i < node.NamedChildCount(); i++ {
			s.addImports(node.NamedChild(i), prefix, source)
		}
	case "use_as_clause":
		alias, target := node.ChildByFieldName("alias"), node.ChildByFieldName("path")
		if alias != nil && target != nil {
			id := alias.Utf8Text(source)
			s.imports[id] = append(s.imports[id], prefix+pathText(target, source))
		}
	case "self", "identifier", "scoped_identifier":
		full := prefix + pathText(node, source)
		if node.Kind() == "self" {
			full = strings.TrimSuffix(full, "::self")
		}
		id := full
		if i := strings.LastIndex(full, "::"); i >= 0 {
			id = full[i+2:]
		}
		s.imports[id] = append(s.imports[id], full)
	}
}

func (s *bindings) constant(name string) *sitter.Node {
	for scope := s; scope != nil; scope = scope.parent {
		if scope.blocked[name] || len(scope.imports[name]) != 0 {
			return nil
		}
		if values := scope.constants[name]; len(values) > 0 {
			if len(values) == 1 {
				return values[0]
			}
			return nil
		}
	}
	return nil
}

func (s *bindings) isBuilder(name, builder string) bool {
	expected := tokioPipe + builder
	if strings.HasPrefix(name, "::") {
		return strings.TrimPrefix(name, "::") == expected
	}
	head, tail, qualified := strings.Cut(name, "::")
	for scope := s; scope != nil; scope = scope.parent {
		if scope.blocked[head] || len(scope.constants[head]) != 0 {
			return false
		}
		if imports := scope.imports[head]; len(imports) != 0 {
			if len(imports) != 1 {
				return false
			}
			full := strings.TrimPrefix(imports[0], "::")
			if qualified {
				full += "::" + tail
			}
			return full == expected
		}
	}
	return name == expected
}
