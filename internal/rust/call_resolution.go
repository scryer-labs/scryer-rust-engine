package rust

import (
	"context"
	"path"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

type moduleKey struct{ file, inline string }

type callImport struct {
	module      moduleKey
	name, path  string
	visibility  string
	conditional bool
}

func (g *callGraph) addImports(module moduleKey, node *sitter.Node, visibility string, attrs []sourceAttribute, source []byte) {
	var collect func(*sitter.Node, string)
	collect = func(node *sitter.Node, prefix string) {
		switch node.Kind() {
		case "scoped_use_list":
			if qualifier := node.ChildByFieldName("path"); qualifier != nil {
				prefix += sourcePath(qualifier, source) + "::"
			}
			if list := node.ChildByFieldName("list"); list != nil {
				collect(list, prefix)
			}
		case "use_list":
			for i := uint(0); i < node.NamedChildCount(); i++ {
				collect(node.NamedChild(i), prefix)
			}
		case "use_as_clause":
			target, alias := node.ChildByFieldName("path"), node.ChildByFieldName("alias")
			if target != nil && alias != nil && alias.Utf8Text(source) != "_" {
				full := prefix + sourcePath(target, source)
				if target.Kind() == "self" && prefix != "" {
					full = strings.TrimSuffix(prefix, "::")
				}
				g.imports = append(g.imports, callImport{module, alias.Utf8Text(source), full, visibility, guarded(attrs)})
			}
		case "identifier", "scoped_identifier", "self", "use_wildcard":
			full := prefix + sourcePath(node, source)
			if node.Kind() == "self" && prefix != "" {
				full = strings.TrimSuffix(prefix, "::")
			}
			name := full
			if i := strings.LastIndex(full, "::"); i >= 0 {
				name = full[i+2:]
			}
			g.imports = append(g.imports, callImport{module, name, full, visibility, guarded(attrs)})
		}
	}
	collect(node, "")
}

type moduleInstance struct {
	visibility  string
	key         moduleKey
	parent      *moduleInstance
	children    map[string][]*moduleInstance
	target      Target
	conditional bool
}

type callIndex struct {
	roots        map[string]*moduleInstance
	instances    map[moduleKey][]*moduleInstance
	functions    map[moduleKey]map[string][]functionSymbol
	imports      map[moduleKey]map[string][]callImport
	blockers     map[moduleKey]map[string]bool
	libraries    map[string]Target
	dependencies map[string]map[string]Target
	moduleNames  map[moduleKey]map[string]bool
}

func (g *callGraph) resolve(ctx context.Context, targets []Target, modules []Module, manifests []Manifest) error {
	index := callIndex{
		roots: map[string]*moduleInstance{}, instances: map[moduleKey][]*moduleInstance{},
		functions: map[moduleKey]map[string][]functionSymbol{}, imports: map[moduleKey]map[string][]callImport{},
		blockers: g.blockers, libraries: map[string]Target{}, moduleNames: map[moduleKey]map[string]bool{},
	}
	declarations := map[moduleKey][]Module{}
	for _, module := range modules {
		key := moduleKey{module.File, module.ParentID}
		declarations[key] = append(declarations[key], module)
		if index.moduleNames[key] == nil {
			index.moduleNames[key] = map[string]bool{}
		}
		index.moduleNames[key][module.Name] = true
	}
	for _, symbol := range g.Symbols {
		if !symbol.moduleFunction {
			continue
		}
		key := moduleKey{symbol.Definition.File, symbol.module}
		if index.functions[key] == nil {
			index.functions[key] = map[string][]functionSymbol{}
		}
		index.functions[key][symbol.Name] = append(index.functions[key][symbol.Name], symbol)
	}
	for _, imp := range g.imports {
		if index.imports[imp.module] == nil {
			index.imports[imp.module] = map[string][]callImport{}
		}
		index.imports[imp.module][imp.name] = append(index.imports[imp.module][imp.name], imp)
	}
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if target.Kind == "lib" {
			index.libraries[target.Manifest] = target
		}
		root := &moduleInstance{key: moduleKey{file: target.File}, target: target, children: map[string][]*moduleInstance{}}
		index.roots[target.ID] = root
		index.addModule(root, declarations, map[moduleKey]bool{})
	}
	index.dependencies = localCrateDependencies(manifests, index.libraries)
	for i := range g.Calls {
		if err := ctx.Err(); err != nil {
			return err
		}
		site := &g.Calls[i]
		if site.directPath == "" || site.shadowed {
			continue
		}
		owners := index.instances[moduleKey{site.Site.File, site.module}]
		var id string
		for _, owner := range owners {
			target := index.resolvePath(owner, strings.Split(site.directPath, "::"), owner, map[lookup]bool{})
			if target == "" || id != "" && id != target {
				id = ""
				break
			}
			id = target
		}
		if id != "" {
			site.TargetID, site.Resolution, site.Reason = id, "resolved", ""
		}
	}
	g.resolveInherentCalls()
	g.resolveTypedCalls(index)
	return nil
}

func (x *callIndex) addModule(instance *moduleInstance, declarations map[moduleKey][]Module, ancestors map[moduleKey]bool) {
	if ancestors[instance.key] {
		return
	}
	ancestors[instance.key] = true
	defer delete(ancestors, instance.key)
	x.instances[instance.key] = append(x.instances[instance.key], instance)
	for _, module := range declarations[instance.key] {
		key := moduleKey{file: module.TargetFile}
		switch module.Resolution {
		case "inline":
			key.inline = module.ID
		case "resolved":
		default:
			continue
		}
		if ancestors[key] {
			continue
		}
		child := &moduleInstance{visibility: module.visibility, key: key, parent: instance, target: instance.target, children: map[string][]*moduleInstance{},
			conditional: instance.conditional || hasConditionalModuleAttributes(module.Attributes)}
		instance.children[module.Name] = append(instance.children[module.Name], child)
		x.addModule(child, declarations, ancestors)
	}
}

func hasConditionalModuleAttributes(attributes []string) bool {
	for _, attr := range attributes {
		if strings.HasPrefix(strings.Join(strings.Fields(attr), ""), "#[cfg") {
			return true
		}
	}
	return false
}

type lookup struct {
	module *moduleInstance
	path   string
	viewer *moduleInstance
}

func (x *callIndex) resolvePath(module *moduleInstance, parts []string, viewer *moduleInstance, seen map[lookup]bool) string {
	if module == nil || len(parts) == 0 || parts[0] == "" {
		return ""
	}
	key := lookup{module, strings.Join(parts, "::"), viewer}
	if seen[key] {
		return ""
	}
	seen[key] = true
	defer delete(seen, key)
	first := parts[0]
	switch first {
	case "crate":
		return x.resolvePath(x.roots[module.target.ID], parts[1:], viewer, seen)
	case "self":
		return x.resolvePath(module, parts[1:], viewer, seen)
	case "super":
		return x.resolvePath(module.parent, parts[1:], viewer, seen)
	}
	imports := x.imports[module.key]
	functions := x.functions[module.key][first]
	if x.blockers[module.key][first] {
		return ""
	}
	if len(parts) == 1 && len(functions) != 0 {
		if len(functions) != 1 || len(imports[first]) != 0 || module.conditional {
			return ""
		}
		fn := functions[0]
		if fn.conditional || !visibleFrom(module, viewer, fn.visibility) {
			return ""
		}
		return fn.ID
	}
	if len(parts) > 1 && x.moduleNames[module.key][first] {
		children := module.children[first]
		if len(children) != 1 || len(imports[first]) != 0 || children[0].conditional || !visibleFrom(module, viewer, children[0].visibility) {
			return ""
		}
		return x.resolvePath(children[0], parts[1:], viewer, seen)
	}
	if matches := imports[first]; len(matches) > 0 {
		if len(matches) != 1 || matches[0].conditional || !visibleFrom(module, viewer, matches[0].visibility) {
			return ""
		}
		imp := matches[0]
		path := strings.Split(imp.path, "::")
		// Relative imports rooted in local identifiers have edition-dependent
		// lookup. Explicit crate/self/super and the Cargo library are supported.
		lib := x.libraries[module.target.Manifest]
		if path[0] != "crate" && path[0] != "self" && path[0] != "super" && path[0] != lib.Name &&
			x.dependencies[module.target.Manifest][path[0]].ID == "" {
			return ""
		}
		return x.resolvePath(module, append(path, parts[1:]...), module, seen)
	}
	if len(imports["*"]) != 0 || len(functions) != 0 {
		return ""
	}
	lib := x.libraries[module.target.Manifest]
	if viewer.target.ID == module.target.ID && len(parts) > 1 {
		if lib.Name == first && lib.ID != module.target.ID && module.target.Kind != "lib" && module.target.Kind != "build" {
			return x.resolvePath(x.roots[lib.ID], parts[1:], viewer, seen)
		}
		if dependency := x.dependencies[module.target.Manifest][first]; dependency.ID != "" && module.target.Kind != "build" {
			return x.resolvePath(x.roots[dependency.ID], parts[1:], viewer, seen)
		}
	}
	return ""
}

// Resolve explicitly declared, unconditional local Cargo dependencies.
func localCrateDependencies(manifests []Manifest, libraries map[string]Target) map[string]map[string]Target {
	result := map[string]map[string]Target{}
	for _, manifest := range manifests {
		switch manifest.Edition {
		case "2018", "2021", "2024":
		default:
			continue
		}
		candidates := map[string]Target{}
		blocked := map[string]bool{}
		for _, dependency := range manifest.Dependencies {
			if dependency.Scope == "build" || dependency.Scope == "workspace" {
				continue
			}
			name := strings.ReplaceAll(dependency.Name, "-", "_")
			declared, _ := dependency.Declared.(map[string]any)
			location, _ := declared["path"].(string)
			if dependency.Scope != "normal" || dependency.Condition != "" || location == "" || path.IsAbs(location) ||
				declared["optional"] == true || declared["workspace"] == true {
				blocked[name] = true
				continue
			}
			library := libraries[path.Join(path.Dir(manifest.File), location, "Cargo.toml")]
			if library.ID == "" || candidates[name].ID != "" {
				blocked[name] = true
				continue
			}
			candidates[name] = library
		}
		for name := range blocked {
			delete(candidates, name)
		}
		result[manifest.File] = candidates
	}
	return result
}

func visibleFrom(module, viewer *moduleInstance, visibility string) bool {
	if visibility == "pub" {
		return true
	}
	if module.target.ID != viewer.target.ID {
		return false
	}
	switch visibility {
	case "pub(crate)":
		return true
	case "pub(super)":
		if module.parent != nil {
			module = module.parent
		}
	case "", "pub(self)":
	default:
		return false
	}
	for scope := viewer; scope != nil; scope = scope.parent {
		if scope == module {
			return true
		}
	}
	return false
}
