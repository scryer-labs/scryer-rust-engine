// Package tauri collects source evidence for the Rust side of Tauri IPC.
package tauri

import (
	"context"
	"io/fs"
	"path"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

type Site struct {
	File   string `json:"file"`
	Line   uint   `json:"line"`
	Column uint   `json:"column"`
}

type Endpoint struct {
	Site
	Name   string `json:"name"`
	Role   string `json:"role"`
	Kind   string `json:"kind"`
	Symbol string `json:"symbol,omitempty"`
}

type App struct {
	Config    string     `json:"config"`
	Endpoints []Endpoint `json:"endpoints"`
}

type Collector struct {
	fs          fs.FS
	apps        []App
	appByConfig map[string]int
	configByDir map[string]string
}

func New(fsys fs.FS) *Collector {
	return &Collector{fs: fsys, apps: []App{}, appByConfig: map[string]int{}, configByDir: map[string]string{}}
}

func (c *Collector) Facts() map[string]any {
	return map[string]any{"basis": "static-source", "tauri": map[string]any{"apps": c.apps}}
}

func (c *Collector) Observe(ctx context.Context, file string, root *sitter.Node, source []byte) error {
	config := c.config(path.Dir(file))
	if config == "" {
		return nil
	}
	index, ok := c.appByConfig[config]
	if !ok {
		index = len(c.apps)
		c.appByConfig[config] = index
		c.apps = append(c.apps, App{Config: config, Endpoints: []Endpoint{}})
	}
	endpoints := &c.apps[index].Endpoints
	appendEndpoint := func(node *sitter.Node, kind, role, name, symbol string) {
		pos := node.StartPosition()
		*endpoints = append(*endpoints, Endpoint{Site{file, pos.Row + 1, pos.Column + 1}, name, role, kind, symbol})
	}
	return observe(ctx, root, source, appendEndpoint, nil, nil)
}

func (c *Collector) config(dir string) string {
	if config, found := c.configByDir[dir]; found {
		return config
	}
	var config string
	for _, name := range []string{"tauri.conf.json", "tauri.conf.json5", "Tauri.toml"} {
		candidate := path.Join(dir, name)
		if info, err := fs.Stat(c.fs, candidate); err == nil && info.Mode().IsRegular() {
			config = candidate
			break
		}
	}
	if config == "" && dir != "." {
		config = c.config(path.Dir(dir))
	}
	c.configByDir[dir] = config
	return config
}

type record func(node *sitter.Node, kind, role, name, symbol string)

func observe(ctx context.Context, node *sitter.Node, source []byte, add record, receiverTypes, importedTypes map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if node.Kind() == "source_file" || node.Kind() == "declaration_list" {
		importedTypes = tauriTypes(node, source)
	}
	var command *sitter.Node
	for i := uint(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		switch child.Kind() {
		case "attribute_item":
			if attribute := child.NamedChild(0); attribute != nil && !attribute.HasError() {
				if name := attribute.NamedChild(0); name != nil && name.Utf8Text(source) == "tauri::command" &&
					attribute.ChildByFieldName("arguments") == nil && attribute.ChildByFieldName("value") == nil {
					command = child
				}
			}
			continue
		case "line_comment", "block_comment":
			continue
		case "token_tree":
			command = nil
			continue
		case "function_item":
			if command != nil {
				if name := child.ChildByFieldName("name"); name != nil && !child.HasError() {
					symbol := name.Utf8Text(source)
					add(command, "command", "declaration", symbol, symbol)
				}
			}
			if err := observe(ctx, child, source, add, emitterParameters(child, source, importedTypes), importedTypes); err != nil {
				return err
			}
			command = nil
			continue
		case "call_expression":
			observeCall(child, source, add, receiverTypes)
		}
		command = nil
		if err := observe(ctx, child, source, add, receiverTypes, importedTypes); err != nil {
			return err
		}
	}
	return nil
}

func observeCall(call *sitter.Node, source []byte, add record, receiverTypes map[string]bool) {
	method := call.ChildByFieldName("function")
	args := call.ChildByFieldName("arguments")
	if method == nil || method.Kind() != "field_expression" || args == nil || call.HasError() {
		return
	}
	field := method.ChildByFieldName("field")
	receiver := method.ChildByFieldName("value")
	if field == nil || receiver == nil {
		return
	}
	var first *sitter.Node
	for i := uint(0); i < args.NamedChildCount(); i++ {
		child := args.NamedChild(i)
		if child.Kind() != "line_comment" && child.Kind() != "block_comment" {
			first = child
			break
		}
	}
	if first == nil {
		return
	}
	switch field.Utf8Text(source) {
	case "invoke_handler":
		macro := first.ChildByFieldName("macro")
		if first.Kind() == "macro_invocation" && macro != nil && macro.Utf8Text(source) == "tauri::generate_handler" {
			handlers(first, source, add)
		}
	case "emit":
		if receiver.Kind() == "identifier" && receiverTypes[receiver.Utf8Text(source)] {
			if name := eventName(first, source); name != "" {
				add(call, "event", "emit", name, "")
			}
		}
	}
}

func handlers(macro *sitter.Node, source []byte, add record) {
	args := macro.NamedChild(macro.NamedChildCount() - 1)
	if args == nil || args.Kind() != "token_tree" {
		return
	}
	var parts []string
	var first *sitter.Node
	flush := func() {
		if len(parts)%2 == 1 {
			symbol := strings.Join(parts, "")
			add(first, "command", "registration", parts[len(parts)-1], symbol)
		}
		parts, first = nil, nil
	}
	valid := true
	for i := uint(1); i+1 < args.ChildCount(); i++ {
		child := args.Child(i)
		switch child.Kind() {
		case "line_comment", "block_comment":
			continue
		case ",":
			if valid {
				flush()
			}
			parts, first, valid = nil, nil, true
		default:
			if !valid {
				continue
			}
			if len(parts)%2 == 0 && child.Kind() != "identifier" || len(parts)%2 == 1 && child.Kind() != "::" {
				valid = false
				continue
			}
			if first == nil {
				first = child
			}
			parts = append(parts, child.Utf8Text(source))
		}
	}
	if valid {
		flush()
	}
}

func eventName(node *sitter.Node, source []byte) string {
	if node.Kind() != "string_literal" {
		return ""
	}
	value := strings.Trim(node.Utf8Text(source), "\"")
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_/:", r)) {
			return ""
		}
	}
	return value
}
