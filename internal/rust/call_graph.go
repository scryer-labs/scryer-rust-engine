package rust

import (
	"fmt"
	"strings"

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
	ID              string       `json:"symbolId"`
	Name            string       `json:"name,omitempty"`
	Kind            string       `json:"kind"`
	Definition      sourceRange  `json:"definition"`
	TestContext     *TestContext `json:"testContext,omitempty"`
	crateRootPublic bool
}

type callSite struct {
	ID          string       `json:"callSiteId"`
	CallerID    string       `json:"callerSymbolId,omitempty"`
	Callee      string       `json:"callee"`
	Resolution  string       `json:"resolution"`
	TargetID    string       `json:"targetSymbolId,omitempty"`
	Site        sourceRange  `json:"site"`
	TestContext *TestContext `json:"testContext,omitempty"`
	directPath  string
}

type callGraph struct {
	ProjectID string           `json:"projectId"`
	Basis     string           `json:"basis"`
	Symbols   []functionSymbol `json:"symbols"`
	Calls     []callSite       `json:"calls"`
}

func nodeRange(file string, node *sitter.Node) sourceRange {
	start, end := node.StartPosition(), node.EndPosition()
	return sourceRange{file, position{start.Row + 1, start.Column + 1}, position{end.Row + 1, end.Column + 1}}
}

func (g *callGraph) symbol(file string, node *sitter.Node, scope *TestContext, attributes []sourceAttribute, source []byte) string {
	kind, name := "function", ""
	if node.Kind() == "closure_expression" {
		kind = "lambda"
	} else if id := node.ChildByFieldName("name"); id != nil {
		name = id.Utf8Text(source)
	}
	symbol := functionSymbol{
		ID: fmt.Sprintf("%s:%d", file, node.StartByte()), Name: name, Kind: kind,
		Definition: nodeRange(file, node), TestContext: scope,
	}
	if node.Parent() != nil && node.Parent().Kind() == "source_file" && node.NamedChildCount() > 0 {
		first := node.NamedChild(0)
		symbol.crateRootPublic = first.Kind() == "visibility_modifier" && first.Utf8Text(source) == "pub" && len(attributes) == 0
	}
	g.Symbols = append(g.Symbols, symbol)
	return symbol.ID
}

func (g *callGraph) call(file, caller string, node *sitter.Node, scope *TestContext, source []byte) {
	callee := node.ChildByFieldName("function")
	if callee == nil {
		return
	}
	site := callSite{
		ID:       fmt.Sprintf("%s:%d-%d", file, node.StartByte(), node.EndByte()),
		CallerID: caller, Callee: callee.Utf8Text(source), Resolution: "unresolved",
		Site: nodeRange(file, node), TestContext: scope,
	}
	if callee.Kind() == "scoped_identifier" && !callee.HasError() {
		site.directPath = sourcePath(callee, source)
	}
	g.Calls = append(g.Calls, site)
}

// Resolve the implicit Cargo library dependency of a binary in the same
// manifest. Other crate paths and Rust name lookup are left unresolved.
func (g *callGraph) resolve(targets []Target, files []SourceFile, imports []Import, modules []Module) {
	type key struct{ file, name string }
	public := map[key][]functionSymbol{}
	for _, symbol := range g.Symbols {
		if symbol.crateRootPublic && symbol.TestContext == nil {
			k := key{symbol.Definition.File, symbol.Name}
			public[k] = append(public[k], symbol)
		}
	}
	owners := map[string][]string{}
	for _, file := range files {
		owners[file.Path] = file.ComponentIDs
	}
	byID := map[string]Target{}
	libraries := map[string]Target{}
	for _, target := range targets {
		byID[target.ID] = target
		if target.Kind == "lib" {
			libraries[target.Manifest] = target
		}
	}
	for i := range g.Calls {
		site := &g.Calls[i]
		crate, name, ok := strings.Cut(site.directPath, "::")
		ids := owners[site.Site.File]
		if !ok || strings.Contains(name, "::") || len(ids) != 1 {
			continue
		}
		binary := byID[ids[0]]
		lib := libraries[binary.Manifest]
		if binary.Kind != "bin" || lib.Name != crate || lib.File == binary.File || binary.File != site.Site.File || shadowsCrate(site.Site.File, crate, imports, modules) {
			continue
		}
		candidates := public[key{lib.File, name}]
		if len(candidates) == 1 {
			site.Resolution, site.TargetID = "resolved", candidates[0].ID
		}
	}
}

func shadowsCrate(file, name string, imports []Import, modules []Module) bool {
	for _, module := range modules {
		if module.File == file && module.Name == name {
			return true
		}
	}
	for _, item := range imports {
		// An alias, glob, or import of the same final name may shadow the crate.
		if item.File == file && (strings.Contains(item.Expression, "*") ||
			strings.Contains(item.Expression, " as ") || strings.Contains(item.Expression, name)) {
			return true
		}
	}
	return false
}
