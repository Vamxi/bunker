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
