package rust

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// A receiver inside a single, unconditional inherent impl can refer to a
// method declared in that same impl. Other receiver types need semantic data.
func (g *callGraph) resolveInherentCalls() {
	methods := map[string]map[string][]functionSymbol{}
	for _, symbol := range g.Symbols {
		if symbol.implID == "" {
			continue
		}
		if methods[symbol.implID] == nil {
			methods[symbol.implID] = map[string][]functionSymbol{}
		}
		methods[symbol.implID][symbol.Name] = append(methods[symbol.implID][symbol.Name], symbol)
	}
	for i := range g.Calls {
		call := &g.Calls[i]
		candidates := methods[call.implID][call.method]
		if len(candidates) == 1 && !candidates[0].conditional && (!call.receiver || candidates[0].receiver) {
			call.TargetID, call.Resolution, call.Reason = candidates[0].ID, "resolved", ""
		}
	}
}

func enclosingInherentImpl(node *sitter.Node) *sitter.Node {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Kind() {
		case "impl_item":
			if parent.ChildByFieldName("trait") != nil || (parent.Parent() == nil || !moduleContainer(parent.Parent())) {
				return nil
			}
			// Treat attributes and other syntactic errors as incomplete evidence.
			for sibling := parent.PrevNamedSibling(); sibling != nil; sibling = sibling.PrevNamedSibling() {
				switch sibling.Kind() {
				case "line_comment", "block_comment":
					continue
				case "attribute_item":
					return nil
				}
				break
			}
			if parent.HasError() {
				return nil
			}
			return parent
		case "mod_item", "source_file":
			return nil
		}
	}
	return nil
}

func inherentCallee(callee *sitter.Node, source []byte) (name string, receiver bool) {
	if callee == nil || callee.HasError() {
		return "", false
	}
	switch callee.Kind() {
	case "scoped_identifier":
		prefix, name, ok := strings.Cut(sourcePath(callee, source), "::")
		if ok && prefix == "Self" && !strings.Contains(name, "::") {
			return name, false
		}
	case "field_expression":
		value, field := callee.ChildByFieldName("value"), callee.ChildByFieldName("field")
		if value != nil && value.Kind() == "self" && field != nil {
			return field.Utf8Text(source), true
		}
	}
	return "", false
}
