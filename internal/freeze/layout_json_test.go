package freeze

import "testing"

func TestParseJSONLayoutMatchesClassic(t *testing.T) {
	classic, err := parseWindowLayout("abcd,80x24,0,0{55x24,0,0[55x11,0,0,0,55x12,0,12,2],24x24,56,0,1}")
	if err != nil {
		t.Fatal(err)
	}
	jsonForm, err := parseWindowLayout(`{"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0,"c":[{"t":"v","w":55,"h":24,"x":0,"y":0,"c":[{"t":"p","w":55,"h":11,"x":0,"y":0,"a":true,"i":0,"I":"%0"},{"t":"p","w":55,"h":12,"x":0,"y":12,"i":1,"I":"%2"}]},{"t":"p","w":24,"h":24,"x":56,"y":0,"i":2,"I":"%1"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	var eq func(a, b *layoutNode) bool
	eq = func(a, b *layoutNode) bool {
		if a.W != b.W || a.H != b.H || a.X != b.X || a.Y != b.Y || a.PaneID != b.PaneID || a.Type != b.Type || len(a.Children) != len(b.Children) {
			return false
		}
		for i := range a.Children {
			if !eq(a.Children[i], b.Children[i]) {
				return false
			}
		}
		return true
	}
	if !eq(classic, jsonForm) {
		t.Error("JSON layout parsed to a different tree than the classic form")
	}
}

func TestParseJSONLayoutErrors(t *testing.T) {
	for _, in := range []string{`{`, `{"V":2}`, `{"L":{"t":"q"}}`, `{"L":{"t":"h"}}`, `{"L":{"t":"p","I":""}}`} {
		if _, err := parseWindowLayout(in); err == nil {
			t.Errorf("parseWindowLayout(%q) succeeded", in)
		}
	}
}
