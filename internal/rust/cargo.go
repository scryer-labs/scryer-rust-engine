package rust

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Manifest struct {
	File         string       `json:"file"`
	Package      string       `json:"package,omitempty"`
	Workspace    *Workspace   `json:"workspace,omitempty"`
	Targets      []Target     `json:"targets"`
	Dependencies []Dependency `json:"dependencies"`
	Edition      string       `json:"edition,omitempty"`
}

type Workspace struct {
	Members []string `json:"members,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type Target struct {
	ID       string `json:"componentId"`
	Manifest string `json:"manifest"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	File     string `json:"file"`
}

type Dependency struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	Condition string `json:"condition,omitempty"`
	Declared  any    `json:"declared"`
}

type DependencyTables struct {
	Normal map[string]any `toml:"dependencies"`
	Dev    map[string]any `toml:"dev-dependencies"`
	Build  map[string]any `toml:"build-dependencies"`
}

type cargoTarget struct {
	Name string
	Path string
}

func readManifest(fsys fs.FS, file string) (Manifest, error) {
	result := Manifest{File: file, Targets: []Target{}, Dependencies: []Dependency{}}
	data, err := fs.ReadFile(fsys, file)
	if err != nil {
		return result, err
	}
	var document struct {
		Package *struct {
			Name     string
			Edition  any
			Autolib  *bool
			Autobins *bool
			Build    any
		}
		Workspace *struct {
			Members      []string
			Exclude      []string
			Dependencies map[string]any
		}
		Lib     *cargoTarget
		Bin     []cargoTarget
		Test    []cargoTarget
		Bench   []cargoTarget
		Example []cargoTarget
		Target  map[string]DependencyTables
		DependencyTables
	}
	if err := toml.Unmarshal(data, &document); err != nil {
		return result, fmt.Errorf("%s: %w", file, err)
	}
	addDependencies := func(scope, condition string, table map[string]any) {
		names := make([]string, 0, len(table))
		for name := range table {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			result.Dependencies = append(result.Dependencies, Dependency{name, scope, condition, table[name]})
		}
	}
	addTables := func(condition string, tables DependencyTables) {
		addDependencies("normal", condition, tables.Normal)
		addDependencies("dev", condition, tables.Dev)
		addDependencies("build", condition, tables.Build)
	}
	addTables("", document.DependencyTables)
	var conditions []string
	for condition := range document.Target {
		conditions = append(conditions, condition)
	}
	sort.Strings(conditions)
	for _, condition := range conditions {
		addTables(condition, document.Target[condition])
	}
	if w := document.Workspace; w != nil {
		result.Workspace = &Workspace{w.Members, w.Exclude}
		addDependencies("workspace", "", w.Dependencies)
	}
	pkg := document.Package
	if pkg == nil {
		return result, nil
	}
	result.Package = pkg.Name
	result.Edition, _ = pkg.Edition.(string)
	dir := path.Dir(file)
	addTarget := func(kind, name, source string, explicit bool) {
		if source == "" || name == "" || filepath.IsAbs(source) {
			return
		}
		projectFile := path.Join(dir, filepath.ToSlash(source))
		if !fs.ValidPath(projectFile) {
			return
		}
		if !explicit {
			if info, err := fs.Stat(fsys, projectFile); err != nil || !info.Mode().IsRegular() {
				return
			}
		}
		for _, existing := range result.Targets {
			if existing.Kind == kind && existing.Name == name {
				return
			}
		}
		result.Targets = append(result.Targets, Target{file + ":" + kind + ":" + name, file, name, kind, projectFile})
	}
	defaultLib := strings.ReplaceAll(pkg.Name, "-", "_")
	if lib := document.Lib; lib != nil {
		addTarget("lib", fallback(lib.Name, defaultLib), fallback(lib.Path, "src/lib.rs"), true)
	} else if pkg.Autolib == nil || *pkg.Autolib {
		addTarget("lib", defaultLib, "src/lib.rs", false)
	}
	for _, bin := range document.Bin {
		addTarget("bin", fallback(bin.Name, pkg.Name), bin.Path, true)
		if bin.Path == "" {
			if bin.Name == pkg.Name || bin.Name == "" {
				addTarget("bin", pkg.Name, "src/main.rs", false)
			}
			addTarget("bin", bin.Name, "src/bin/"+bin.Name+".rs", false)
			addTarget("bin", bin.Name, "src/bin/"+bin.Name+"/main.rs", false)
		}
	}
	for _, group := range []struct {
		kind  string
		items []cargoTarget
	}{{"test", document.Test}, {"bench", document.Bench}, {"example", document.Example}} {
		for _, target := range group.items {
			addTarget(group.kind, target.Name, target.Path, true)
			if target.Path == "" {
				dir := group.kind + "s/" + target.Name
				addTarget(group.kind, target.Name, dir+".rs", false)
				addTarget(group.kind, target.Name, dir+"/main.rs", false)
			}
		}
	}
	if pkg.Autobins == nil || *pkg.Autobins {
		addTarget("bin", pkg.Name, "src/main.rs", false)
		entries, _ := fs.ReadDir(fsys, path.Join(dir, "src/bin"))
		for _, entry := range entries {
			if entry.IsDir() {
				addTarget("bin", entry.Name(), "src/bin/"+entry.Name()+"/main.rs", false)
			} else if name, ok := strings.CutSuffix(entry.Name(), ".rs"); ok {
				addTarget("bin", name, "src/bin/"+entry.Name(), false)
			}
		}
	}
	switch build := pkg.Build.(type) {
	case string:
		addTarget("build", pkg.Name, build, true)
	case nil:
		addTarget("build", pkg.Name, "build.rs", false)
	}
	return result, nil
}

func fallback(value, defaultValue string) string {
	if value != "" {
		return value
	}
	return defaultValue
}

func projectFS(root string) fs.FS {
	return os.DirFS(root)
}
