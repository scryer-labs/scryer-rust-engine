package rust

import (
	"slices"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

type TestContext struct {
	Scope    string         `json:"scope"`
	Evidence []TestEvidence `json:"evidence"`
}

type TestEvidence struct {
	Location
	Kind       string `json:"kind"`
	Expression string `json:"expression"`
}

type sourceAttribute struct {
	node *sitter.Node
	text string
}

func testContext(parent *TestContext, attributes []sourceAttribute, item *sitter.Node, file string, source []byte) *TestContext {
	var evidence []TestEvidence
	for _, attribute := range attributes {
		node := attribute.node.NamedChild(0)
		if node == nil || node.Kind() != "attribute" || node.HasError() {
			continue
		}
		name := node.NamedChild(0)
		if name == nil || name.Kind() != "identifier" || node.ChildByFieldName("value") != nil {
			continue
		}
		args := node.ChildByFieldName("arguments")
		var kind string
		switch name.Utf8Text(source) {
		case "test":
			if item.Kind() == "function_item" && args == nil && item.ChildByFieldName("body") != nil {
				kind = "rust-test"
			}
		case "cfg":
			if predicates, ok := cfgPredicates(args, source); ok && len(predicates) == 1 && predicates[0] {
				kind = "rust-cfg"
			}
		}
		if kind != "" {
			pos := attribute.node.StartPosition()
			evidence = append(evidence, TestEvidence{
				Location: Location{file, pos.Row + 1, pos.Column + 1}, Kind: kind, Expression: attribute.text,
			})
		}
	}
	if len(evidence) == 0 {
		return parent
	}
	if parent != nil {
		evidence = append(slices.Clone(parent.Evidence), evidence...)
	}
	return &TestContext{Scope: "test-only", Evidence: evidence}
}

// cfgPredicates returns whether each comma-separated predicate requires test.
// Unsupported predicate operators provide no evidence.
func cfgPredicates(tree *sitter.Node, source []byte) ([]bool, bool) {
	if tree == nil || tree.Kind() != "token_tree" || tree.ChildCount() < 2 ||
		tree.Child(0).Kind() != "(" || tree.Child(tree.ChildCount()-1).Kind() != ")" {
		return nil, false
	}
	var predicates []bool
	var tokens []*sitter.Node
	for i := uint(1); i+1 < tree.ChildCount(); i++ {
		token := tree.Child(i)
		switch token.Kind() {
		case "line_comment", "block_comment":
			continue
		case ",":
			requires, valid := cfgRequiresTest(tokens, source)
			if !valid {
				return nil, false
			}
			predicates = append(predicates, requires)
			tokens = nil
		default:
			tokens = append(tokens, token)
		}
	}
	if len(tokens) > 0 {
		requires, valid := cfgRequiresTest(tokens, source)
		if !valid {
			return nil, false
		}
		predicates = append(predicates, requires)
	}
	return predicates, true
}

func cfgRequiresTest(tokens []*sitter.Node, source []byte) (bool, bool) {
	if len(tokens) == 0 || tokens[0].Kind() != "identifier" {
		return false, false
	}
	name := tokens[0].Utf8Text(source)
	switch len(tokens) {
	case 1:
		return name == "test", true
	case 2:
		args, valid := cfgPredicates(tokens[1], source)
		if !valid {
			return false, false
		}
		switch name {
		case "all":
			return slices.Contains(args, true), true
		case "any":
			return len(args) > 0 && !slices.Contains(args, false), true
		case "not":
			return false, len(args) == 1
		default:
			return false, false
		}
	case 3:
		return false, tokens[1].Kind() == "=" &&
			(tokens[2].Kind() == "string_literal" || tokens[2].Kind() == "raw_string_literal")
	default:
		return false, false
	}
}
