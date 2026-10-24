package freeze

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// layoutNode is one cell of a parsed tmux window_layout string: either a
// leaf (a single pane) or a container (a list of children split either
// left-right or top-bottom).
type layoutNode struct {
	W, H, X, Y int
	PaneID     string // "%N", set only on leaf nodes
	Type       byte   // '{' (left-right) or '[' (top-bottom); 0 on leaf nodes
	Children   []*layoutNode
}

// parseWindowLayout parses tmux's #{window_layout} value, e.g.
// "2fa8,222x50,0,0{111x50,0,0,0,110x50,112,0,1}": a checksum, then a cell.
// A cell is "WxH,X,Y" followed by either ",<pane-id>" (a leaf) or a
// braced/bracketed, comma-separated list of child cells (a container):
// "{...}" for left-right, "[...]" for top-bottom. This is tmux's own
// serialization (see tmux's layout-custom.c); wyrm only ever reads it back.
//
// tmux 3.8 reports the same tree as JSON instead — see parseJSONLayout — so a
// value starting with "{" is read that way.
func parseWindowLayout(layout string) (*layoutNode, error) {
	if strings.HasPrefix(layout, "{") {
		return parseJSONLayout(layout)
	}
	comma := -1
	for i := 0; i < len(layout); i++ {
		if layout[i] == ',' {
			comma = i
			break
		}
	}
	if comma < 0 {
		return nil, fmt.Errorf("invalid window layout %q: missing checksum", layout)
	}
	s := &layoutScanner{data: layout[comma+1:]}
	n, err := s.parseCell()
	if err != nil {
		return nil, fmt.Errorf("parsing window layout %q: %w", layout, err)
	}
	if s.pos != len(s.data) {
		return nil, fmt.Errorf("parsing window layout %q: unexpected trailing data at position %d", layout, s.pos)
	}
	return n, nil
}

type layoutScanner struct {
	data string
	pos  int
}

func (s *layoutScanner) parseCell() (*layoutNode, error) {
	w, err := s.readInt()
	if err != nil {
		return nil, err
	}
	if err := s.expect('x'); err != nil {
		return nil, err
	}
	h, err := s.readInt()
	if err != nil {
		return nil, err
	}
	if err := s.expect(','); err != nil {
		return nil, err
	}
	x, err := s.readInt()
	if err != nil {
		return nil, err
	}
	if err := s.expect(','); err != nil {
		return nil, err
	}
	y, err := s.readInt()
	if err != nil {
		return nil, err
	}

	n := &layoutNode{W: w, H: h, X: x, Y: y}

	if s.pos >= len(s.data) {
		return nil, fmt.Errorf("unexpected end of input after %dx%d,%d,%d", w, h, x, y)
	}
	switch s.data[s.pos] {
	case ',':
		s.pos++
		id, err := s.readInt()
		if err != nil {
			return nil, err
		}
		n.PaneID = "%" + strconv.Itoa(id)
	case '{', '[':
		open := s.data[s.pos]
		closeCh := byte('}')
		if open == '[' {
			closeCh = ']'
		}
		n.Type = open
		s.pos++
		for {
			child, err := s.parseCell()
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, child)
			if s.pos < len(s.data) && s.data[s.pos] == ',' {
				s.pos++
				continue
			}
			break
		}
		if err := s.expect(closeCh); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unexpected character %q at position %d", s.data[s.pos], s.pos)
	}
	return n, nil
}

func (s *layoutScanner) expect(c byte) error {
	if s.pos >= len(s.data) || s.data[s.pos] != c {
		return fmt.Errorf("expected %q at position %d in %q", c, s.pos, s.data)
	}
	s.pos++
	return nil
}

func (s *layoutScanner) readInt() (int, error) {
	start := s.pos
	for s.pos < len(s.data) && s.data[s.pos] >= '0' && s.data[s.pos] <= '9' {
		s.pos++
	}
	if s.pos == start {
		return 0, fmt.Errorf("expected a number at position %d in %q", start, s.data)
	}
	return strconv.Atoi(s.data[start:s.pos])
}

// jsonLayout is one cell of tmux 3.8's JSON #{window_layout}:
// {"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0,"c":[...]}}. "t" is "h" for a
// left-right container, "v" for top-bottom, or "p" for a pane, whose ID is
// "I".
type jsonLayout struct {
	T        string       `json:"t"`
	W        int          `json:"w"`
	H        int          `json:"h"`
	X        int          `json:"x"`
	Y        int          `json:"y"`
	ID       string       `json:"I"`
	Children []jsonLayout `json:"c"`
	// Index is the pane index. Unused, but declared so encoding/json's
	// case-insensitive matching doesn't try to store it in ID.
	Index int `json:"i"`
}

func parseJSONLayout(layout string) (*layoutNode, error) {
	var doc struct {
		L *jsonLayout `json:"L"`
	}
	if err := json.Unmarshal([]byte(layout), &doc); err != nil {
		return nil, fmt.Errorf("parsing window layout %q: %w", layout, err)
	}
	if doc.L == nil {
		return nil, fmt.Errorf("parsing window layout %q: no layout tree", layout)
	}
	n, err := fromJSON(*doc.L)
	if err != nil {
		return nil, fmt.Errorf("parsing window layout %q: %w", layout, err)
	}
	return n, nil
}

func fromJSON(j jsonLayout) (*layoutNode, error) {
	n := &layoutNode{W: j.W, H: j.H, X: j.X, Y: j.Y}
	switch j.T {
	case "p":
		if !strings.HasPrefix(j.ID, "%") {
			return nil, fmt.Errorf("pane cell without a pane ID")
		}
		n.PaneID = j.ID
		return n, nil
	case "h":
		n.Type = '{'
	case "v":
		n.Type = '['
	default:
		return nil, fmt.Errorf("unknown cell type %q", j.T)
	}
	if len(j.Children) == 0 {
		return nil, fmt.Errorf("container cell with no children")
	}
	for _, c := range j.Children {
		child, err := fromJSON(c)
		if err != nil {
			return nil, err
		}
		n.Children = append(n.Children, child)
	}
	return n, nil
}
