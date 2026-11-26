package rust

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/scryer-labs/scryer-rust-engine/internal/analysis"
	sitter "github.com/tree-sitter/go-tree-sitter"
	grammar "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

type Location struct {
	File   string `json:"file"`
	Line   uint   `json:"line"`
	Column uint   `json:"column"`
}

type Module struct {
	Location
	ID               string       `json:"moduleId"`
	Name             string       `json:"name"`
	ParentID         string       `json:"parentModuleId,omitempty"`
	TargetFile       string       `json:"targetFile,omitempty"`
	Resolution       string       `json:"resolution"`
	Candidates       []string     `json:"candidates,omitempty"`
	ResolutionReason string       `json:"resolutionReason,omitempty"`
	ComponentIDs     []string     `json:"componentIds,omitempty"`
	Attributes       []string     `json:"attributes,omitempty"`
	TestContext      *TestContext `json:"testContext,omitempty"`
	visibility       string
}

type Import struct {
	Location
	Expression  string       `json:"expression"`
	ModuleID    string       `json:"moduleId,omitempty"`
	Paths       []string     `json:"paths"`
	TestContext *TestContext `json:"testContext,omitempty"`
}

type SourceFile struct {
	Path         string   `json:"path"`
	ComponentIDs []string `json:"componentIds"`
	SyntaxErrors bool     `json:"syntaxErrors,omitempty"`
}

type scanner struct {
	graph       *callGraph
	files       []SourceFile
	modules     []Module
	imports     []Import
	references  []Reference
	sourcePaths map[string]bool
	targetRoots map[string]bool
}

// SourceObserver sees each parsed source file while its syntax tree is valid.
type SourceObserver func(context.Context, string, *sitter.Node, []byte) error

func Scan(ctx context.Context, root string, selected []string, observers ...SourceObserver) (map[string]any, error) {
	fsys := os.DirFS(root)
	manifests := []Manifest{}
	targets := []Target{}
	s := scanner{files: []SourceFile{}, modules: []Module{}, imports: []Import{}, references: []Reference{}, sourcePaths: map[string]bool{}, targetRoots: map[string]bool{}}
	err := fs.WalkDir(fsys, ".", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".orrery", ".scryer", "target", "node_modules":
				return fs.SkipDir
			}
		} else if entry.Type().IsRegular() {
			switch {
			case entry.Name() == "Cargo.toml":
				manifest, err := readManifest(fsys, file)
				if err != nil {
					return err
				}
				manifests = append(manifests, manifest)
				targets = append(targets, manifest.Targets...)
			case path.Ext(file) == ".rs":
				s.sourcePaths[file] = true
				s.files = append(s.files, SourceFile{Path: file, ComponentIDs: []string{}})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	facts := map[string]any{}
	if slices.Contains(selected, "dependencies") {
		facts["dependencies"] = map[string]any{"basis": "cargo-declarations", "manifests": manifests}
	}
	architecture := slices.Contains(selected, "architecture")
	files := slices.Contains(selected, "files")
	if slices.Contains(selected, "call-graph") {
		s.graph = &callGraph{ProjectID: root, Basis: "tree-sitter-syntax", Limitations: []string{"type-directed-dispatch", "registry-and-git-crate-symbols", "macro-expansion", "active-cargo-features"}, Symbols: []functionSymbol{}, Calls: []callSite{}, blockers: map[moduleKey]map[string]bool{}, localTypes: map[moduleKey]map[string]typeEvidence{}}
	}
	collect := architecture || files || s.graph != nil
	if !collect && len(observers) == 0 {
		return facts, nil
	}
	for _, target := range targets {
		s.targetRoots[target.File] = true
	}
	if err := s.scanSources(ctx, fsys, collect, observers); err != nil {
		return nil, err
	}
	if collect {
		s.attachComponents(targets)
		s.inheritExternalTestContexts(targets)
	}
	if s.graph != nil {
		if err := s.graph.resolve(ctx, targets, s.modules, manifests); err != nil {
			return nil, err
		}
		facts["call-graph"] = s.graph
	}
	if architecture {
		facts["architecture"] = map[string]any{
			"projectId": root, "basis": "cargo-and-rust-declarations", "components": targets, "modules": s.modules,
		}
	}
	if files {
		facts["files"] = map[string]any{"projectId": root, "files": s.files, "modules": s.modules, "imports": s.imports, "references": s.references}
	}
	return facts, nil
}

func (s *scanner) scanSources(ctx context.Context, fsys fs.FS, collect bool, observers []SourceObserver) error {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(grammar.Language())); err != nil {
		return err
	}
	for i := range s.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := &s.files[i]
		source, err := fs.ReadFile(fsys, entry.Path)
		if err != nil {
			return err
		}
		tree := parser.ParseWithOptions(func(offset int, _ sitter.Point) []byte {
			return source[offset:]
		}, nil, &sitter.ParseOptions{ProgressCallback: func(sitter.ParseState) bool { return ctx.Err() != nil }})
		if tree == nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("could not parse %s", entry.Path)
		}
		root := tree.RootNode()
		entry.SyntaxErrors = root.HasError()
		dir := path.Dir(entry.Path)
		if !s.targetRoots[entry.Path] && path.Base(entry.Path) != "mod.rs" {
			dir = strings.TrimSuffix(entry.Path, ".rs")
		}
		if collect {
			err = s.collect(ctx, root, entry.Path, dir, "", "", nil, source)
		}
		for _, observe := range observers {
			if err != nil {
				break
			}
			err = observe(ctx, entry.Path, root, source)
		}
		if s.graph != nil {
			s.graph.contexts = append(s.graph.contexts, analysis.ImpactContext{Path: entry.Path, Fingerprint: analysis.Hash([]byte(s.graph.contextTokens(entry.Path, root, source)))})
		}
		tree.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *scanner) collect(ctx context.Context, node *sitter.Node, file, moduleDir, parent, caller string, inherited *TestContext, source []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var attributes []sourceAttribute
	for i := uint(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		pos := child.StartPosition()
		loc := Location{file, pos.Row + 1, pos.Column + 1}
		switch child.Kind() {
		case "attribute_item":
			attributes = append(attributes, sourceAttribute{child, child.Utf8Text(source)})
			continue
		case "line_comment", "block_comment":
			continue
		}
		scope := testContext(inherited, attributes, child, file, source)
		if s.graph != nil && moduleContainer(node) {
			s.graph.block(moduleKey{file, parent}, child, source)
			s.graph.recordType(moduleKey{file, parent}, child, attributes, source)
		}
		switch child.Kind() {
		case "mod_item":
			name := child.ChildByFieldName("name")
			if name == nil {
				break
			}
			module := Module{Location: loc, Name: name.Utf8Text(source), ParentID: parent, TestContext: scope}
			module.visibility = itemVisibility(child, source)
			for _, attr := range attributes {
				module.Attributes = append(module.Attributes, attr.text)
			}
			module.ID = fmt.Sprintf("%s:%d:%d", file, loc.Line, loc.Column)
			if body := child.ChildByFieldName("body"); body != nil {
				module.TargetFile, module.Resolution = file, "inline"
				s.modules = append(s.modules, module)
				if err := s.collect(ctx, body, file, path.Join(moduleDir, module.Name), module.ID, "", scope, source); err != nil {
					return err
				}
			} else {
				s.resolveModule(&module, moduleDir, attributes, source)
				s.modules = append(s.modules, module)
			}
		case "use_declaration":
			if argument := child.ChildByFieldName("argument"); argument != nil {
				if s.graph != nil && moduleContainer(node) {
					s.graph.addImports(moduleKey{file, parent}, argument, itemVisibility(child, source), attributes, source)
				}
				s.imports = append(s.imports, Import{Location: loc, Expression: argument.Utf8Text(source), ModuleID: parent, Paths: importPaths(argument, "", source), TestContext: scope})
			}
		case "inner_attribute_item", "token_tree":
			// Attribute predicates and unexpanded macro tokens are not source references.
		default:
			nestedCaller := caller
			if s.graph != nil {
				switch child.Kind() {
				case "function_item", "closure_expression":
					nestedCaller = s.graph.symbol(file, parent, child, scope, attributes, source)
				case "call_expression":
					s.graph.call(file, caller, parent, child, scope, source)
				}
			}
			if value := explicitCratePath(child, source); value != "" {
				s.references = append(s.references, Reference{loc, value, parent, scope})
			} else if err := s.collect(ctx, child, file, moduleDir, parent, nestedCaller, scope, source); err != nil {
				return err
			}
		}
		attributes = nil
	}
	return nil
}

func (s *scanner) resolveModule(module *Module, moduleDir string, attrs []sourceAttribute, source []byte) {
	module.Resolution = "unresolved"
	var explicit string
	for _, attr := range attrs {
		node := attr.node.NamedChild(0)
		if node == nil || node.NamedChildCount() == 0 {
			continue
		}
		switch node.NamedChild(0).Utf8Text(source) {
		case "cfg_attr":
			module.ResolutionReason = "conditional-module-path"
			return
		case "path":
			value := node.ChildByFieldName("value")
			if value == nil || value.Kind() != "string_literal" || explicit != "" ||
				json.Unmarshal([]byte(value.Utf8Text(source)), &explicit) != nil || explicit == "" ||
				path.IsAbs(explicit) || strings.Contains(explicit, "\\") {
				module.ResolutionReason = "unsupported-module-path"
				return
			}
		}
	}
	if explicit != "" {
		base := path.Dir(module.File)
		if module.ParentID != "" {
			base = moduleDir
		}
		candidate := path.Join(base, explicit)
		if !s.sourcePaths[candidate] {
			module.ResolutionReason = "module-file-not-found"
			return
		}
		module.TargetFile, module.Resolution = candidate, "resolved"
		return
	}

	for _, candidate := range []string{
		path.Join(moduleDir, module.Name+".rs"), path.Join(moduleDir, module.Name, "mod.rs"),
	} {
		if s.sourcePaths[candidate] {
			module.Candidates = append(module.Candidates, candidate)
		}
	}
	switch len(module.Candidates) {
	case 1:
		module.TargetFile, module.Resolution, module.Candidates = module.Candidates[0], "resolved", nil
	case 2:
		module.Resolution, module.ResolutionReason = "ambiguous", "multiple-module-files"
	default:
		module.ResolutionReason = "module-file-not-found"
	}
}

func (s *scanner) attachComponents(targets []Target) {
	modulesByFile := map[string][]string{}
	for _, module := range s.modules {
		if module.Resolution == "resolved" {
			modulesByFile[module.File] = append(modulesByFile[module.File], module.TargetFile)
		}
	}
	owners := map[string][]string{}
	for _, target := range targets {
		pending := []string{target.File}
		seen := map[string]bool{}
		for i := 0; i < len(pending); i++ {
			file := pending[i]
			if !s.sourcePaths[file] || seen[file] {
				continue
			}
			seen[file] = true
			owners[file] = append(owners[file], target.ID)
			pending = append(pending, modulesByFile[file]...)
		}
	}
	for i, file := range s.files {
		if ids := owners[file.Path]; len(ids) > 0 {
			s.files[i].ComponentIDs = ids
		}
	}
	for i, module := range s.modules {
		s.modules[i].ComponentIDs = owners[module.File]
	}
}
