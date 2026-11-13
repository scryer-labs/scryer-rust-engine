package rust

import (
	"fmt"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

type position struct {
	Line   uint `json:"line"`
	Column uint `json:"column"`
}

type sourceRange struct {
	File  string   `json:"file"`
	Start position `json:"start"`
	End   position `json:"end"`
}

type functionSymbol struct {
	ID             string       `json:"symbolId"`
	Name           string       `json:"name,omitempty"`
	Kind           string       `json:"kind"`
	Definition     sourceRange  `json:"definition"`
	TestContext    *TestContext `json:"testContext,omitempty"`
	module         string
	moduleFunction bool
	visibility     string
	conditional    bool
	implID         string
	receiver       bool
}

type callSite struct {
	ID          string       `json:"callSiteId"`
	CallerID    string       `json:"callerSymbolId,omitempty"`
	Callee      string       `json:"callee"`
	Resolution  string       `json:"resolution"`
	Reason      string       `json:"resolutionReason,omitempty"`
	TargetID    string       `json:"targetSymbolId,omitempty"`
	Site        sourceRange  `json:"site"`
	TestContext *TestContext `json:"testContext,omitempty"`
	directPath  string
	module      string
	shadowed    bool
	implID      string
	method      string
	receiver    bool
}

type callGraph struct {
	imports     []callImport
	blockers    map[moduleKey]map[string]bool
	ProjectID   string           `json:"projectId"`
	Basis       string           `json:"basis"`
	Limitations []string         `json:"limitations"`
	Symbols     []functionSymbol `json:"symbols"`
	Calls       []callSite       `json:"calls"`
}

func nodeRange(file string, node *sitter.Node) sourceRange {
	start, end := node.StartPosition(), node.EndPosition()
	return sourceRange{file, position{start.Row + 1, start.Column + 1}, position{end.Row + 1, end.Column + 1}}
}

func (g *callGraph) symbol(file, module string, node *sitter.Node, scope *TestContext, attributes []sourceAttribute, source []byte) string {
	kind, name := "function", ""
	if node.Kind() == "closure_expression" {
		kind = "lambda"
	} else if id := node.ChildByFieldName("name"); id != nil {
		name = id.Utf8Text(source)
	}
	symbol := functionSymbol{
		ID: fmt.Sprintf("%s:%d", file, node.StartByte()), Name: name, Kind: kind,
		Definition: nodeRange(file, node), TestContext: scope, module: module,
	}
	if parent := node.Parent(); kind == "function" && parent != nil {
		container := parent.Parent()
		symbol.moduleFunction = parent.Kind() == "source_file" || parent.Kind() == "declaration_list" && container != nil && container.Kind() == "mod_item"
		symbol.visibility = itemVisibility(node, source)
		symbol.conditional = guarded(attributes)
		if impl := parent.Parent(); impl != nil && impl.Kind() == "impl_item" && enclosingInherentImpl(node) != nil {
			symbol.implID = fmt.Sprintf("%s:%d", file, impl.StartByte())
			if params := node.ChildByFieldName("parameters"); params != nil && params.NamedChildCount() > 0 {
				symbol.receiver = params.NamedChild(0).Kind() == "self_parameter"
			}
		}
	}
	g.Symbols = append(g.Symbols, symbol)
	return symbol.ID
}

func (g *callGraph) call(file, caller, module string, node *sitter.Node, scope *TestContext, source []byte) {
	callee := node.ChildByFieldName("function")
	if callee == nil {
		return
	}
	site := callSite{
		ID:       fmt.Sprintf("%s:%d-%d", file, node.StartByte(), node.EndByte()),
		CallerID: caller, Callee: callee.Utf8Text(source), Resolution: "unresolved", Reason: "indirect-call",
		Site: nodeRange(file, node), TestContext: scope, module: module,
	}
	if callee.Kind() == "generic_function" {
		callee = callee.ChildByFieldName("function")
	}
	if method, receiver := inherentCallee(callee, source); method != "" {
		if impl := enclosingInherentImpl(node); impl != nil {
			site.implID = fmt.Sprintf("%s:%d", file, impl.StartByte())
			site.method, site.receiver = method, receiver
		}
	}
	if callee != nil && !callee.HasError() && (callee.Kind() == "identifier" || callee.Kind() == "scoped_identifier") {
		site.directPath = sourcePath(callee, source)
		site.shadowed = shadowsCall(node, site.directPath, source)
		site.Reason = "path-not-statically-resolved"
		if site.shadowed {
			site.Reason = "shadowed-binding"
		}
	}
	g.Calls = append(g.Calls, site)
}
