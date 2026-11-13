package rust

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

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
	ID           string       `json:"moduleId"`
	Name         string       `json:"name"`
	ParentID     string       `json:"parentModuleId,omitempty"`
	TargetFile   string       `json:"targetFile,omitempty"`
	Resolution   string       `json:"resolution"`
	Candidates   []string     `json:"candidates,omitempty"`
	ComponentIDs []string     `json:"componentIds,omitempty"`
	Attributes   []string     `json:"attributes,omitempty"`
	TestContext  *TestContext `json:"testContext,omitempty"`
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
	files       []SourceFile
	modules     []Module
	imports     []Import
	references  []Reference
	sourcePaths map[string]bool
	targetRoots map[string]bool
}

func Scan(ctx context.Context, root string, selected []string) (map[string]any, error) {
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
	if !architecture && !files {
		return facts, nil
	}
	for _, target := range targets {
		s.targetRoots[target.File] = true
	}
	if err := s.scanSources(ctx, fsys); err != nil {
		return nil, err
	}
	s.attachComponents(targets)
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

func (s *scanner) scanSources(ctx context.Context, fsys fs.FS) error {
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
		err = s.collect(ctx, root, entry.Path, dir, "", nil, source)
		tree.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *scanner) collect(ctx context.Context, node *sitter.Node, file, moduleDir, parent string, inherited *TestContext, source []byte) error {
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
		switch child.Kind() {
		case "mod_item":
			name := child.ChildByFieldName("name")
			if name == nil {
				break
			}
			module := Module{Location: loc, Name: name.Utf8Text(source), ParentID: parent, TestContext: scope}
			for _, attr := range attributes {
				module.Attributes = append(module.Attributes, attr.text)
			}
			module.ID = fmt.Sprintf("%s:%d:%d", file, loc.Line, loc.Column)
			if body := child.ChildByFieldName("body"); body != nil {
				module.TargetFile, module.Resolution = file, "inline"
				s.modules = append(s.modules, module)
				if err := s.collect(ctx, body, file, path.Join(moduleDir, module.Name), module.ID, scope, source); err != nil {
					return err
				}
			} else {
				s.resolveModule(&module, moduleDir)
				s.modules = append(s.modules, module)
			}
		case "use_declaration":
			if argument := child.ChildByFieldName("argument"); argument != nil {
				s.imports = append(s.imports, Import{Location: loc, Expression: argument.Utf8Text(source), ModuleID: parent, Paths: importPaths(argument, "", source), TestContext: scope})
			}
		case "inner_attribute_item", "token_tree":
			// Attribute predicates and unexpanded macro tokens are not source references.
		default:
			if value := explicitCratePath(child, source); value != "" {
				s.references = append(s.references, Reference{loc, value, parent, scope})
			} else if err := s.collect(ctx, child, file, moduleDir, parent, scope, source); err != nil {
				return err
			}
		}
		attributes = nil
	}
	return nil
}

func (s *scanner) resolveModule(module *Module, moduleDir string) {
	module.Resolution = "unresolved"
	for _, attr := range module.Attributes {
		compact := strings.Join(strings.Fields(attr), "")
		if strings.HasPrefix(compact, "#[path=") || strings.HasPrefix(compact, "#[cfg_attr(") {
			return
		}
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
		module.Resolution = "ambiguous"
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
