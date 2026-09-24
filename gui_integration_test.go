package main

// GUI integration tests: real bunker windows on the test display, driven
// through the same entry points as a user (key handler, config file, tab
// actions). One test per shipped feature; see TESTING.md.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

const (
	ctrl  = gdk.ControlMask
	shift = gdk.ShiftMask
	alt   = gdk.AltMask
)

// Rendering: shell output reaches the screen, and the window chrome takes
// the terminal theme's colours (header bar, terminal area).
func TestGUI_RendersOutputAndThemedChrome(t *testing.T) {
	w := newTestWin(t, "printf 'hello bunker'; exec sleep 30", map[string]string{"theme": `"nord"`})
	p := w.panes()[0]
	w.waitDrawn(p, "hello bunker")

	img := w.screenshot()
	var bg interface{ RGB() (int32, int32, int32) }
	onMain(func() { bg = w.cfg.Theme.bg })
	b := img.Bounds()
	if px := img.At(b.Dx()/2, 10); !sameRGB(px, bg) {
		t.Errorf("header bar pixel %v is not the theme background", px)
	}
	if px := img.At(b.Dx()-20, b.Dy()-20); !sameRGB(px, bg) {
		t.Errorf("terminal area pixel %v is not the theme background", px)
	}
}

// Tabs: open, switch, close, numbering, and new-tab via the real key path.
func TestGUI_Tabs(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", nil)
	w.key(gdk.KEY_T, ctrl|shift) // Ctrl+Shift+T
	w.key(gdk.KEY_T, ctrl|shift)
	onMain(func() {
		if len(w.tabs) != 3 || w.active != w.tabs[2] {
			t.Fatalf("tabs=%d active=%d, want 3 tabs with the last active", len(w.tabs), indexOf(w.tabs, w.active))
		}
		w.selectRelative(1) // wraps to the first
		if w.active != w.tabs[0] {
			t.Errorf("next tab from the last should wrap to the first")
		}
		w.closeTab(w.tabs[1])
		if len(w.tabs) != 2 || w.tabs[1].shortLabel() != "2" {
			t.Errorf("after closing the middle tab: %d tabs, second labelled %q", len(w.tabs), w.tabs[1].shortLabel())
		}
	})
	w.key(gdk.KEY_W, ctrl|shift) // Ctrl+Shift+W closes the active tab
	onMain(func() {
		if len(w.tabs) != 1 {
			t.Errorf("Ctrl+Shift+W left %d tabs", len(w.tabs))
		}
	})
}

// Tab strip layout: collapse to one character, positions, autohide.
func TestGUI_TabStripLayout(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", map[string]string{"tabs.width": "240"})
	onMain(func() {
		width, _ := w.stripScroll.SizeRequest()
		if width != 240 || !w.sidebarBtn.IsVisible() {
			t.Errorf("sidebar width %d (want 240), toggle visible %v", width, w.sidebarBtn.IsVisible())
		}
		w.win.ActivateAction("toggle-tabs", nil)
		if width, _ = w.stripScroll.SizeRequest(); width != guiCollapsedWidth || !w.active.short.IsVisible() || w.active.title.IsVisible() {
			t.Errorf("collapsed: width %d, short label %v, title %v", width, w.active.short.IsVisible(), w.active.title.IsVisible())
		}
	})
	writeKey(t, w.path, "tabs", "position", `"top"`)
	onMain(func() {
		w.reload()
		if w.layout.Orientation() != gtk.OrientationVertical || w.sidebarBtn.IsVisible() || w.isCollapsed() {
			t.Errorf("top bar: orientation %v, toggle visible %v, collapsed %v", w.layout.Orientation(), w.sidebarBtn.IsVisible(), w.isCollapsed())
		}
		if w.active.title.XAlign() != 0.5 {
			t.Errorf("bar titles should be centred, xalign %v", w.active.title.XAlign())
		}
	})
	writeKey(t, w.path, "tabs", "autohide", "true")
	onMain(func() {
		w.reload()
		if w.stripScroll.IsVisible() {
			t.Errorf("autohide with one tab should hide the strip")
		}
	})
}

// Renaming: typed names stick, untouched text does not, renaming from the
// collapsed sidebar expands and re-collapses, Reset Name restores.
func TestGUI_RenameTab(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", nil)
	onMain(func() {
		tab := w.active
		tab.startRename()
		tab.finishRename(true) // clicked away without typing
		if tab.customTitle != "" {
			t.Errorf("untouched rename set a name: %q", tab.customTitle)
		}
		tab.startRename()
		tab.entry.SetText("server logs")
		tab.finishRename(true)
		if tab.customTitle != "server logs" || tab.title.Text() != "server logs" {
			t.Errorf("rename: custom %q, shown %q", tab.customTitle, tab.title.Text())
		}

		w.setCollapsed(true)
		tab.startRename() // menu path: expands for the edit
		if w.isCollapsed() || !tab.entry.IsVisible() {
			t.Errorf("rename from collapsed should expand and show the entry")
		}
		tab.finishRename(false)
		if !w.isCollapsed() || tab.short.Text() != "S" {
			t.Errorf("after rename: collapsed %v, short %q", w.isCollapsed(), tab.short.Text())
		}
		tab.row.ActivateAction("tab.reset-name", nil)
		if tab.customTitle != "" || tab.short.Text() != "1" {
			t.Errorf("reset name: custom %q, short %q", tab.customTitle, tab.short.Text())
		}
	})
}

// Config: edits apply live through the directory monitor (editor-style
// rename save); an invalid file keeps the previous settings and shows the
// banner.
func TestGUI_ConfigLiveReload(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", map[string]string{"theme": `"default"`})
	data, err := os.ReadFile(w.path)
	if err != nil {
		t.Fatal(err)
	}
	next := setTOMLKey(string(data), "", "theme", `"dracula"`)
	next = setTOMLKey(next, "window", "padding", "20")
	if err := os.WriteFile(w.path+".new", []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(w.path+".new", w.path); err != nil {
		t.Fatal(err)
	}
	waitMain(t, 5*time.Second, "reload after an editor-style save", func() bool {
		return w.cfg.ThemeName == "dracula" && w.active.view.pad == 20
	})

	if err := os.WriteFile(w.path, []byte("theme = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitMain(t, 5*time.Second, "error banner", func() bool { return w.banner.IsVisible() })
	onMain(func() {
		if w.cfg.ThemeName != "dracula" {
			t.Errorf("invalid file replaced the config: theme %q", w.cfg.ThemeName)
		}
		if !strings.Contains(w.banner.Text(), "keeping previous settings") {
			t.Errorf("banner text %q", w.banner.Text())
		}
	})
}

// Preferences: controls reflect the config and write single keys back.
func TestGUI_Preferences(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", map[string]string{"theme": `"nord"`, "tabs.position": `"right"`})
	onMain(func() {
		w.openSettings()
		s := w.settings
		if s.themes[s.theme.Selected()] != "nord" || tabPositions[s.tabsPos.Selected()] != "right" {
			t.Errorf("settings show theme %q, position %q", s.themes[s.theme.Selected()], tabPositions[s.tabsPos.Selected()])
		}
		if got := s.keyLabels["split"].Text(); got != "f1" {
			t.Errorf("keyboard page shows split = %q", got)
		}
		for i, name := range s.themes {
			if name == "dracula" {
				s.theme.SetSelected(uint(i))
			}
		}
		s.padding.SetValue(14)
	})
	data, _ := os.ReadFile(w.path)
	if !strings.Contains(string(data), `theme = "dracula"`) || !strings.Contains(string(data), "padding = 14") {
		t.Errorf("settings did not write the keys:\n%s", data)
	}
	if !strings.Contains(string(data), "# bunker configuration") {
		t.Errorf("settings lost the file's comments")
	}
	onMain(func() {
		if w.cfg.ThemeName != "dracula" {
			t.Errorf("window theme %q after changing it in Preferences", w.cfg.ThemeName)
		}
	})
}

// bunk's multiplexer: split, focus, zoom, pane exit, last pane closes the tab.
func TestGUI_SplitsZoomAndPaneExit(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", nil)
	w.key(gdk.KEY_F1, 0)
	w.waitPanes(2)
	w.key(gdk.KEY_F1, 0)
	w.waitPanes(3)
	onMain(func() {
		var borders []guiBorder
		app := w.activeApp()
		app.mu.Lock()
		collectBorders(app.root, app.active, &borders)
		app.mu.Unlock()
		if len(borders) < 2 {
			t.Errorf("3 panes should have 2 separators, got %+v", borders)
		}
	})
	panes := w.panes()
	w.key(gdk.KEY_Left, alt)
	onMain(func() {
		if a := w.activeApp().active; a == panes[2] {
			t.Errorf("Alt+Left did not move focus")
		}
	})
	w.key(gdk.KEY_F12, 0)
	waitMain(t, 3*time.Second, "zoomed frame", func() bool {
		return w.activeApp().zoomedPane != nil && len(w.active.view.frames) == 1
	})
	w.key(gdk.KEY_F12, 0)
	waitMain(t, 3*time.Second, "unzoomed frames", func() bool {
		return w.activeApp().zoomedPane == nil && len(w.active.view.frames) == 3
	})

	for _, p := range w.panes()[1:] {
		p.writeInput([]byte("exit\n"))
	}
	w.waitPanes(1)
	w.panes()[0].close() // last shell ends: the tab closes, and with it the window
	waitMain(t, 5*time.Second, "tab closed", func() bool { return len(w.tabs) == 0 })
}

// Search: Ctrl+F, typed query, highlights in the frame, Esc exits.
func TestGUI_Search(t *testing.T) {
	w := newTestWin(t, "printf 'a needle in the haystack\\nneedle again\\n'; exec sleep 30", nil)
	p := w.panes()[0]
	w.waitDrawn(p, "needle again")
	w.key(gdk.KEY_f, ctrl)
	w.typeText("needle")
	waitMain(t, 3*time.Second, "matches highlighted", func() bool {
		app := w.activeApp()
		f := w.active.view.frames[p]
		if !app.searchMode || len(app.searchMatches) != 2 || f == nil {
			return false
		}
		for _, row := range f.searchReg {
			if len(row) > 0 {
				return true
			}
		}
		for _, row := range f.searchCur {
			if len(row) > 0 {
				return true
			}
		}
		return false
	})
	w.key(gdk.KEY_Escape, 0)
	onMain(func() {
		if w.activeApp().searchMode {
			t.Errorf("Escape should leave search")
		}
	})
}

// Info at the bunker level: header subtitle and tab tooltip carry the cwd;
// tab info shows the pane count once split.
func TestGUI_HeaderAndTabInfo(t *testing.T) {
	dir := t.TempDir()
	w := newTestWin(t, "cd '"+dir+"' && exec sleep 30", nil)
	waitMain(t, 5*time.Second, "cwd in the subtitle", func() bool {
		w.active.refreshTitle()
		return strings.Contains(w.headSub.Text(), tildePath(dir))
	})
	w.key(gdk.KEY_F1, 0)
	w.waitPanes(2)
	waitMain(t, 3*time.Second, "pane count chip", func() bool {
		w.active.refreshTitle()
		return strings.Contains(w.active.info.Text(), "⊞2")
	})
}

// Keyboard passthrough (Ctrl+F12) also hands bunker's window shortcuts to
// the program.
func TestGUI_PassthroughBypassesWindowShortcuts(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", nil)
	w.key(gdk.KEY_F12, ctrl)
	w.key(gdk.KEY_T, ctrl|shift)
	onMain(func() {
		if len(w.tabs) != 1 {
			t.Errorf("Ctrl+Shift+T opened a tab while passthrough was on")
		}
	})
	w.key(gdk.KEY_F12, ctrl)
	w.key(gdk.KEY_T, ctrl|shift)
	onMain(func() {
		if len(w.tabs) != 2 {
			t.Errorf("Ctrl+Shift+T should open a tab once passthrough is off")
		}
	})
}

// The tab menu builds, and Close Other Tabs works from it.
func TestGUI_TabMenu(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", nil)
	w.key(gdk.KEY_T, ctrl|shift)
	onMain(func() {
		tab := w.active
		tab.showMenu(10, 10)
		if tab.menu == nil || !tab.menu.IsVisible() {
			t.Fatalf("tab menu did not open")
		}
		tab.menu.Popdown()
		tab.row.ActivateAction("tab.close-others", nil)
		if len(w.tabs) != 1 || w.tabs[0] != tab {
			t.Errorf("close others left %d tabs", len(w.tabs))
		}
	})
}

func writeKey(t *testing.T, path, section, key, literal string) {
	t.Helper()
	if _, err := writeConfigKey(path, section, key, literal); err != nil {
		t.Fatal(err)
	}
}

func indexOf(tabs []*guiTab, t *guiTab) int {
	for i, x := range tabs {
		if x == t {
			return i
		}
	}
	return -1
}

// Input methods: committed text (a dead key's é, CJK) reaches the program
// through bunk's key handling, feeds the search bar in search mode, and the
// composition is drawn at the cursor while in progress.
func TestGUI_IME(t *testing.T) {
	w := newTestWin(t, "exec cat", nil)
	p := w.panes()[0]
	onMain(func() { w.active.view.imeCommit("é日本 ok\r") })
	w.waitDrawn(p, "é日本 ok")

	onMain(func() {
		v := w.active.view
		v.setPreedit("nihao", 3)
		if v.ime.preedit != "nihao" {
			t.Fatalf("preedit not stored")
		}
	})
	settle(100 * time.Millisecond)
	img := w.screenshot() // draws the preedit path; must not disturb the frame
	if img.Bounds().Dx() == 0 {
		t.Fatal("empty screenshot")
	}
	onMain(func() {
		v := w.active.view
		if v.ime.area == [4]int{} {
			t.Errorf("cursor location never reported to the input method")
		}
		v.imeCommit("你好")
		if v.ime.preedit != "" {
			t.Errorf("commit should end the composition")
		}
	})
	w.waitDrawn(p, "你好")

	w.key(gdk.KEY_f, ctrl)
	onMain(func() { w.active.view.imeCommit("ok") })
	onMain(func() {
		if q := w.activeApp().searchQuery; q != "ok" {
			t.Errorf("committed text in search mode went elsewhere: query %q", q)
		}
	})
}

// TestGUI_WaylandIMStress opens, focuses rename entries in, and closes
// windows and tabs quickly, in a child process so a crash is reported
// rather than taking down the suite. Before the terminal view had its own
// input method this crashed GTK's Wayland IM code about one run in three
// (gtk_im_context_wayland_global_get).
func TestGUI_WaylandIMStress(t *testing.T) {
	needGUI(t)
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("Wayland only")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestIMStressChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "BUNKER_IM_STRESS_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child crashed or failed: %v\n%s", err, tail(out, 40))
	}
}

func TestIMStressChild(t *testing.T) {
	if os.Getenv("BUNKER_IM_STRESS_CHILD") == "" {
		t.Skip("run by TestGUI_WaylandIMStress")
	}
	for i := range 25 {
		w := newTestWin(t, "exec sleep 30", nil)
		w.key(gdk.KEY_T, ctrl|shift)
		onMain(func() {
			w.active.startRename() // focuses an entry, which uses the IM
			w.active.view.imeCommit("x")
		})
		settle(20 * time.Millisecond)
		onMain(func() {
			w.closeTab(w.active) // destroys the focused entry
			w.gw().win.Destroy()
		})
		settle(time.Duration(i%3) * 10 * time.Millisecond)
	}
}

func (w *testWin) gw() *guiWin { return w.guiWin }

func tail(b []byte, lines int) string {
	parts := strings.Split(string(b), "\n")
	return strings.Join(parts[max(0, len(parts)-lines):], "\n")
}
