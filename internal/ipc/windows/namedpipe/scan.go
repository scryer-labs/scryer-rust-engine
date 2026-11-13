// Package namedpipe collects Windows named-pipe evidence from Tokio builder calls.
package namedpipe

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

const tokioPipe = "tokio::net::windows::named_pipe::"

type Location struct {
	File   string `json:"file"`
	Line   uint   `json:"line"`
	Column uint   `json:"column"`
}

type Evidence struct {
	Location
	Operation string `json:"operation"`
	Symbol    string `json:"symbol"`
}

type Endpoint struct {
	Role        string     `json:"role"`
	Name        string     `json:"name"`
	SourceRole  string     `json:"sourceRole"`
	AccessMode  string     `json:"accessMode"`
	AccessFlags string     `json:"accessFlags"`
	Evidence    []Evidence `json:"evidence"`
}

type Collector struct {
	Endpoints []Endpoint `json:"endpoints"`
}

func (c *Collector) Observe(ctx context.Context, file string, root *sitter.Node, source []byte) error {
	return c.walk(ctx, file, root, source, nil)
}

func (c *Collector) walk(ctx context.Context, file string, node *sitter.Node, source []byte, scope *bindings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch node.Kind() {
	case "source_file", "declaration_list":
		scope = readBindings(node, source)
	case "block":
		// Stop resolution across block-local names that could shadow module bindings.
		scope = blockBindings(node, source, scope)
	case "token_tree":
		return nil
	case "call_expression":
		if endpoint, ok := observeCall(file, node, source, scope); ok {
			c.Endpoints = append(c.Endpoints, endpoint)
		}
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		if err := c.walk(ctx, file, node.NamedChild(i), source, scope); err != nil {
			return err
		}
	}
	return nil
}

func observeCall(file string, node *sitter.Node, source []byte, scope *bindings) (Endpoint, bool) {
	receiver, method, arg := methodCall(node, source)
	if method != "open" && method != "create" || arg == nil {
		return Endpoint{}, false
	}
	builder := "ClientOptions"
	role := "client"
	if method == "create" {
		builder, role = "ServerOptions", "server"
	}
	read, write, ok := access(receiver, source, scope, builder)
	if !ok {
		return Endpoint{}, false
	}
	value := arg
	if arg.Kind() == "identifier" {
		value = scope.constant(arg.Utf8Text(source))
	}
	name, ok := stringLiteral(value, source)
	if !ok || !strings.HasPrefix(strings.ToLower(name), `\\.\pipe\`) || len(name) <= len(`\\.\pipe\`) {
		return Endpoint{}, false
	}
	mode := "unknown"
	switch {
	case read == "true" && write == "true":
		mode = "read-write"
	case read == "true" && write == "false":
		mode = "read"
	case read == "false" && write == "true":
		mode = "write"
	}
	return Endpoint{
		Role: role, Name: name, SourceRole: "source", AccessMode: mode, AccessFlags: receiver.Utf8Text(source),
		Evidence: []Evidence{
			{location(file, value), "endpoint-name", name},
			{location(file, node), "native-api", tokioPipe + builder + "::" + method},
		},
	}, true
}

func location(file string, node *sitter.Node) Location {
	pos := node.StartPosition()
	return Location{file, pos.Row + 1, pos.Column + 1}
}

func methodCall(node *sitter.Node, source []byte) (*sitter.Node, string, *sitter.Node) {
	if node == nil || node.Kind() != "call_expression" || node.HasError() {
		return nil, "", nil
	}
	fn := node.ChildByFieldName("function")
	if fn == nil || fn.Kind() != "field_expression" {
		return nil, "", nil
	}
	field := fn.ChildByFieldName("field")
	if field == nil {
		return nil, "", nil
	}
	return fn.ChildByFieldName("value"), field.Utf8Text(source), singleArgument(node.ChildByFieldName("arguments"))
}

func singleArgument(args *sitter.Node) *sitter.Node {
	var result *sitter.Node
	for i := uint(0); args != nil && i < args.NamedChildCount(); i++ {
		child := args.NamedChild(i)
		if comment(child) {
			continue
		}
		if result != nil {
			return nil
		}
		result = child
	}
	return result
}

func access(node *sitter.Node, source []byte, scope *bindings, builder string) (string, string, bool) {
	read, write := "", ""
	for node != nil && node.Kind() == "call_expression" && !node.HasError() {
		receiver, method, arg := methodCall(node, source)
		if receiver == nil {
			fn := node.ChildByFieldName("function")
			args := node.ChildByFieldName("arguments")
			if fn == nil || args == nil || strings.TrimSpace(args.Utf8Text(source)) != "()" {
				break
			}
			name, ok := strings.CutSuffix(pathText(fn, source), "::new")
			if !ok || !scope.isBuilder(name, builder) {
				break
			}
			if read == "" {
				read = "true"
			}
			if write == "" {
				write = "true"
			}
			return read, write, true
		}
		readMethod, writeMethod := "read", "write"
		if builder == "ServerOptions" {
			readMethod, writeMethod = "access_inbound", "access_outbound"
		}
		switch method {
		case readMethod:
			if read == "" {
				read = boolValue(arg, source)
			}
		case writeMethod:
			if write == "" {
				write = boolValue(arg, source)
			}
		default:
			// Unknown builder calls could alter access.
			return "", "", false
		}
		node = receiver
	}
	return "", "", false
}

func boolValue(node *sitter.Node, source []byte) string {
	if node != nil && node.Kind() == "boolean_literal" {
		return node.Utf8Text(source)
	}
	return "unknown"
}

var rawLiteral = regexp.MustCompile(`^r(#{0,255})"`)

func stringLiteral(node *sitter.Node, source []byte) (string, bool) {
	if node == nil || node.HasError() {
		return "", false
	}
	raw := node.Utf8Text(source)
	switch node.Kind() {
	case "raw_string_literal":
		open := rawLiteral.FindStringSubmatch(raw)
		if len(open) != 2 {
			return "", false
		}
		body, ok := strings.CutSuffix(raw[len(open[0]):], `"`+open[1])
		return body, ok
	case "string_literal":
		// Limit decoding to escapes shared by Rust and Go.
		for i := 0; i < len(raw); i++ {
			if raw[i] == '\\' {
				i++
				if i == len(raw) || !strings.ContainsRune(`\\"`, rune(raw[i])) {
					return "", false
				}
			}
		}
		value, err := strconv.Unquote(raw)
		return value, err == nil
	}
	return "", false
}

func comment(node *sitter.Node) bool {
	return node.Kind() == "line_comment" || node.Kind() == "block_comment"
}

func pathText(node *sitter.Node, source []byte) string {
	if comment(node) {
		return ""
	}
	if node.ChildCount() == 0 {
		return node.Utf8Text(source)
	}
	var result strings.Builder
	for i := uint(0); i < node.ChildCount(); i++ {
		result.WriteString(pathText(node.Child(i), source))
	}
	return result.String()
}
