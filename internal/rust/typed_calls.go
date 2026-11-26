package rust

import sitter "github.com/tree-sitter/go-tree-sitter"

type typeEvidence struct {
	count       int
	conditional bool
	kind        string
}

func (g *callGraph) recordType(module moduleKey, node *sitter.Node, attributes []sourceAttribute, source []byte) {
	switch node.Kind() {
	case "struct_item", "enum_item", "trait_item":
	default:
		return
	}
	name := node.ChildByFieldName("name")
	if name == nil {
		return
	}
	if g.localTypes[module] == nil {
		g.localTypes[module] = map[string]typeEvidence{}
	}
	evidence := g.localTypes[module][name.Utf8Text(source)]
	evidence.count++
	evidence.conditional = evidence.conditional || guarded(attributes) || node.HasError() || node.ChildByFieldName("type_parameters") != nil
	evidence.kind = node.Kind()
	g.localTypes[module][name.Utf8Text(source)] = evidence
}

func sourceImpl(impl *sitter.Node, conditional bool, source []byte) (string, string, bool) {
	typ := impl.ChildByFieldName("type")
	if typ == nil || typ.Kind() != "type_identifier" {
		return "", "", true
	}
	traitName := ""
	if trait := impl.ChildByFieldName("trait"); trait != nil {
		if trait.Kind() != "type_identifier" {
			return "", "", true
		}
		traitName = trait.Utf8Text(source)
	}
	conditional = conditional || impl.HasError() || impl.ChildByFieldName("type_parameters") != nil || impl.Parent() == nil || !moduleContainer(impl.Parent())
	for sibling := impl.PrevNamedSibling(); sibling != nil; sibling = sibling.PrevNamedSibling() {
		switch sibling.Kind() {
		case "line_comment", "block_comment":
			continue
		case "attribute_item":
			conditional = true
		}
		break
	}
	return typ.Utf8Text(source), traitName, conditional
}

func simpleReceiverType(node *sitter.Node, source []byte) string {
	for node != nil && node.Kind() == "reference_type" {
		node = node.ChildByFieldName("type")
	}
	if node != nil && node.Kind() == "type_identifier" {
		return node.Utf8Text(source)
	}
	return ""
}

func explicitReceiverType(call, callee *sitter.Node, source []byte) (string, string) {
	if callee == nil || callee.HasError() {
		return "", ""
	}
	if callee.Kind() == "scoped_identifier" {
		qualifier := callee.ChildByFieldName("path")
		if qualifier != nil && qualifier.Kind() == "bracketed_type" && qualifier.NamedChildCount() == 1 {
			qualified := qualifier.NamedChild(0)
			if qualified.Kind() == "qualified_type" {
				typ, trait := qualified.ChildByFieldName("type"), qualified.ChildByFieldName("alias")
				if typ != nil && trait != nil && typ.Kind() == "type_identifier" && trait.Kind() == "type_identifier" {
					if typeParameterShadows(call, typ.Utf8Text(source), source) || typeParameterShadows(call, trait.Utf8Text(source), source) {
						return "", ""
					}
					return typ.Utf8Text(source), trait.Utf8Text(source)
				}
			}
		}
		return "", ""
	}
	if callee.Kind() != "field_expression" {
		return "", ""
	}
	value := callee.ChildByFieldName("value")
	if value == nil || value.Kind() != "identifier" {
		return "", ""
	}
	name := value.Utf8Text(source)
	for scope := call.Parent(); scope != nil && !moduleContainer(scope); scope = scope.Parent() {
		switch scope.Kind() {
		case "block":
			var declaration *sitter.Node
			count := 0
			for i := uint(0); i < scope.NamedChildCount(); i++ {
				child := scope.NamedChild(i)
				if child.Kind() == "let_declaration" && containsName(child.ChildByFieldName("pattern"), name, source) {
					count++
					declaration = child
				}
			}
			if count > 0 {
				if count != 1 || declaration.EndByte() > call.StartByte() {
					return "", ""
				}
				pattern := declaration.ChildByFieldName("pattern")
				if pattern == nil || pattern.Kind() != "identifier" {
					return "", ""
				}
				typ := simpleReceiverType(declaration.ChildByFieldName("type"), source)
				if typeParameterShadows(call, typ, source) {
					return "", ""
				}
				return typ, ""
			}
		case "function_item", "closure_expression":
			params := scope.ChildByFieldName("parameters")
			if params != nil {
				for i := uint(0); i < params.NamedChildCount(); i++ {
					param := params.NamedChild(i)
					if !containsName(param.ChildByFieldName("pattern"), name, source) {
						continue
					}
					pattern := param.ChildByFieldName("pattern")
					if pattern == nil || pattern.Kind() != "identifier" {
						return "", ""
					}
					typ := simpleReceiverType(param.ChildByFieldName("type"), source)
					if typeParameterShadows(call, typ, source) {
						return "", ""
					}
					return typ, ""
				}
			}
			if scope.Kind() == "function_item" {
				return "", ""
			}
		case "for_expression", "match_arm", "let_condition":
			if containsName(scope.ChildByFieldName("pattern"), name, source) {
				return "", ""
			}
		case "if_expression", "while_expression":
			if containsName(scope.ChildByFieldName("condition"), name, source) {
				return "", ""
			}
		}
	}
	return "", ""
}

func (g *callGraph) resolveTypedCalls(index callIndex) {
	for i := range g.Calls {
		call := &g.Calls[i]
		if call.TargetID != "" || call.receiverType == "" || call.method == "" {
			continue
		}
		key := moduleKey{call.Site.File, call.module}
		typ := g.localTypes[key][call.receiverType]
		if typ.count != 1 || typ.conditional || typ.kind == "trait_item" {
			continue
		}
		owners := index.instances[key]
		if len(index.imports[key][call.receiverType]) != 0 || len(index.imports[key]["*"]) != 0 {
			continue
		}
		if len(owners) == 0 {
			continue
		}
		conditional := false
		for _, owner := range owners {
			conditional = conditional || owner.conditional
		}
		if conditional {
			continue
		}
		if call.receiverTrait != "" {
			trait := g.localTypes[key][call.receiverTrait]
			if trait.count != 1 || trait.conditional || trait.kind != "trait_item" {
				continue
			}
		}
		var candidates []functionSymbol
		for _, symbol := range g.Symbols {
			if symbol.Definition.File == key.file && symbol.module == key.inline && symbol.implType == call.receiverType && symbol.implTrait == call.receiverTrait && symbol.Name == call.method {
				candidates = append(candidates, symbol)
			}
		}
		if len(candidates) == 1 && !candidates[0].conditional && (!call.receiver || candidates[0].receiver) {
			call.TargetID, call.Resolution, call.Reason = candidates[0].ID, "resolved", ""
		} else {
			call.Reason = "ambiguous-or-conditional-method"
		}
	}
}

func typeParameterShadows(node *sitter.Node, name string, source []byte) bool {
	for scope := node.Parent(); scope != nil; scope = scope.Parent() {
		if scope.Kind() == "block" {
			for i := uint(0); i < scope.NamedChildCount(); i++ {
				item := scope.NamedChild(i)
				switch item.Kind() {
				case "use_declaration":
					return true
				case "type_item", "struct_item", "enum_item", "trait_item":
					if containsName(item.ChildByFieldName("name"), name, source) {
						return true
					}
				}
			}
		}
		if containsName(scope.ChildByFieldName("type_parameters"), name, source) {
			return true
		}
	}
	return false
}
