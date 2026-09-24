package main

import "testing"

func TestTabShortLabel(t *testing.T) {
	gw := &guiWin{}
	a, b, c := &guiTab{gw: gw}, &guiTab{gw: gw, customTitle: "server logs"}, &guiTab{gw: gw, customTitle: "étape"}
	gw.tabs = []*guiTab{a, b, c}
	for _, tt := range []struct {
		tab  *guiTab
		want string
	}{{a, "1"}, {b, "S"}, {c, "É"}} {
		if got := tt.tab.shortLabel(); got != tt.want {
			t.Errorf("shortLabel = %q, want %q", got, tt.want)
		}
	}
	gw.tabs = []*guiTab{b, a} // numbers follow position
	if got := a.shortLabel(); got != "2" {
		t.Errorf("after reorder: %q, want 2", got)
	}
}

func TestIsCollapsedOnlyForSidebar(t *testing.T) {
	gw := &guiWin{collapsed: true}
	for pos, want := range map[string]bool{"left": true, "right": true, "top": false, "bottom": false} {
		gw.cfg.Tabs.Position = pos
		if got := gw.isCollapsed(); got != want {
			t.Errorf("%s: isCollapsed = %v, want %v", pos, got, want)
		}
	}
}

func TestRenamedTitle(t *testing.T) {
	for _, tt := range []struct {
		name, prev, initial, entered, want string
	}{
		{"unchanged automatic title stays automatic", "", "vamsi@host:~", "vamsi@host:~", ""},
		{"unchanged custom title stays", "logs", "logs", " logs ", "logs"},
		{"typed name is kept", "", "vamsi@host:~", "build", "build"},
		{"cleared returns to automatic", "logs", "logs", "  ", ""},
	} {
		if got := renamedTitle(tt.prev, tt.initial, tt.entered); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestCollectBorders: a vertical split whose right side is split
// horizontally yields both separators, with the part next to the active
// pane marked.
func TestCollectBorders(t *testing.T) {
	a := &Pane{x: 0, y: 0, w: 50, h: 30}
	b := &Pane{x: 51, y: 0, w: 49, h: 15}
	c := &Pane{x: 51, y: 16, w: 49, h: 14}
	right := &Node{x: 51, y: 0, w: 49, h: 30, dir: splitHorizontal,
		left: newLeaf(b, 51, 0, 49, 15), right: newLeaf(c, 51, 16, 49, 14)}
	root := &Node{x: 0, y: 0, w: 100, h: 30, dir: splitVertical,
		left: newLeaf(a, 0, 0, 50, 30), right: right}

	var got []guiBorder
	collectBorders(root, c, &got)
	want := []guiBorder{
		{vertical: false, at: 15, from: 51, to: 100},               // b | c separator
		{vertical: false, at: 15, from: 51, to: 100, active: true}, // next to c
		{vertical: true, at: 50, from: 0, to: 30},                  // a | right separator
		{vertical: true, at: 50, from: 16, to: 30, active: true},   // beside c only
	}
	if len(got) != len(want) {
		t.Fatalf("got %d borders: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("border %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
