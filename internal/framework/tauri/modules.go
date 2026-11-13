package tauri

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/scryer-labs/scryer-rust-engine/internal/rust"
)

type moduleSite struct {
	file, inline string
}

type moduleRoute struct {
	target, name string
}

// ResolveRegistrations joins handler paths against the Cargo target's explicit
// module tree. Imports, conditional modules and ambiguous owners stay unresolved.
func (c *Collector) ResolveRegistrations(modules []rust.Module, targets []rust.Target) {
	children := map[moduleSite][]rust.Module{}
	for _, module := range modules {
		if module.Resolution == "inline" || module.Resolution == "resolved" {
			children[moduleSite{module.File, module.ParentID}] = append(children[moduleSite{module.File, module.ParentID}], module)
		}
	}
	routes := map[moduleSite][]moduleRoute{}
	var visit func(moduleSite, moduleRoute, map[moduleSite]bool)
	visit = func(site moduleSite, route moduleRoute, seen map[moduleSite]bool) {
		if seen[site] {
			return
		}
		seen[site] = true
		defer delete(seen, site)
		routes[site] = append(routes[site], route)
		for _, child := range children[site] {
			if conditionalModule(child) {
				continue
			}
			next := moduleSite{file: child.TargetFile}
			if child.Resolution == "inline" {
				next.inline = child.ID
			}
			prefix := route.name
			if prefix != "" {
				prefix += "::"
			}
			visit(next, moduleRoute{route.target, prefix + child.Name}, seen)
		}
	}
	for _, target := range targets {
		visit(moduleSite{file: target.File}, moduleRoute{target: target.ID}, map[moduleSite]bool{})
	}
	for i := range c.apps {
		endpoints := c.apps[i].Endpoints
		declarations := map[moduleRoute][]int{}
		for j, endpoint := range endpoints {
			owners := routes[moduleSite{endpoint.File, endpoint.ModuleID}]
			if endpoint.Role == "declaration" && len(owners) == 1 {
				route := owners[0]
				route.name = qualified(route.name, endpoint.Name)
				declarations[route] = append(declarations[route], j)
			}
		}
		for j := range endpoints {
			endpoint := &endpoints[j]
			owners := routes[moduleSite{endpoint.File, endpoint.ModuleID}]
			if endpoint.Role != "registration" || endpoint.ResolutionReason != "" {
				continue
			}
			endpoint.ResolutionReason = "declaration-path-not-proven"
			if len(owners) != 1 {
				continue
			}
			route := owners[0]
			parts := strings.Split(endpoint.Symbol, "::")
			switch parts[0] {
			case "crate":
				route.name = ""
				parts = parts[1:]
			case "self":
				parts = parts[1:]
			case "super":
				for len(parts) > 0 && parts[0] == "super" && route.name != "" {
					last := strings.LastIndex(route.name, "::")
					route.name = route.name[:max(0, last)]
					parts = parts[1:]
				}
			}
			route.name = qualified(route.name, strings.Join(parts, "::"))
			if matches := declarations[route]; len(matches) == 1 {
				declaration := endpoints[matches[0]].Site
				endpoint.Declaration = &declaration
				endpoint.ResolutionReason = ""
			}
		}
	}
}

func qualified(module, name string) string {
	if module == "" {
		return name
	}
	return module + "::" + name
}

func conditionalModule(module rust.Module) bool {
	for _, attr := range module.Attributes {
		if strings.HasPrefix(strings.Join(strings.Fields(attr), ""), "#[cfg") {
			return true
		}
	}
	return false
}

func localPathMayBeShadowed(node *sitter.Node, symbol string) bool {
	for _, prefix := range []string{"crate::", "self::", "super::"} {
		if strings.HasPrefix(symbol, prefix) {
			return false
		}
	}
	for scope := node.Parent(); scope != nil && scope.Kind() != "mod_item"; scope = scope.Parent() {
		if scope.Kind() != "block" {
			continue
		}
		for i := uint(0); i < scope.NamedChildCount(); i++ {
			switch scope.NamedChild(i).Kind() {
			case "use_declaration", "mod_item", "extern_crate_declaration":
				return true
			}
		}
	}
	return false
}
