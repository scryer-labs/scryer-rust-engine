package tauri

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

func tauriTypes(scope *sitter.Node, source []byte) map[string]bool {
	types := map[string]bool{}
	for i := uint(0); i < scope.NamedChildCount(); i++ {
		node := scope.NamedChild(i)
		if node.Kind() != "use_declaration" {
			continue
		}
		arg := node.ChildByFieldName("argument")
		if arg == nil {
			continue
		}
		if arg.Kind() == "scoped_use_list" {
			prefix, list := arg.ChildByFieldName("path"), arg.ChildByFieldName("list")
			if prefix == nil || list == nil || prefix.Utf8Text(source) != "tauri" {
				continue
			}
			for j := uint(0); j < list.NamedChildCount(); j++ {
				item := list.NamedChild(j)
				if item.Kind() == "identifier" {
					types[item.Utf8Text(source)] = true
				}
			}
		} else if qualifier, name, ok := strings.Cut(arg.Utf8Text(source), "::"); ok && qualifier == "tauri" {
			types[name] = true
		}
	}
	return types
}

func emitterParameters(function *sitter.Node, source []byte, importedTypes map[string]bool) map[string]bool {
	parameters := function.ChildByFieldName("parameters")
	if parameters == nil {
		return nil
	}
	receivers := map[string]bool{}
	for i := uint(0); i < parameters.NamedChildCount(); i++ {
		parameter := parameters.NamedChild(i)
		name := parameter.ChildByFieldName("pattern")
		typ := parameter.ChildByFieldName("type")
		if name == nil || name.Kind() != "identifier" || typ == nil {
			continue
		}
		for typ.Kind() == "reference_type" || typ.Kind() == "generic_type" {
			typ = typ.ChildByFieldName("type")
			if typ == nil {
				break
			}
		}
		if typ == nil {
			continue
		}
		text := typ.Utf8Text(source)
		nameType, qualified := strings.CutPrefix(text, "tauri::")
		if !qualified && !importedTypes[text] {
			continue
		}
		switch nameType {
		case "App", "AppHandle", "WebviewWindow", "Window", "Webview":
			receivers[name.Utf8Text(source)] = true
		}
	}
	return receivers
}
