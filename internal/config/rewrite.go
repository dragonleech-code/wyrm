package config

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/pelletier/go-toml/v2/unstable"
)

// RootNeedsAbsolute reports whether a root depends on the config's directory.
func RootNeedsAbsolute(root string) bool {
	expanded, err := ExpandPath(root)
	return err == nil && !filepath.IsAbs(expanded)
}

// RewriteSessionRoot preserves formatting and comments while replacing root.
// Invalid input is left untouched; migration uses RewriteSessionString's error.
func RewriteSessionRoot(data []byte, newRoot string) []byte {
	out, err := RewriteSessionString(data, "root", newRoot)
	if err != nil {
		return data
	}
	return out
}

// RewriteSessionString uses TOML syntax ranges, including quoted/dotted keys,
// inline tables and multiline values. It changes only the requested value.
func RewriteSessionString(data []byte, key, value string) ([]byte, error) {
	var parser unstable.Parser
	parser.Reset(data)
	want := "session." + key
	var table []string
	start, end, insert := -1, -1, -1
	inline, inlineNonempty := false, false
	var visit func(*unstable.Node, []string)
	visit = func(n *unstable.Node, prefix []string) {
		parts := slices.Clone(prefix)
		it := n.Key()
		for it.Next() {
			parts = append(parts, string(it.Node().Data))
		}
		v := n.Value()
		if slices.Equal(parts, []string{"session", key}) {
			if v.Kind != unstable.String {
				return
			}
			start, end = int(v.Raw.Offset), int(v.Raw.Offset+v.Raw.Length)
		}
		if v.Kind == unstable.InlineTable {
			children := v.Children()
			if slices.Equal(parts, []string{"session"}) {
				insert, inline = int(v.Raw.Offset)+1, true
				inlineNonempty = v.Child() != nil
			}
			for children.Next() {
				if children.Node().Kind == unstable.KeyValue {
					visit(children.Node(), parts)
				}
			}
		}
	}
	for parser.NextExpression() {
		n := parser.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			var parts []string
			it := n.Key()
			last := 0
			for it.Next() {
				parts = append(parts, string(it.Node().Data))
				last = int(it.Node().Raw.Offset + it.Node().Raw.Length)
			}
			table = parts
			if n.Kind == unstable.ArrayTable {
				table = append([]string{"[]"}, table...)
			}
			if slices.Equal(table, []string{"session"}) {
				insert = len(data)
				if i := bytes.IndexByte(data[last:], '\n'); i >= 0 {
					insert = last + i + 1
				}
			}
		case unstable.KeyValue:
			visit(n, table)
		}
	}
	if err := parser.Error(); err != nil {
		return nil, fmt.Errorf("rewriting session.%s: %w", key, err)
	}
	text := tomlQuote(value)
	if start >= 0 {
		return splice(data, start, end, text), nil
	}
	if insert < 0 {
		return splice(data, 0, 0, want+" = "+text+"\n"), nil
	}
	text = key + " = " + text
	if inline {
		if inlineNonempty {
			text += ", "
		}
	} else {
		if insert > 0 && data[insert-1] != '\n' {
			text = "\n" + text
		}
		text += "\n"
	}
	return splice(data, insert, insert, text), nil
}

func splice(data []byte, start, end int, text string) []byte {
	out := append([]byte(nil), data[:start]...)
	out = append(out, text...)
	return append(out, data[end:]...)
}
