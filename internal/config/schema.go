package config

import (
	"encoding/json"
	"reflect"
	"strings"
)

// SchemaURL is where the published schema lives, for editors that resolve a
// `#:schema` directive (taplo, Even Better TOML).
const SchemaURL = "https://raw.githubusercontent.com/jskoll/wyrm/main/schema/wyrm.schema.json"

// schemaHint adds what reflection can't see: a description, allowed values.
type schemaHint struct {
	desc string
	enum []string
}

// schemaHints is keyed by dotted path; array elements are written "[]".
var schemaHints = map[string]schemaHint{
	"extends":                         {desc: "A base config this one builds on, relative to this file. Session keys set here override the base's; a window with the same name replaces the base's."},
	"session":                         {desc: "The tmux session and its lifecycle hooks."},
	"session.name":                    {desc: "Session name. Defaults to the root directory's name."},
	"session.root":                    {desc: "Session directory. Relative paths resolve against this file's directory; ~ and $VAR expand."},
	"session.on_project_start":        {desc: "Shell run before a fresh build."},
	"session.on_project_first_start":  {desc: "Shell run before the very first build of this project."},
	"session.on_project_restart":      {desc: "Shell run before every build after the first."},
	"session.on_project_attach":       {desc: "Shell run each time wyrm attaches to the session."},
	"session.on_project_detach":       {desc: "Shell run by tmux when a client detaches."},
	"session.on_project_exit":         {desc: "Shell run before `wyrm kill` destroys the session."},
	"session.hook_timeout":            {desc: "Limit for each hook and post_window, as a duration such as \"90s\" or \"5m\"."},
	"session.startup_window":          {desc: "Window to focus after building, by name or index."},
	"session.startup_pane":            {desc: "Pane index to focus in startup_window."},
	"session.env":                     {desc: "Environment variables for every pane and hook."},
	"session.aliases":                 {desc: "Extra names `wyrm <name>` accepts for this project."},
	"session.enable_pane_titles":      {desc: "Show tmux pane border titles."},
	"session.pane_title_position":     {desc: "Where pane titles are drawn.", enum: []string{"top", "bottom"}},
	"session.pane_title_format":       {desc: "tmux format for pane titles."},
	"session.project_dir":             {desc: "Written by `wyrm migrate-config`: the project directory a shared config belongs to."},
	"windows":                         {desc: "The session's windows, in order."},
	"windows[].root":                  {desc: "Window directory, relative to session.root."},
	"windows[].layout":                {desc: "tmux layout for the legacy panes list."},
	"windows[].pre_window":            {desc: "Typed into every pane of the window before its own command."},
	"windows[].post_window":           {desc: "Shell run once every pane of the window exists."},
	"windows[].synchronize":           {desc: "Turn on synchronize-panes for this window."},
	"windows[].synchronize_panes":     {desc: "Deprecated alias for synchronize."},
	"windows[].remain_on_exit":        {desc: "Keep panes open after their process exits."},
	"windows[].panes":                 {desc: "Deprecated flat pane list; use splits."},
	"windows[].splits":                {desc: "The window's split tree."},
	"windows[].splits[].type":         {desc: "Split direction. Omit on the first entry to use the window's initial pane.", enum: []string{"h", "horizontal", "v", "vertical"}},
	"windows[].splits[].size":         {desc: "Percentage of the split pane given to the new one (1-99)."},
	"windows[].splits[].command":      {desc: "Typed into the pane's shell."},
	"windows[].splits[].run":          {desc: "Run as the pane's own process, with no shell underneath."},
	"windows[].splits[].wait_for":     {desc: "Delay command or run until ready: tcp:HOST:PORT, file:PATH, or an http(s) URL."},
	"windows[].splits[].wait_timeout": {desc: "Give up waiting after this long (default 2m)."},
	"windows[].splits[].zoomed":       {desc: "Start this pane zoomed."},
	"windows[].splits[].zoom":         {desc: "Deprecated alias for zoomed."},
	"windows[].splits[].children":     {desc: "Splits within this pane."},
}

// JSONSchema describes a .wyrm.toml for editors. It is generated from Config
// itself, so it lists exactly the keys the decoder accepts.
func JSONSchema() ([]byte, error) {
	g := &schemaGen{defs: map[string]any{}, building: map[reflect.Type]bool{}}
	root := g.schema(reflect.TypeOf(Config{}), "")
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["$id"] = SchemaURL
	root["title"] = "wyrm session config"
	if len(g.defs) > 0 {
		root["$defs"] = g.defs
	}
	return json.MarshalIndent(root, "", "  ")
}

// schemaGen builds a schema, publishing a recursive struct (Split, whose
// children are Splits) once under $defs and referring to it everywhere else.
type schemaGen struct {
	defs     map[string]any
	building map[reflect.Type]bool
}

// recursive lists the struct types published under $defs.
var recursive = map[reflect.Type]bool{reflect.TypeOf(Split{}): true}

func (g *schemaGen) schema(t reflect.Type, path string) map[string]any {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var out map[string]any
	switch t.Kind() {
	case reflect.String:
		out = map[string]any{"type": "string"}
	case reflect.Bool:
		out = map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		out = map[string]any{"type": "integer"}
	case reflect.Map:
		out = map[string]any{"type": "object", "additionalProperties": g.schema(t.Elem(), path+".*")}
	case reflect.Slice:
		out = map[string]any{"type": "array", "items": g.schema(t.Elem(), path+"[]")}
	case reflect.Struct:
		ref := map[string]any{"$ref": "#/$defs/" + t.Name()}
		if recursive[t] && (g.building[t] || g.defs[t.Name()] != nil) {
			return ref
		}
		g.building[t] = true
		props := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("toml"), ",")[0]
			if !f.IsExported() || tag == "" || tag == "-" {
				continue
			}
			childPath := tag
			if path != "" {
				childPath = path + "." + tag
			}
			props[tag] = g.schema(f.Type, childPath)
		}
		delete(g.building, t)
		out = map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if recursive[t] {
			g.defs[t.Name()] = out
			return ref
		}
	default:
		out = map[string]any{}
	}
	if h, ok := schemaHints[path]; ok {
		if h.desc != "" {
			out["description"] = h.desc
		}
		if len(h.enum) > 0 {
			out["enum"] = h.enum
		}
	}
	return out
}
