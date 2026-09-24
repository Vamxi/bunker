package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bunker/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

type countingScreen struct {
	tcell.Screen
	shows atomic.Int32
}

func (s *countingScreen) Show() {
	s.shows.Add(1)
	s.Screen.Show()
}

func (s *countingScreen) Sync() {
	s.shows.Add(1)
	s.Screen.Sync()
}

// testTheme returns a resolvedTheme with distinct, recognisable colours for
// fg, bg, and a partial ANSI palette.  Slots not explicitly set are left as
// tcell.ColorDefault so the "no palette override" path is exercised.
func testTheme() resolvedTheme {
	rt := resolvedTheme{
		fg:             tcell.NewRGBColor(0xd0, 0xd0, 0xd0), // light grey
		bg:             tcell.NewRGBColor(0x1a, 0x1a, 0x2e), // dark blue
		activeBorder:   tcell.ColorGreen,
		inactiveBorder: tcell.ColorGray,
		scrollThumb:    tcell.ColorWhite,
		scrollTrack:    tcell.ColorDarkGray,
	}
	// Override only a few palette slots so we can test both paths.
	rt.palette[0] = tcell.NewRGBColor(0x00, 0x00, 0x00) // black override
	rt.palette[1] = tcell.NewRGBColor(0xcc, 0x00, 0x00) // red override
	rt.palette[7] = tcell.NewRGBColor(0xaa, 0xaa, 0xaa) // light grey override
	// Slots 2-6, 8-15 remain tcell.ColorDefault (no override).
	return rt
}

// ---------------------------------------------------------------------------
// 1. DefaultFG returns rt.fg regardless of the def parameter
//
// Bug: btop background leak after resize+exit
//
// When btop exits, cells are cleared with the active cursor attributes.
// vtColor must map DefaultFG → rt.fg (not the `def` fallback) so that
// cleared cells pick up the theme foreground, not the app's colours.
// ---------------------------------------------------------------------------

func TestVtColor_DefaultFG_BtopBackgroundLeak(t *testing.T) {
	rt := testTheme()

	// Pass a totally different colour as `def` to prove it is ignored.
	def := tcell.ColorRed
	got := vtColor(vt10x.DefaultFG, def, rt)
	if got != rt.fg {
		t.Errorf("vtColor(DefaultFG, def, rt) = %v; want rt.fg = %v", got, rt.fg)
	}
}

func TestVtColor_DefaultFG_BtopBackgroundLeak_IgnoresDef(t *testing.T) {
	rt := testTheme()

	// Even when def == rt.bg, the result must be rt.fg.
	got := vtColor(vt10x.DefaultFG, rt.bg, rt)
	if got != rt.fg {
		t.Errorf("vtColor(DefaultFG, rt.bg, rt) = %v; want rt.fg = %v", got, rt.fg)
	}
}

// ---------------------------------------------------------------------------
// 2. DefaultBG returns rt.bg regardless of the def parameter
//
// Bug: btop background leak after resize+exit
//
// Same as DefaultFG above — DefaultBG must always → rt.bg so that
// background colours in cleared cells use the theme, not the app's palette.
// ---------------------------------------------------------------------------

func TestVtColor_DefaultBG_BtopBackgroundLeak(t *testing.T) {
	rt := testTheme()

	def := tcell.ColorBlue
	got := vtColor(vt10x.DefaultBG, def, rt)
	if got != rt.bg {
		t.Errorf("vtColor(DefaultBG, def, rt) = %v; want rt.bg = %v", got, rt.bg)
	}
}

func TestVtColor_DefaultBG_BtopBackgroundLeak_IgnoresDef(t *testing.T) {
	rt := testTheme()

	got := vtColor(vt10x.DefaultBG, rt.fg, rt)
	if got != rt.bg {
		t.Errorf("vtColor(DefaultBG, rt.fg, rt) = %v; want rt.bg = %v", got, rt.bg)
	}
}

// ---------------------------------------------------------------------------
// 3. DefaultCursor returns def
// ---------------------------------------------------------------------------

func TestVtColor_DefaultCursor_ReturnsDef(t *testing.T) {
	rt := testTheme()

	defs := []tcell.Color{
		tcell.ColorRed,
		tcell.ColorWhite,
		rt.fg,
		rt.bg,
		tcell.NewRGBColor(0x12, 0x34, 0x56),
	}
	for _, def := range defs {
		got := vtColor(vt10x.DefaultCursor, def, rt)
		if got != def {
			t.Errorf("vtColor(DefaultCursor, %v, rt) = %v; want %v", def, got, def)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. ANSI colors 0-15 WITH theme palette override
// ---------------------------------------------------------------------------

func TestVtColor_ANSIWithPaletteOverride(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	// Slot 0 is overridden in testTheme.
	got := vtColor(0, def, rt)
	if got != rt.palette[0] {
		t.Errorf("vtColor(0, def, rt) = %v; want palette[0] = %v", got, rt.palette[0])
	}

	// Slot 1 is overridden.
	got = vtColor(1, def, rt)
	if got != rt.palette[1] {
		t.Errorf("vtColor(1, def, rt) = %v; want palette[1] = %v", got, rt.palette[1])
	}

	// Slot 7 is overridden.
	got = vtColor(7, def, rt)
	if got != rt.palette[7] {
		t.Errorf("vtColor(7, def, rt) = %v; want palette[7] = %v", got, rt.palette[7])
	}
}

// ---------------------------------------------------------------------------
// 5. ANSI colors 0-15 WITHOUT palette override (tcell.ColorDefault in palette)
// ---------------------------------------------------------------------------

func TestVtColor_ANSIWithoutPaletteOverride(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	// Slots 2-6 and 8-15 are NOT overridden in testTheme.
	unoverridden := []vt10x.Color{2, 3, 4, 5, 6, 8, 9, 10, 11, 12, 13, 14, 15}
	for _, c := range unoverridden {
		want := tcell.PaletteColor(int(c))
		got := vtColor(c, def, rt)
		if got != want {
			t.Errorf("vtColor(%d, def, rt) = %v; want PaletteColor(%d) = %v", c, got, c, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 6. xterm-256 colors (16-255)
// ---------------------------------------------------------------------------

func TestVtColor_Xterm256(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	// Test boundary and a sample of values.
	cases := []vt10x.Color{16, 17, 100, 200, 231, 255}
	for _, c := range cases {
		want := tcell.PaletteColor(int(c))
		got := vtColor(c, def, rt)
		if got != want {
			t.Errorf("vtColor(%d, def, rt) = %v; want PaletteColor(%d) = %v", c, got, c, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 7. Truecolor RGB
// ---------------------------------------------------------------------------

func TestVtColor_Truecolor(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	cases := []struct {
		r, g, b byte
	}{
		{0x00, 0x00, 0x00}, // black
		{0xff, 0xff, 0xff}, // white
		{0xca, 0xfe, 0x42}, // arbitrary
		{0x01, 0x00, 0x00}, // near-zero
		{0x00, 0x01, 0x00},
		{0x00, 0x00, 0x01},
	}
	for _, tc := range cases {
		c := vt10x.Color(uint32(tc.r)<<16 | uint32(tc.g)<<8 | uint32(tc.b))
		// Skip if the packed value happens to collide with a low ANSI or
		// xterm-256 index (values 0-255) since those take a different path.
		if c < 256 {
			continue
		}
		want := tcell.NewRGBColor(int32(tc.r), int32(tc.g), int32(tc.b))
		got := vtColor(c, def, rt)
		if got != want {
			t.Errorf("vtColor(rgb(%02x,%02x,%02x)=%d, def, rt) = %v; want %v",
				tc.r, tc.g, tc.b, c, got, want)
		}
	}
}

func TestVtColor_TruecolorBoundary(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	// The highest valid truecolor is 0xffffff = 16777215, which must be
	// less than vt10x.DefaultFG (1<<24 = 16777216).
	c := vt10x.Color(0xff_ff_ff)
	if c >= vt10x.DefaultFG {
		t.Fatalf("0xffffff (%d) >= DefaultFG (%d); test assumption broken", c, vt10x.DefaultFG)
	}
	want := tcell.NewRGBColor(0xff, 0xff, 0xff)
	got := vtColor(c, def, rt)
	if got != want {
		t.Errorf("vtColor(0xffffff, def, rt) = %v; want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// 8. Fallback: unknown sentinel values beyond DefaultCursor return def
// ---------------------------------------------------------------------------

func TestVtColor_UnknownSentinel_ReturnsDef(t *testing.T) {
	rt := testTheme()
	def := tcell.NewRGBColor(0x42, 0x42, 0x42)

	// Values above DefaultCursor are unknown; vtColor should return def.
	unknown := vt10x.DefaultCursor + 1
	got := vtColor(unknown, def, rt)
	if got != def {
		t.Errorf("vtColor(%d, def, rt) = %v; want def = %v", unknown, got, def)
	}
}

// ---------------------------------------------------------------------------
// 9. REVERSE VIDEO SCENARIO
//
// Bug: Claude/Copilot cursor invisible in bunk
//
// Claude Code draws its cursor as a reverse-video space (\x1b[7m \x1b[27m).
// vt10x pre-swaps FG/BG for reverse cells, setting cell.FG=DefaultBG and
// cell.BG=DefaultFG.  The old vtColor treated both DefaultFG and DefaultBG
// as "return def", which undid the swap — making the cursor invisible.
//
// The fix: DefaultFG always → rt.fg, DefaultBG always → rt.bg.
// ---------------------------------------------------------------------------

func TestVtColor_ReverseVideo_ClaudeCopilotCursor(t *testing.T) {
	rt := testTheme()

	// Simulate the render call site for a normal (non-reversed) cell:
	//   fg = vtColor(cell.FG=DefaultFG, def=rt.fg, rt)  → rt.fg
	//   bg = vtColor(cell.BG=DefaultBG, def=rt.bg, rt)  → rt.bg
	normalFG := vtColor(vt10x.DefaultFG, rt.fg, rt)
	normalBG := vtColor(vt10x.DefaultBG, rt.bg, rt)
	if normalFG != rt.fg {
		t.Fatalf("normal cell FG: got %v, want rt.fg=%v", normalFG, rt.fg)
	}
	if normalBG != rt.bg {
		t.Fatalf("normal cell BG: got %v, want rt.bg=%v", normalBG, rt.bg)
	}

	// Simulate the render call site for a REVERSED cell.
	// vt10x has already swapped FG↔BG in the Glyph:
	//   cell.FG = DefaultBG   (was background, now foreground)
	//   cell.BG = DefaultFG   (was foreground, now background)
	//
	// The caller still passes the same `def` values as for a normal cell:
	//   fg = vtColor(cell.FG=DefaultBG, def=rt.fg, rt)
	//   bg = vtColor(cell.BG=DefaultFG, def=rt.bg, rt)
	reversedFG := vtColor(vt10x.DefaultBG, rt.fg, rt)
	reversedBG := vtColor(vt10x.DefaultFG, rt.bg, rt)

	// After reverse-video swap, FG should be the theme background colour
	// and BG should be the theme foreground colour.
	if reversedFG != rt.bg {
		t.Errorf("reversed cell FG: vtColor(DefaultBG, rt.fg, rt) = %v; want rt.bg = %v",
			reversedFG, rt.bg)
	}
	if reversedBG != rt.fg {
		t.Errorf("reversed cell BG: vtColor(DefaultFG, rt.bg, rt) = %v; want rt.fg = %v",
			reversedBG, rt.fg)
	}

	// The critical invariant: reversed colours must be the opposite of normal.
	if normalFG == reversedFG {
		t.Error("reversed cell FG must differ from normal cell FG (swap broken)")
	}
	if normalBG == reversedBG {
		t.Error("reversed cell BG must differ from normal cell BG (swap broken)")
	}
}

// TestVtColor_ReverseVideoWithANSIColors verifies that reverse video also
// works correctly when one side is an ANSI colour and the other is a default.
func TestVtColor_ReverseVideoWithANSIColors(t *testing.T) {
	rt := testTheme()

	// Scenario: cell has ANSI red foreground on default background, then reversed.
	// Before reverse: FG=Red(1), BG=DefaultBG
	// After vt10x reverse swap: FG=DefaultBG, BG=Red(1)
	//
	// Render call: fg = vtColor(DefaultBG, rt.fg, rt), bg = vtColor(1, rt.bg, rt)
	fg := vtColor(vt10x.DefaultBG, rt.fg, rt)
	bg := vtColor(1, rt.bg, rt)

	if fg != rt.bg {
		t.Errorf("reversed ANSI cell FG: vtColor(DefaultBG, rt.fg, rt) = %v; want rt.bg = %v",
			fg, rt.bg)
	}
	// Color 1 is overridden in our test palette.
	if bg != rt.palette[1] {
		t.Errorf("reversed ANSI cell BG: vtColor(1, rt.bg, rt) = %v; want palette[1] = %v",
			bg, rt.palette[1])
	}
}

// ---------------------------------------------------------------------------
// Table-driven comprehensive test
// ---------------------------------------------------------------------------

func TestVtColor_TableDriven(t *testing.T) {
	rt := testTheme()

	tests := []struct {
		name string
		c    vt10x.Color
		def  tcell.Color
		want tcell.Color
	}{
		// Defaults
		{"DefaultFG with fg def", vt10x.DefaultFG, rt.fg, rt.fg},
		{"DefaultFG with bg def", vt10x.DefaultFG, rt.bg, rt.fg},
		{"DefaultFG with red def", vt10x.DefaultFG, tcell.ColorRed, rt.fg},
		{"DefaultBG with bg def", vt10x.DefaultBG, rt.bg, rt.bg},
		{"DefaultBG with fg def", vt10x.DefaultBG, rt.fg, rt.bg},
		{"DefaultBG with red def", vt10x.DefaultBG, tcell.ColorRed, rt.bg},
		{"DefaultCursor with fg", vt10x.DefaultCursor, rt.fg, rt.fg},
		{"DefaultCursor with bg", vt10x.DefaultCursor, rt.bg, rt.bg},
		{"DefaultCursor with red", vt10x.DefaultCursor, tcell.ColorRed, tcell.ColorRed},

		// ANSI palette overrides
		{"ANSI 0 (black, overridden)", 0, tcell.ColorDefault, rt.palette[0]},
		{"ANSI 1 (red, overridden)", 1, tcell.ColorDefault, rt.palette[1]},
		{"ANSI 7 (light grey, overridden)", 7, tcell.ColorDefault, rt.palette[7]},

		// ANSI palette no override
		{"ANSI 2 (green, no override)", 2, tcell.ColorDefault, tcell.PaletteColor(2)},
		{"ANSI 15 (white, no override)", 15, tcell.ColorDefault, tcell.PaletteColor(15)},

		// xterm-256
		{"xterm 16", 16, tcell.ColorDefault, tcell.PaletteColor(16)},
		{"xterm 128", 128, tcell.ColorDefault, tcell.PaletteColor(128)},
		{"xterm 255", 255, tcell.ColorDefault, tcell.PaletteColor(255)},

		// Truecolor
		{"truecolor #010101", vt10x.Color(0x010101), tcell.ColorDefault,
			tcell.NewRGBColor(0x01, 0x01, 0x01)},
		{"truecolor #cafe42", vt10x.Color(0xcafe42), tcell.ColorDefault,
			tcell.NewRGBColor(0xca, 0xfe, 0x42)},
		{"truecolor #ffffff", vt10x.Color(0xffffff), tcell.ColorDefault,
			tcell.NewRGBColor(0xff, 0xff, 0xff)},

		// Unknown sentinel
		{"unknown past DefaultCursor", vt10x.DefaultCursor + 1, tcell.ColorRed, tcell.ColorRed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vtColor(tt.c, tt.def, rt)
			if got != tt.want {
				t.Errorf("vtColor(%d, %v, rt) = %v; want %v", tt.c, tt.def, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

// TestVtColor_Color256Boundary checks the boundary between ANSI (0-15) and
// xterm-256 (16-255).
func TestVtColor_Color256Boundary(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	// Color 15 is the last ANSI slot; not overridden in testTheme → PaletteColor.
	got15 := vtColor(15, def, rt)
	want15 := tcell.PaletteColor(15)
	if got15 != want15 {
		t.Errorf("vtColor(15) = %v; want PaletteColor(15) = %v", got15, want15)
	}

	// Color 16 is the first xterm-256 slot.
	got16 := vtColor(16, def, rt)
	want16 := tcell.PaletteColor(16)
	if got16 != want16 {
		t.Errorf("vtColor(16) = %v; want PaletteColor(16) = %v", got16, want16)
	}
}

// TestVtColor_AllANSISlots verifies that every ANSI slot (0-15) returns either
// the palette override or PaletteColor, never anything else.
func TestVtColor_AllANSISlots(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	for i := vt10x.Color(0); i < 16; i++ {
		got := vtColor(i, def, rt)
		if rt.palette[i] != tcell.ColorDefault {
			if got != rt.palette[i] {
				t.Errorf("ANSI %d: got %v; want palette override %v", i, got, rt.palette[i])
			}
		} else {
			want := tcell.PaletteColor(int(i))
			if got != want {
				t.Errorf("ANSI %d: got %v; want PaletteColor(%d) = %v", i, got, i, want)
			}
		}
	}
}

// TestVtColor_TruecolorComponents tests that RGB extraction from the packed
// vt10x format is correct for a range of values.
func TestVtColor_TruecolorComponents(t *testing.T) {
	rt := testTheme()
	def := tcell.ColorDefault

	// Generate a spread of RGB values to test component extraction.
	components := []struct{ r, g, b int32 }{
		{0x10, 0x20, 0x30},
		{0xff, 0x00, 0x00},
		{0x00, 0xff, 0x00},
		{0x00, 0x00, 0xff},
		{0x80, 0x80, 0x80},
		{0xde, 0xad, 0xbe},
	}

	for _, comp := range components {
		packed := vt10x.Color(uint32(comp.r)<<16 | uint32(comp.g)<<8 | uint32(comp.b))
		if packed < 256 {
			continue // skip values that fall in the palette range
		}
		want := tcell.NewRGBColor(comp.r, comp.g, comp.b)
		got := vtColor(packed, def, rt)
		if got != want {
			t.Errorf("truecolor #%02x%02x%02x: got %v; want %v",
				comp.r, comp.g, comp.b, got, want)
		}
	}
}

// TestVtColor_EmptyPalette verifies behaviour when the theme has NO palette
// overrides at all (all slots are tcell.ColorDefault).
func TestVtColor_EmptyPalette(t *testing.T) {
	rt := resolvedTheme{
		fg: tcell.NewRGBColor(0xff, 0xff, 0xff),
		bg: tcell.NewRGBColor(0x00, 0x00, 0x00),
	}
	// palette is zero-value: all tcell.ColorDefault (which is 0).
	def := tcell.ColorDefault

	for i := vt10x.Color(0); i < 16; i++ {
		want := tcell.PaletteColor(int(i))
		got := vtColor(i, def, rt)
		if got != want {
			t.Errorf("empty palette ANSI %d: got %v; want PaletteColor(%d) = %v",
				i, got, i, want)
		}
	}
}

// TestVtColor_FullPalette verifies behaviour when every palette slot is overridden.
func TestVtColor_FullPalette(t *testing.T) {
	rt := resolvedTheme{
		fg: tcell.NewRGBColor(0xff, 0xff, 0xff),
		bg: tcell.NewRGBColor(0x00, 0x00, 0x00),
	}
	for i := 0; i < 16; i++ {
		// Each slot gets a unique colour.
		rt.palette[i] = tcell.NewRGBColor(int32(i*16), int32(i*16+1), int32(i*16+2))
	}
	def := tcell.ColorDefault

	for i := vt10x.Color(0); i < 16; i++ {
		got := vtColor(i, def, rt)
		if got != rt.palette[i] {
			t.Errorf("full palette ANSI %d: got %v; want %v", i, got, rt.palette[i])
		}
	}
}

// TestVtColor_DefaultFGBGAreDistinct_ClaudeCopilotCursor confirms that
// DefaultFG and DefaultBG produce different results — the core invariant for
// reverse video.  Without this, the cursor drawn by Claude/Copilot
// (\x1b[7m space) is invisible because the FG/BG swap is undone.
func TestVtColor_DefaultFGBGAreDistinct_ClaudeCopilotCursor(t *testing.T) {
	rt := testTheme()
	if rt.fg == rt.bg {
		t.Skip("test theme fg == bg; pick different colours")
	}

	fgResult := vtColor(vt10x.DefaultFG, tcell.ColorDefault, rt)
	bgResult := vtColor(vt10x.DefaultBG, tcell.ColorDefault, rt)
	if fgResult == bgResult {
		t.Errorf("vtColor(DefaultFG) == vtColor(DefaultBG) == %v; they must differ for reverse video", fgResult)
	}
}

// TestVtColor_DefParameterIrrelevant_BtopBackgroundLeak exhaustively verifies
// that the `def` parameter has zero effect on DefaultFG and DefaultBG results.
// This prevents background colours leaking from alt-screen apps like btop.
func TestVtColor_DefParameterIrrelevant_BtopBackgroundLeak(t *testing.T) {
	rt := testTheme()

	defs := []tcell.Color{
		tcell.ColorDefault,
		tcell.ColorRed,
		tcell.ColorBlue,
		rt.fg,
		rt.bg,
		tcell.NewRGBColor(0x12, 0x34, 0x56),
		tcell.NewRGBColor(0xff, 0x00, 0xff),
	}

	for _, def := range defs {
		t.Run(fmt.Sprintf("DefaultFG_def_%v", def), func(t *testing.T) {
			got := vtColor(vt10x.DefaultFG, def, rt)
			if got != rt.fg {
				t.Errorf("vtColor(DefaultFG, %v, rt) = %v; want rt.fg = %v", def, got, rt.fg)
			}
		})
		t.Run(fmt.Sprintf("DefaultBG_def_%v", def), func(t *testing.T) {
			got := vtColor(vt10x.DefaultBG, def, rt)
			if got != rt.bg {
				t.Errorf("vtColor(DefaultBG, %v, rt) = %v; want rt.bg = %v", def, got, rt.bg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkAndClearNeedsSync
//
// Bug: btop background artifact persists after exit
//
// After alt-screen exit, needsSync is set on the pane so that the render
// loop uses Sync() (full repaint).  checkAndClearNeedsSync walks the BSP
// tree, returns true if any pane had it set, and clears them all.
// ---------------------------------------------------------------------------

func TestCheckAndClearNeedsSync_NoPanes(t *testing.T) {
	if checkAndClearNeedsSync(nil) {
		t.Error("checkAndClearNeedsSync(nil) = true, want false")
	}
}

func TestCheckAndClearNeedsSync_SingleLeaf_Clean(t *testing.T) {
	p := &Pane{needsSync: false}
	n := &Node{pane: p}
	if checkAndClearNeedsSync(n) {
		t.Error("checkAndClearNeedsSync(clean pane) = true, want false")
	}
}

func TestCheckAndClearNeedsSync_SingleLeaf_Dirty(t *testing.T) {
	p := &Pane{needsSync: true}
	n := &Node{pane: p}
	if !checkAndClearNeedsSync(n) {
		t.Error("checkAndClearNeedsSync(dirty pane) = false, want true")
	}
	// Should have cleared the flag.
	if p.needsSync {
		t.Error("needsSync not cleared after checkAndClearNeedsSync")
	}
}

func TestCheckAndClearNeedsSync_Tree_OneDirty(t *testing.T) {
	p1 := &Pane{needsSync: false}
	p2 := &Pane{needsSync: true}
	root := &Node{
		left:  &Node{pane: p1},
		right: &Node{pane: p2},
	}
	if !checkAndClearNeedsSync(root) {
		t.Error("checkAndClearNeedsSync with one dirty = false, want true")
	}
	if p2.needsSync {
		t.Error("p2.needsSync not cleared")
	}
}

func TestCheckAndClearNeedsSync_Tree_AllDirty(t *testing.T) {
	p1 := &Pane{needsSync: true}
	p2 := &Pane{needsSync: true}
	root := &Node{
		left:  &Node{pane: p1},
		right: &Node{pane: p2},
	}
	if !checkAndClearNeedsSync(root) {
		t.Error("checkAndClearNeedsSync with both dirty = false, want true")
	}
	if p1.needsSync || p2.needsSync {
		t.Error("not all needsSync flags cleared")
	}
}

func TestNextRenderDelay(t *testing.T) {
	now := time.Unix(100, 0)
	cases := []struct {
		name       string
		lastRender time.Time
		want       time.Duration
	}{
		{
			name: "first frame waits for burst settle",
			want: renderSettleInterval,
		},
		{
			name:       "recent frame honours minimum interval",
			lastRender: now.Add(-time.Millisecond),
			want:       renderMinInterval - time.Millisecond,
		},
		{
			name:       "stale frame still waits for burst settle",
			lastRender: now.Add(-time.Second),
			want:       renderSettleInterval,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextRenderDelay(tc.lastRender, now)
			if got != tc.want {
				t.Errorf("nextRenderDelay() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRenderLoop_CoalescesEraseAndRedrawBurst(t *testing.T) {
	const (
		w = 24
		h = 1
	)
	term := vt10x.New(vt10x.WithSize(w-1, h))
	p := &Pane{
		term:            term,
		cmd:             &exec.Cmd{},
		x:               0,
		y:               0,
		w:               w,
		h:               h,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	base := tcell.NewSimulationScreen("UTF-8")
	base.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer base.Fini()
	base.SetSize(w, h)
	screen := &countingScreen{Screen: base}

	app := &App{
		screen: screen,
		root:   &Node{pane: p},
		active: p,
		redraw: make(chan struct{}, 2),
		done:   make(chan struct{}),
		oscBuf: newOSCBuffer(),
		theme:  testTheme(),
	}

	p.mu.Lock()
	p.captureAndWrite([]byte("\r                       \r"))
	p.mu.Unlock()
	app.triggerRedraw()

	p.mu.Lock()
	p.captureAndWrite([]byte("\r  99% |████████████|"))
	p.mu.Unlock()
	app.triggerRedraw()

	app.renderWg.Add(1)
	go app.renderLoop()

	deadline := time.Now().Add(100 * time.Millisecond)
	for screen.shows.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(app.done)
	app.renderWg.Wait()

	if got := screen.shows.Load(); got != 1 {
		t.Fatalf("renderLoop painted %d frames, want 1 coalesced frame", got)
	}
	mainc, _, _, _ := base.GetContent(2, 0) //nolint:staticcheck // rune-level access
	if mainc != '9' {
		t.Fatalf("rendered content at progress column = %q, want '9'", mainc)
	}
}

func TestRender_SkipsTransientLineClear(t *testing.T) {
	const (
		w = 24
		h = 1
	)
	term := vt10x.New(vt10x.WithSize(w-1, h))
	term.Write([]byte("  99% |████████████|")) //nolint:errcheck
	p := &Pane{
		term:               term,
		cmd:                &exec.Cmd{},
		x:                  0,
		y:                  0,
		w:                  w,
		h:                  h,
		scrollbackLines:    100,
		sb:                 sbRing{maxLines: 100},
		transientLineClear: true,
	}

	base := tcell.NewSimulationScreen("UTF-8")
	base.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer base.Fini()
	base.SetSize(w, h)
	screen := &countingScreen{Screen: base}

	app := &App{
		screen: screen,
		root:   &Node{pane: p},
		active: p,
		oscBuf: newOSCBuffer(),
		theme:  testTheme(),
	}

	base.SetContent(2, 0, 'Z', nil, tcell.StyleDefault)
	app.render()
	if got := screen.shows.Load(); got != 1 {
		t.Fatalf("render painted %d frames while transient clear was pending, want 1 unchanged frame", got)
	}
	mainc, _, _, _ := base.GetContent(2, 0) //nolint:staticcheck // rune-level access
	if mainc != 'Z' {
		t.Fatalf("transient clear changed screen content to %q, want previous 'Z'", mainc)
	}
	_, _, visible := base.GetCursor()
	if visible {
		t.Fatal("cursor visible during transient clear, want hidden")
	}

	p.mu.Lock()
	p.transientLineClear = false
	p.mu.Unlock()
	app.render()
	if got := screen.shows.Load(); got != 2 {
		t.Fatalf("render painted %d frames after transient clear resolved, want 2", got)
	}
	mainc, _, _, _ = base.GetContent(2, 0) //nolint:staticcheck // rune-level access
	if mainc != '9' {
		t.Fatalf("rendered content after transient clear resolved = %q, want '9'", mainc)
	}
}

func TestRender_InactiveTransientLineClearDoesNotBlockActivePane(t *testing.T) {
	const h = 1
	activeTerm := vt10x.New(vt10x.WithSize(4, h))
	activeTerm.Write([]byte("OK")) //nolint:errcheck
	active := &Pane{
		term:            activeTerm,
		cmd:             &exec.Cmd{},
		x:               0,
		y:               0,
		w:               5,
		h:               h,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}
	inactiveTerm := vt10x.New(vt10x.WithSize(4, h))
	inactiveTerm.Write([]byte("OLD")) //nolint:errcheck
	inactive := &Pane{
		term:               inactiveTerm,
		cmd:                &exec.Cmd{},
		x:                  6,
		y:                  0,
		w:                  5,
		h:                  h,
		scrollbackLines:    100,
		sb:                 sbRing{maxLines: 100},
		transientLineClear: true,
	}

	base := tcell.NewSimulationScreen("UTF-8")
	base.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer base.Fini()
	base.SetSize(11, h)
	screen := &countingScreen{Screen: base}

	root := &Node{
		x: 0, y: 0, w: 11, h: h, dir: splitVertical,
		left:  &Node{x: 0, y: 0, w: 5, h: h, pane: active},
		right: &Node{x: 6, y: 0, w: 5, h: h, pane: inactive},
	}
	app := &App{
		screen: screen,
		root:   root,
		active: active,
		oscBuf: newOSCBuffer(),
		theme:  testTheme(),
	}

	app.render()
	if got := screen.shows.Load(); got != 1 {
		t.Fatalf("render painted %d frames, want 1", got)
	}
	mainc, _, _, _ := base.GetContent(0, 0) //nolint:staticcheck // rune-level access
	if mainc != 'O' {
		t.Fatalf("active pane was not rendered while inactive pane was transient: got %q", mainc)
	}
}

// TestRender_ZoomOutRepaintsStalePanes_BtopOverflow reproduces the zoom-exit
// bug: with two vertical panes, zooming the right pane (btop) fullscreen
// paints its frame over the left pane's region of the tcell cell buffer.  On
// zoom-out the left pane has no vt10x dirty rows and no overlay change, so
// without forceFullRepaint renderPane skips it and the zoomed pane's overflow
// stays on screen until some overlay change (e.g. a mouse click starting a
// selection) forces a repaint.
func TestRender_ZoomOutRepaintsStalePanes_BtopOverflow(t *testing.T) {
	const (
		screenW = 11
		h       = 1
	)
	leftTerm := vt10x.New(vt10x.WithSize(4, h))
	left := &Pane{
		term: leftTerm, cmd: &exec.Cmd{},
		x: 0, y: 0, w: 5, h: h,
		scrollbackLines: 100, sb: sbRing{maxLines: 100},
	}
	rightTerm := vt10x.New(vt10x.WithSize(4, h))
	right := &Pane{
		id:   1,
		term: rightTerm, cmd: &exec.Cmd{},
		x: 6, y: 0, w: 5, h: h,
		scrollbackLines: 100, sb: sbRing{maxLines: 100},
	}
	left.mu.Lock()
	left.captureAndWrite([]byte("LLLL"))
	left.mu.Unlock()
	right.mu.Lock()
	right.captureAndWrite([]byte("RRRR"))
	right.mu.Unlock()

	base := tcell.NewSimulationScreen("UTF-8")
	base.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer base.Fini()
	base.SetSize(screenW, h)

	root := &Node{
		x: 0, y: 0, w: screenW, h: h, dir: splitVertical,
		left:  &Node{x: 0, y: 0, w: 5, h: h, pane: left},
		right: &Node{x: 6, y: 0, w: 5, h: h, pane: right},
	}
	app := &App{
		screen: base,
		root:   root,
		active: right,
		oscBuf: newOSCBuffer(),
		theme:  testTheme(),
	}

	app.render()
	mainc, _, _, _ := base.GetContent(0, 0) //nolint:staticcheck // rune-level access
	if mainc != 'L' {
		t.Fatalf("pre-zoom left pane cell = %q, want 'L'", mainc)
	}

	// Zoom the right pane fullscreen and have it paint across the whole
	// width — like btop redrawing after the zoom SIGWINCH.
	app.zoomIn()
	right.mu.Lock()
	right.captureAndWrite([]byte("\rBBBBBBBBBB"))
	right.mu.Unlock()
	app.render()
	mainc, _, _, _ = base.GetContent(0, 0) //nolint:staticcheck // rune-level access
	if mainc != 'B' {
		t.Fatalf("zoomed pane cell over left pane region = %q, want 'B'", mainc)
	}

	app.zoomOut()
	app.render()
	mainc, _, _, _ = base.GetContent(0, 0) //nolint:staticcheck // rune-level access
	if mainc != 'L' {
		t.Fatalf("post-zoom left pane cell = %q, want 'L' (stale zoomed content not repainted)", mainc)
	}
}

func TestRender_HidesCursorDuringInPlaceLineUpdate(t *testing.T) {
	const (
		w = 24
		h = 1
	)
	term := vt10x.New(vt10x.WithSize(w-1, h))
	term.Write([]byte("  99% |==========|")) //nolint:errcheck
	p := &Pane{
		term:                      term,
		cmd:                       &exec.Cmd{},
		x:                         0,
		y:                         0,
		w:                         w,
		h:                         h,
		scrollbackLines:           100,
		sb:                        sbRing{maxLines: 100},
		progressCursorHiddenUntil: time.Now().Add(time.Second),
	}

	base := tcell.NewSimulationScreen("UTF-8")
	base.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer base.Fini()
	base.SetSize(w, h)

	app := &App{
		screen: base,
		root:   &Node{pane: p},
		active: p,
		oscBuf: newOSCBuffer(),
		theme:  testTheme(),
	}

	app.render()
	_, _, visible := base.GetCursor()
	if visible {
		t.Fatal("cursor visible during in-place line update, want hidden")
	}

	p.mu.Lock()
	p.progressCursorHiddenUntil = time.Now().Add(-time.Second)
	p.mu.Unlock()
	app.render()
	_, _, visible = base.GetCursor()
	if !visible {
		t.Fatal("cursor hidden after in-place line update settled, want visible")
	}
}

// ---------------------------------------------------------------------------
// Wide-character rendering
//
// Regression: emoji and CJK characters occupy 2 terminal columns.  vt10x
// stores a zero-char continuation glyph at col+1.  Before the fix, renderPane
// converted that zero to ' ' and called SetContent(col+1, ' '), overwriting
// tcell's internal combining-placeholder.  At Show() time tcell had to emit a
// cursor-back sequence to write the space, erasing the right half of the emoji
// and corrupting column alignment for every character that followed (e.g. the
// │ borders in copilot-cli's output tables).
// ---------------------------------------------------------------------------

// TestRenderPane_WideChar_CombiningPlaceholderPreserved checks that after
// rendering a wide rune (🔴, 2 cells), the adjacent col+1 still holds
// tcell's combining-placeholder (width=0), not a space (width=1).
func TestRenderPane_WideChar_CombiningPlaceholderPreserved(t *testing.T) {
	const (
		w = 12
		h = 3
	)
	term := vt10x.New(vt10x.WithSize(w-1, h))
	term.Write([]byte("🔴X")) //nolint:errcheck

	p := &Pane{
		term:            term,
		cmd:             &exec.Cmd{},
		x:               0,
		y:               0,
		w:               w,
		h:               h,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(w, h)

	renderPane(scr, p, testTheme())

	// Col 0: the wide emoji must be present.
	mainc, _, _, width := scr.GetContent(0, 0) //nolint:staticcheck // rune-level access; Get() returns string
	if mainc != '🔴' {
		t.Errorf("col 0: got rune %q, want '🔴'", mainc)
	}
	if width != 2 {
		t.Errorf("col 0: got width %d, want 2", width)
	}

	// Col 1: must NOT have the wide char or 'X' — the displayCol remapping
	// means col 1 is the visual right-half of the emoji (the combining slot).
	// In tcell's SimulationScreen the combining placeholder is not auto-set,
	// but we must NOT have called SetContent(1, 'X') there (column shift).
	mainc, _, _, _ = scr.GetContent(1, 0) //nolint:staticcheck // rune-level access
	if mainc == 'X' {
		t.Errorf("col 1: got 'X', want the combining slot to be empty — column shift bug present")
	}

	// Col 2: narrow 'X' must be at exactly column 2, not shifted to 3.
	mainc, _, _, _ = scr.GetContent(2, 0) //nolint:staticcheck // rune-level access
	if mainc != 'X' {
		t.Errorf("col 2: got rune %q, want 'X' — column shifted after wide char", mainc)
	}
}

// TestRenderPane_WideChar_TableBordersAligned checks that box-drawing
// characters used as column separators (│, U+2502) land at the correct screen
// column after a wide emoji in the same row.  This is the exact pattern from
// copilot-cli's output tables: "│ 🔴 High │ ...".
func TestRenderPane_WideChar_TableBordersAligned(t *testing.T) {
	const (
		w = 20
		h = 3
	)
	term := vt10x.New(vt10x.WithSize(w-1, h))
	// "A🔴B│C": A at col 0, 🔴 at col 1 (2 cells), B at col 3, │ at col 4, C at col 5.
	term.Write([]byte("A🔴B│C")) //nolint:errcheck

	p := &Pane{
		term:            term,
		cmd:             &exec.Cmd{},
		x:               0,
		y:               0,
		w:               w,
		h:               h,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(w, h)

	renderPane(scr, p, testTheme())

	wants := []struct {
		col  int
		want rune
		desc string
	}{
		{0, 'A', "col 0: 'A'"},
		{1, '🔴', "col 1: wide emoji"},
		// col 2 is the combining placeholder — skip rune check
		{3, 'B', "col 3: 'B' after wide emoji"},
		{4, '│', "col 4: box-drawing '│' (table border)"},
		{5, 'C', "col 5: 'C' after table border"},
	}
	for _, tc := range wants {
		mainc, _, _, _ := scr.GetContent(tc.col, 0) //nolint:staticcheck // rune-level access
		if mainc != tc.want {
			t.Errorf("%s: got %q, want %q", tc.desc, mainc, tc.want)
		}
	}
}

// TestRenderPane_WideChar_MultipleConsecutive checks that two consecutive wide
// chars each get their own combining-placeholder and narrow chars after them
// are correctly positioned.
func TestRenderPane_WideChar_MultipleConsecutive(t *testing.T) {
	const (
		w = 16
		h = 3
	)
	term := vt10x.New(vt10x.WithSize(w-1, h))
	// 🔴🟡Z: 🔴 at 0-1, 🟡 at 2-3, Z at 4.
	term.Write([]byte("🔴🟡Z")) //nolint:errcheck

	p := &Pane{
		term:            term,
		cmd:             &exec.Cmd{},
		x:               0,
		y:               0,
		w:               w,
		h:               h,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(w, h)

	renderPane(scr, p, testTheme())

	// Both continuation slots must NOT hold the character that follows —
	// that would indicate a column shift (the old bug).
	for _, tc := range []struct {
		col     int
		notWant rune
	}{
		{1, '🟡'}, // right-half of 🔴 — must not hold the next wide char
		{3, 'Z'}, // right-half of 🟡 — must not hold 'Z'
	} {
		mainc, _, _, _ := scr.GetContent(tc.col, 0) //nolint:staticcheck // rune-level access
		if mainc == tc.notWant {
			t.Errorf("col %d: got %q, which indicates a column-shift bug (char should be at col+1)",
				tc.col, tc.notWant)
		}
	}
	// 'Z' must be at column 4, not shifted to 5 or 6.
	mainc, _, _, _ := scr.GetContent(4, 0) //nolint:staticcheck // rune-level access
	if mainc != 'Z' {
		t.Errorf("col 4: got %q, want 'Z' — column shift after consecutive wide chars", mainc)
	}
}

// ---------------------------------------------------------------------------
// tcellColorToXParse
// ---------------------------------------------------------------------------

func TestTcellColorToXParse_RGB(t *testing.T) {
	// Exact RGB colour: each component doubled to 16-bit.
	c := tcell.NewRGBColor(0xd0, 0xd0, 0xd0)
	got := tcellColorToXParse(c)
	if got != "rgb:d0d0/d0d0/d0d0" {
		t.Errorf("got %q, want rgb:d0d0/d0d0/d0d0", got)
	}
}

func TestTcellColorToXParse_DarkBG(t *testing.T) {
	c := tcell.NewRGBColor(0x1a, 0x1a, 0x2e)
	got := tcellColorToXParse(c)
	if got != "rgb:1a1a/1a1a/2e2e" {
		t.Errorf("got %q, want rgb:1a1a/1a1a/2e2e", got)
	}
}

func TestTcellColorToXParse_Default_Unknown(t *testing.T) {
	// tcell.ColorDefault has no RGB — return empty so callers can suppress
	// OSC replies instead of fabricating a colour.
	got := tcellColorToXParse(tcell.ColorDefault)
	if got != "" {
		t.Errorf("tcellColorToXParse(ColorDefault) = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// SGR 2 (dim), SGR 8 (invisible), SGR 9 (strikethrough) rendering
// ---------------------------------------------------------------------------

// TestCursorDisplayX_WideChars is the regression test for the cursor landing one
// column too far left after a wide character.  Pasting an image into Copilot
// inserts a "[📷 …]" chip; the emulator and renderer must agree on the two
// display cells occupied by the emoji.
func TestCursorDisplayX_WideChars(t *testing.T) {
	const w, h = 40, 3
	term := vt10x.New(vt10x.WithSize(w-1, h))
	// "❯ [📷 ab]" — one wide emoji before the cursor, which ends after ']'.
	term.Write([]byte("❯ [📷 ab]")) //nolint:errcheck
	p := &Pane{
		term: term, cmd: &exec.Cmd{},
		x: 0, y: 0, w: w, h: h,
		scrollbackLines: 100, sb: sbRing{maxLines: 100},
	}

	cur := term.Cursor()
	got := cursorDisplayX(p, cur)
	// Eight runes occupy nine display cells because the emoji is wide.
	if cur.X != 9 {
		t.Fatalf("vt10x cursor col = %d, want 9 display cells", cur.X)
	}
	if got != 9 {
		t.Errorf("cursorDisplayX = %d, want 9 (off-by-one left from wide emoji)", got)
	}

	// No wide chars: display column must equal the vt10x column.
	term2 := vt10x.New(vt10x.WithSize(w-1, h))
	term2.Write([]byte("hello")) //nolint:errcheck
	p2 := &Pane{term: term2, cmd: &exec.Cmd{}, x: 0, y: 0, w: w, h: h, scrollbackLines: 100, sb: sbRing{maxLines: 100}}
	c2 := term2.Cursor()
	if got := cursorDisplayX(p2, c2); got != c2.X {
		t.Errorf("ASCII: cursorDisplayX = %d, want %d (== vt10x col)", got, c2.X)
	}
}

// TestRenderPane_OSC11RepaintsBlankRows is the regression test for the Copilot
// welcome-screen background banding: an app clears the screen (blank rows get
// the theme bg), THEN sets a new default background via OSC 11.  vt10x's Cell()
// resolves DefaultBG through the override, so every cell — including blank rows
// the renderer skips via dirty tracking — should now show the override colour.
// Without a colour-generation check the blank rows keep their stale theme bg,
// producing horizontal bands of mismatched background.
func TestRenderPane_OSC11RepaintsBlankRows(t *testing.T) {
	const w, h = 10, 4
	rt := testTheme()
	term := vt10x.New(vt10x.WithSize(w-1, h))
	p := &Pane{
		term: term, cmd: &exec.Cmd{},
		x: 0, y: 0, w: w, h: h,
		scrollbackLines: 100, sb: sbRing{maxLines: 100},
	}
	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck
	defer scr.Fini()
	scr.SetSize(w, h)

	// 1. Clear the screen (no override yet) and render: blank cells show theme bg.
	p.mu.Lock()
	p.captureAndWrite([]byte("\x1b[2J\x1b[HX")) // an X on row 0, rest blank
	p.mu.Unlock()
	renderPane(scr, p, rt)
	scr.Show()

	// A blank cell on row 2 (never written) currently carries the theme bg.
	if _, bg, _ := getCellStyle(scr, 0, 2); bg != rt.bg {
		t.Fatalf("setup: blank cell bg = %v, want theme bg %v", bg, rt.bg)
	}

	// 2. App sets a new default background via OSC 11, then render again WITHOUT
	//    touching the blank row.  The colour-generation change must force a full
	//    repaint so the blank row picks up the new background.
	p.mu.Lock()
	p.captureAndWrite([]byte("\x1b]11;#0D1117\x07"))
	p.mu.Unlock()
	renderPane(scr, p, rt)
	scr.Show()

	want := tcell.NewRGBColor(0x0D, 0x11, 0x17)
	if _, bg, _ := getCellStyle(scr, 0, 2); bg != want {
		t.Errorf("after OSC 11: blank cell bg = %v, want %v (stale theme bg => banding)", bg, want)
	}
	// The written cell on row 0 must agree, so the whole pane is unified.
	if _, bg, _ := getCellStyle(scr, 0, 0); bg != want {
		t.Errorf("after OSC 11: written cell bg = %v, want %v", bg, want)
	}
}

func getCellStyle(scr tcell.SimulationScreen, x, y int) (tcell.Color, tcell.Color, tcell.AttrMask) {
	_, _, st, _ := scr.GetContent(x, y) //nolint:staticcheck // rune-level access
	return st.Decompose()
}

// renderAndGetStyle builds a minimal Pane+SimulationScreen, writes ANSI bytes
// into the vt10x terminal, renders into the screen, and returns the tcell.Style
// stored at cell (0, 0).
func renderAndGetStyle(t *testing.T, ansi string) tcell.Style {
	t.Helper()
	const (
		w = 20
		h = 3
	)
	term := vt10x.New(vt10x.WithSize(w-1, h))
	term.Write([]byte(ansi)) //nolint:errcheck

	p := &Pane{
		term: term, cmd: &exec.Cmd{},
		x: 0, y: 0, w: w, h: h,
		scrollbackLines: 100, sb: sbRing{maxLines: 100},
	}

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(w, h)

	renderPane(scr, p, testTheme())
	_, _, style, _ := scr.GetContent(0, 0) //nolint:staticcheck // rune-level access
	return style
}

func TestRenderPane_SGR2_Dim(t *testing.T) {
	// SGR 2 with RGB FG: bunk blends the FG 50% toward BG instead of relying
	// on ti.Dim, because most terminals (VTE/Ptyxis, xterm) silently ignore
	// SGR 2 when the FG colour is an explicit RGB value.
	// Verify that the rendered FG is the midpoint between normal FG and BG.
	theme := testTheme()
	normalStyle := renderAndGetStyle(t, "X")     // no dim
	dimStyle := renderAndGetStyle(t, "\x1b[2mX") // with dim

	normalFG, _, _ := normalStyle.Decompose()
	dimFG, _, _ := dimStyle.Decompose()

	if !normalFG.IsRGB() {
		t.Skip("testTheme FG is not RGB; skip RGB-blend assertion")
	}
	nr, ng, nb := normalFG.RGB()
	dr, dg, db := dimFG.RGB()
	br, bg2, bb := theme.bg.RGB()

	wantR, wantG, wantB := (nr+br)/2, (ng+bg2)/2, (nb+bb)/2
	if dr != wantR || dg != wantG || db != wantB {
		t.Errorf("SGR 2 dim: FG not blended correctly\n  got  rgb(%d,%d,%d)\n  want rgb(%d,%d,%d)",
			dr, dg, db, wantR, wantG, wantB)
	}
}

func TestRenderPane_SGR9_Strikethrough(t *testing.T) {
	// SGR 9 should result in StrikeThrough=true on the rendered cell.
	style := renderAndGetStyle(t, "\x1b[9mX")
	_, _, attr := style.Decompose()
	if attr&tcell.AttrStrikeThrough == 0 {
		t.Error("SGR 9 (strikethrough): expected tcell.AttrStrikeThrough to be set, got", attr)
	}
}

func TestRenderPane_SGR8_Invisible_RendersSpace(t *testing.T) {
	// SGR 8 (invisible): character must render as space regardless of content.
	const (
		w = 10
		h = 3
	)
	term := vt10x.New(vt10x.WithSize(w-1, h))
	term.Write([]byte("\x1b[8mX")) //nolint:errcheck

	p := &Pane{
		term: term, cmd: &exec.Cmd{},
		x: 0, y: 0, w: w, h: h,
		scrollbackLines: 100, sb: sbRing{maxLines: 100},
	}

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(w, h)

	renderPane(scr, p, testTheme())
	mainc, _, _, _ := scr.GetContent(0, 0) //nolint:staticcheck // rune-level access
	if mainc != ' ' {
		t.Errorf("SGR 8 (invisible): got rune %q at col 0, want ' ' (space)", mainc)
	}
}

func TestRenderPane_BG256Color_RendersPaletteEntry(t *testing.T) {
	style := renderAndGetStyle(t, "\x1b[48;5;196mX")
	_, bg, _ := style.Decompose()
	if bg != tcell.PaletteColor(196) {
		t.Fatalf("48;5;196 background = %v, want %v", bg, tcell.PaletteColor(196))
	}
}

func TestRenderPane_BGTrueColor_RendersRGB(t *testing.T) {
	style := renderAndGetStyle(t, "\x1b[48;2;255;128;0mX")
	_, bg, _ := style.Decompose()
	want := tcell.NewRGBColor(255, 128, 0)
	if bg != want {
		t.Fatalf("48;2;255;128;0 background = %v, want %v", bg, want)
	}
}

func TestRenderPane_SGR2_Reset_SGR0(t *testing.T) {
	// SGR 0 clears dim: FG should return to the un-blended theme colour.
	normalStyle := renderAndGetStyle(t, "X")
	resetStyle := renderAndGetStyle(t, "\x1b[2m\x1b[0mX")

	nFG, _, _ := normalStyle.Decompose()
	rFG, _, _ := resetStyle.Decompose()
	if nFG != rFG {
		t.Errorf("SGR 0 after SGR 2: FG not restored; got %v, want %v", rFG, nFG)
	}
}

func TestRenderPane_SGR9_Reset_SGR29(t *testing.T) {
	// SGR 29 must clear strikethrough.
	style := renderAndGetStyle(t, "\x1b[9m\x1b[29mX")
	_, _, attr := style.Decompose()
	if attr&tcell.AttrStrikeThrough != 0 {
		t.Error("SGR 29: expected AttrStrikeThrough to be cleared, but it is still set")
	}
}

func TestRenderPane_SGR22_ClearsDimAndBold(t *testing.T) {
	// SGR 22 (normal intensity) must clear both bold and dim.
	// For bold: AttrBold must be off.
	// For dim: FG must return to the un-blended (normal) colour.
	boldDimStyle := renderAndGetStyle(t, "\x1b[1;2mX")
	afterStyle := renderAndGetStyle(t, "\x1b[1;2m\x1b[22mX")
	normalStyle := renderAndGetStyle(t, "X")

	// Bold must be cleared.
	_, _, afterAttr := afterStyle.Decompose()
	if afterAttr&tcell.AttrBold != 0 {
		t.Error("SGR 22: expected AttrBold to be cleared, but it is still set")
	}

	// Dim must be cleared: FG returns to normal (not blended).
	nFG, _, _ := normalStyle.Decompose()
	aFG, _, _ := afterStyle.Decompose()
	if nFG != aFG {
		t.Errorf("SGR 22: dim FG not cleared; got %v, want %v (normal FG)", aFG, nFG)
	}
	_ = boldDimStyle // used above implicitly via the "1;2m" sub-test
}

// ---------------------------------------------------------------------------
// SGR 4:N — underline styles (curly, double, dotted, dashed)
// ---------------------------------------------------------------------------

func TestRenderPane_SGR4_Solid_Underline(t *testing.T) {
	style := renderAndGetStyle(t, "\x1b[4mX")
	got := style.GetUnderlineStyle()
	if got != tcell.UnderlineStyleSolid {
		t.Errorf("SGR 4: want UnderlineStyleSolid, got %v", got)
	}
}

func TestRenderPane_SGR4_1_Solid_SubParam(t *testing.T) {
	style := renderAndGetStyle(t, "\x1b[4:1mX")
	got := style.GetUnderlineStyle()
	if got != tcell.UnderlineStyleSolid {
		t.Errorf("SGR 4:1: want UnderlineStyleSolid, got %v", got)
	}
}

func TestRenderPane_SGR4_3_Curly(t *testing.T) {
	// This is the primary use-case: neovim LSP errors use 4:3.
	style := renderAndGetStyle(t, "\x1b[4:3mX")
	got := style.GetUnderlineStyle()
	if got != tcell.UnderlineStyleCurly {
		t.Errorf("SGR 4:3: want UnderlineStyleCurly, got %v", got)
	}
}

func TestRenderPane_SGR4_2_Double(t *testing.T) {
	style := renderAndGetStyle(t, "\x1b[4:2mX")
	got := style.GetUnderlineStyle()
	if got != tcell.UnderlineStyleDouble {
		t.Errorf("SGR 4:2: want UnderlineStyleDouble, got %v", got)
	}
}

func TestRenderPane_SGR4_4_Dotted(t *testing.T) {
	style := renderAndGetStyle(t, "\x1b[4:4mX")
	got := style.GetUnderlineStyle()
	if got != tcell.UnderlineStyleDotted {
		t.Errorf("SGR 4:4: want UnderlineStyleDotted, got %v", got)
	}
}

func TestRenderPane_SGR4_5_Dashed(t *testing.T) {
	style := renderAndGetStyle(t, "\x1b[4:5mX")
	got := style.GetUnderlineStyle()
	if got != tcell.UnderlineStyleDashed {
		t.Errorf("SGR 4:5: want UnderlineStyleDashed, got %v", got)
	}
}

func TestRenderPane_SGR4_0_ClearsUnderline(t *testing.T) {
	// 4:0 should turn underline off.
	style := renderAndGetStyle(t, "\x1b[4m\x1b[4:0mX")
	got := style.GetUnderlineStyle()
	if got != tcell.UnderlineStyleNone {
		t.Errorf("SGR 4:0: want UnderlineStyleNone (off), got %v", got)
	}
}

func TestRenderPane_SGR24_ClearsUnderline(t *testing.T) {
	// SGR 24 must clear the underline (including any style bits).
	style := renderAndGetStyle(t, "\x1b[4:3m\x1b[24mX")
	got := style.GetUnderlineStyle()
	if got != tcell.UnderlineStyleNone {
		t.Errorf("SGR 24: want UnderlineStyleNone, got %v", got)
	}
}

func TestRenderPane_SGR0_ClearsUnderlineStyle(t *testing.T) {
	// SGR 0 must clear curly underline.
	style := renderAndGetStyle(t, "\x1b[4:3m\x1b[0mX")
	got := style.GetUnderlineStyle()
	if got != tcell.UnderlineStyleNone {
		t.Errorf("SGR 0 after 4:3: want UnderlineStyleNone, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Scrollback background seam
//
// Bug: after resize, columns beyond the old terminal width show ANSI black
// (Color 0) instead of the theme background in scrollback rows.
//
// Scrollback rows captured at oldCols have len(row) == oldCols.  In the render
// loop, when col >= len(cells), the cell was left as a zero-value Glyph
// (BG=0 = ANSI black), which renders as rt.palette[0] — not rt.bg.  A visible
// seam appears between scrollback rows (BG = ANSI black on the right edge)
// and live rows (BG = DefaultBG = rt.bg).
//
// Fix: initialise the cell to DefaultFG/DefaultBG so that unpopulated
// positions render with the theme's background colour.
// ---------------------------------------------------------------------------

func TestRenderPane_ScrollbackBeyondWidth_UsesThemeBG(t *testing.T) {
	const (
		oldCols = 5
		newCols = 8
		rows    = 4
		paneW   = newCols + 1 // +1 for scrollbar column
	)

	rt := testTheme()
	// rt.bg (DefaultBG) must differ from rt.palette[0] (ANSI black) for the
	// test to detect the seam.  testTheme guarantees this:
	//   rt.bg      = #1a1a2e (dark blue)
	//   palette[0] = #000000 (pure black)
	if rt.bg == rt.palette[0] {
		t.Skip("testTheme bg == palette[0]; cannot distinguish the seam")
	}

	// Build a pane whose live terminal is newCols wide.
	term := vt10x.New(vt10x.WithSize(newCols, rows))
	p := &Pane{
		term:            term,
		cmd:             &exec.Cmd{},
		x:               0,
		y:               0,
		w:               paneW,
		h:               rows,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	// Manually push a scrollback row captured at oldCols width (content 'A').
	sbRow := make([]vt10x.Glyph, oldCols)
	for i := range sbRow {
		sbRow[i] = vt10x.Glyph{Char: 'A', FG: vt10x.DefaultFG, BG: vt10x.DefaultBG}
	}
	p.sb.push(sbRow)

	// Enter scrollback mode (show the one captured row at screen row 0).
	p.sbOff = 1

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(paneW, rows)

	renderPane(scr, p, rt)

	// Row 0 is the scrollback row.  Columns 0..oldCols-1 come from sbRow;
	// columns oldCols..newCols-1 are beyond sbRow's width and must render
	// with rt.bg (DefaultBG), NOT rt.palette[0] (ANSI black = Color 0).
	for col := oldCols; col < newCols; col++ {
		_, _, style, _ := scr.GetContent(col, 0) //nolint:staticcheck // rune-level access
		_, bg, _ := style.Decompose()
		if bg != rt.bg {
			t.Errorf("scrollback row 0 col %d: bg = %v, want rt.bg = %v (got ANSI black seam)",
				col, bg, rt.bg)
		}
	}
}

// ---------------------------------------------------------------------------
// Dirty-row rendering — renderPane skips rows vt10x hasn't touched
// ---------------------------------------------------------------------------

// screenRowFingerprints captures the rune at column 0 for each row as a quick
// proxy for "which rows changed" in the simulation screen.
func screenRowFingerprints(scr tcell.SimulationScreen, rows int) []rune {
	fp := make([]rune, rows)
	for r := 0; r < rows; r++ {
		ch, _, _, _ := scr.GetContent(0, r) //nolint:staticcheck // rune-level access
		fp[r] = ch
	}
	return fp
}

func screenRowString(scr tcell.SimulationScreen, y, cols int) string {
	runes := make([]rune, cols)
	for x := 0; x < cols; x++ {
		ch, _, _, _ := scr.GetContent(x, y) //nolint:staticcheck // rune-level access
		if ch == 0 {
			ch = ' '
		}
		runes[x] = ch
	}
	return string(runes)
}

// TestRenderPane_DirtyOnlyRepaintsChangedRow verifies that when only one row
// changes in the live terminal, renderPane only repaints that row and leaves
// the others unchanged in the tcell simulation screen.
func TestRenderPane_DirtyOnlyRepaintsChangedRow(t *testing.T) {
	const (
		cols     = 10
		termCols = cols - 1 // PTY is 1 column narrower (scrollbar)
		rows     = 5
	)

	p := &Pane{
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
		x:               0, y: 0, w: cols, h: rows,
		cmd: &exec.Cmd{},
	}
	p.term = vt10x.New(vt10x.WithSize(termCols, rows), vt10x.WithScrollSwapCallback(p.onScrollSwap))

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(cols, rows)
	rt := testTheme()

	// Write distinct content to each row.
	p.mu.Lock()
	p.captureAndWrite([]byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\nEEEEEEEEEE"))
	p.mu.Unlock()

	// First render: full repaint (all rows dirty from initial state).
	renderPane(scr, p, rt)

	before := screenRowFingerprints(scr, rows)

	// Overwrite only row 2 (0-based) with exactly termCols characters.
	// Using fewer than termCols avoids wrapping into row 3.
	p.mu.Lock()
	p.captureAndWrite([]byte("\x1b[3;1HXXXXXXXXX")) // CSI 3;1H = row 3 col 1 (1-based = row 2 0-based); 9 chars = termCols
	p.mu.Unlock()

	// Second render: dirty-only — only row 2 should change.
	renderPane(scr, p, rt)

	after := screenRowFingerprints(scr, rows)

	for r := 0; r < rows; r++ {
		if r == 2 {
			if after[r] == before[r] {
				t.Errorf("row %d: expected change after write, but rune unchanged (%q)", r, after[r])
			}
		} else {
			if after[r] != before[r] {
				t.Errorf("row %d: expected no change (dirty-only), but rune changed %q → %q",
					r, before[r], after[r])
			}
		}
	}
}

// TestRenderPane_IdlePaneSkipsAllRows verifies that when no rows are dirty and
// no overlay changed, renderPane returns immediately without touching the screen.
func TestRenderPane_IdlePaneSkipsAllRows(t *testing.T) {
	const cols, rows = 10, 4

	p := &Pane{
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
		x:               0, y: 0, w: cols, h: rows,
		cmd: &exec.Cmd{},
	}
	p.term = vt10x.New(vt10x.WithSize(cols-1, rows))

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(cols, rows)
	rt := testTheme()

	p.mu.Lock()
	p.captureAndWrite([]byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC"))
	p.mu.Unlock()

	renderPane(scr, p, rt) // first render, drains dirty

	before := screenRowFingerprints(scr, rows)

	// Second render with no writes: should be a no-op.
	renderPane(scr, p, rt)

	after := screenRowFingerprints(scr, rows)

	for r := 0; r < rows; r++ {
		if after[r] != before[r] {
			t.Errorf("idle render changed row %d: %q → %q", r, before[r], after[r])
		}
	}
}

// TestRenderPane_FullRepaintOnScrollChange verifies that changing sbOff
// triggers a full repaint (even for rows vt10x considers clean).
func TestRenderPane_FullRepaintOnScrollChange(t *testing.T) {
	const cols, rows = 10, 3

	p := &Pane{
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
		x:               0, y: 0, w: cols, h: rows,
		cmd: &exec.Cmd{},
	}
	p.term = vt10x.New(vt10x.WithSize(cols-1, rows), vt10x.WithScrollSwapCallback(p.onScrollSwap))

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(cols, rows)
	rt := testTheme()

	// Fill enough content to create scrollback.
	p.mu.Lock()
	p.captureAndWrite([]byte("AAAAAAAAAA\r\nBBBBBBBBBB\r\nCCCCCCCCCC\r\nDDDDDDDDDD\r\nEEEEEEEEEE\r\n"))
	p.mu.Unlock()

	renderPane(scr, p, rt) // first render

	if p.sb.count == 0 {
		t.Skip("no scrollback captured; cannot test scroll change")
	}

	before := screenRowFingerprints(scr, rows)

	// Scroll back: sbOff > 0, full repaint required.
	p.mu.Lock()
	p.sbOff = p.sb.count
	p.mu.Unlock()

	renderPane(scr, p, rt)

	after := screenRowFingerprints(scr, rows)

	// At least one row must have changed (scrollback content differs from live).
	changed := 0
	for r := 0; r < rows; r++ {
		if after[r] != before[r] {
			changed++
		}
	}
	if changed == 0 {
		t.Error("scroll to top: expected at least one row to change, but all are identical")
	}
}

// TestRenderPane_FullRepaintOnStatusOverlayChange verifies that top-row status
// badges are treated as an overlay invalidation source. Without this, an
// expired badge can leave stale text behind on an otherwise idle pane because
// drawPaneStatus stops drawing before renderPane repaints the underlying row.
func TestRenderPane_FullRepaintOnStatusOverlayChange(t *testing.T) {
	const cols, rows = 14, 2

	p := &Pane{
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
		x:               0,
		y:               0,
		w:               cols,
		h:               rows,
		cmd:             &exec.Cmd{},
	}
	p.term = vt10x.New(vt10x.WithSize(cols-1, rows), vt10x.WithScrollSwapCallback(p.onScrollSwap))

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(cols, rows)
	rt := testTheme()
	node := &Node{pane: p}

	p.mu.Lock()
	p.captureAndWrite([]byte("hello world"))
	p.statusMsg = "COPIED"
	p.statusMsgEnd = time.Now().Add(time.Minute)
	p.mu.Unlock()

	renderPane(scr, p, rt)
	drawScrollbars(scr, node, rt)
	drawPaneStatus(scr, p, true, rt, false)

	withBadge := screenRowString(scr, 0, cols)
	if !strings.Contains(withBadge, "COPIED") {
		t.Fatalf("initial render missing status badge: row=%q", withBadge)
	}

	p.mu.Lock()
	p.statusMsgEnd = time.Now().Add(-time.Second)
	p.mu.Unlock()

	renderPane(scr, p, rt)
	drawScrollbars(scr, node, rt)
	drawPaneStatus(scr, p, true, rt, false)

	withoutBadge := screenRowString(scr, 0, cols)
	if strings.Contains(withoutBadge, "COPIED") {
		t.Fatalf("expired status badge left stale text behind: row=%q", withoutBadge)
	}
	if !strings.Contains(withoutBadge, "hello") {
		t.Fatalf("underlying row was not restored after badge expiry: row=%q", withoutBadge)
	}
}

// ---------------------------------------------------------------------------
// sanitizeTitle: pane titles that came from binary content must be stripped
// of control bytes before bunk re-emits them via OSC 0 to the host terminal.
// ---------------------------------------------------------------------------

func TestSanitizeTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain ascii", "my-title", "my-title"},
		{"unicode preserved", "café — 日本語", "café — 日本語"},
		{"strip C0 controls", "before\x01\x02\x03after", "beforeafter"},
		{"strip ESC", "evil\x1b]0;hijack\x07tail", "evil]0;hijacktail"},
		{"strip DEL", "x\x7fy", "xy"},
		{"strip C1 controls", "x\u0080\u009fy", "xy"},
		{"strip nul", "a\x00b", "ab"},
		{"invalid utf8 stripped", "good\xff\xfetail", "goodtail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeTitle(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeTitle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeTitle_LengthCap(t *testing.T) {
	// Long titles get truncated to keep OSC 0 small.
	in := strings.Repeat("A", 1000)
	got := sanitizeTitle(in)
	if len(got) > 512 {
		t.Errorf("sanitizeTitle produced %d bytes, want <= 512", len(got))
	}
}

// ---------------------------------------------------------------------------
// OSC 8 hyperlinks: end-to-end render pipeline tests.
//
// These tests exist because earlier unit-tests on vt10x's per-glyph Link
// state passed while the actual rendered output (`ls --hyperlink=auto` in
// bunk) corrupted the next prompt.  The bug lived between vt10x and the host
// terminal — in tcell's emission, not in our state.  These tests inspect
// what bunk actually pushes into tcell's cell buffer (via SimulationScreen)
// and what bunk writes to the host's stdout (via the closeHostHyperlink
// helper) — the layers that earlier tests skipped over.
// ---------------------------------------------------------------------------

// hasURL returns true when style carries any non-empty OSC 8 url. tcell's
// Style.url field is unexported, so we detect the URL by exploiting equality:
// Style.Url("") clears the url; if the result equals the original, the
// original already had url="".
func hasURL(s tcell.Style) bool { return s != s.Url("") }

// styleURL returns the URL on style if any. Equality probe against several
// candidates would be expensive; we instead compare s to s.Url(want) and
// return want when they match. Used in tests that already know the candidate.
func styleURLEquals(s tcell.Style, want string) bool { return s == s.Url(want) }

func TestCloseHostHyperlink_WritesOSC8Close(t *testing.T) {
	var buf bytes.Buffer
	closeHostHyperlink(&buf)
	got := buf.Bytes()
	want := []byte("\x1b]8;;\x1b\\")
	if !bytes.Equal(got, want) {
		t.Errorf("closeHostHyperlink wrote %q, want %q", got, want)
	}
}

// TestRenderPane_HyperlinkCellsCarryURL verifies that bunk's renderPane
// stages each cell of a hyperlinked region with style.Url(URL) so tcell
// will emit OSC 8 around the right characters.
func TestRenderPane_HyperlinkCellsCarryURL(t *testing.T) {
	const (
		w = 20
		h = 3
	)
	term := vt10x.New(vt10x.WithSize(w-1, h))
	// `ls --hyperlink=auto`-style: open, name, close, space, plain text.
	term.Write([]byte("\x1b]8;;file:///x\x1b\\foo\x1b]8;;\x1b\\ bar")) //nolint:errcheck

	p := &Pane{
		term:            term,
		cmd:             &exec.Cmd{},
		w:               w,
		h:               h,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(w, h)
	renderPane(scr, p, testTheme())

	// Cells 0..2 ("foo") must carry the URL.
	for col := 0; col < 3; col++ {
		_, _, style, _ := scr.GetContent(col, 0) //nolint:staticcheck // rune-level access
		if !hasURL(style) {
			t.Errorf("col %d (inside link): style has no URL set", col)
		}
		if !styleURLEquals(style, "file:///x") {
			t.Errorf("col %d: URL is not 'file:///x'", col)
		}
	}

	// Cells 3+ (" bar") must NOT carry a URL — the link was closed.
	for col := 3; col < 7; col++ {
		_, _, style, _ := scr.GetContent(col, 0) //nolint:staticcheck // rune-level access
		if hasURL(style) {
			t.Errorf("col %d (after close): style still carries a URL — leak past close", col)
		}
	}
}

// TestRenderPane_PS1AfterLsDoesNotCarryURL is the regression test for the
// original "ls --hyperlink=auto then PS1 is highlighted" bug.  A full
// post-exec stream (OSC 0/3008/666/7) lands between the last hyperlink close
// and the new PS1 — none of it should reattach a URL onto PS1's cells.
func TestRenderPane_PS1AfterLsDoesNotCarryURL(t *testing.T) {
	const (
		w = 80
		h = 20
	)
	term := vt10x.New(vt10x.WithSize(w-1, h))
	term.Write([]byte( //nolint:errcheck
		// Two hyperlinked filenames followed by a newline.
		"\x1b]8;;file:///A\x1b\\AGENTS.MD\x1b]8;;\x1b\\ " +
			"\x1b]8;;file:///B\x1b\\go.mod\x1b]8;;\x1b\\\r\n" +
			// Bash post-exec OSCs (verbatim shape from /tmp/bunk.log).
			"\x1b]0;jsn@ydell:~/x\a" +
			"\x1b]3008;end=abc\x1b\\" +
			"\x1b]666;vte.shell.precmd!\x1b\\" +
			"\x1b]7;file:///x\x1b\\" +
			// New PS1 with SGR styling, no OSC 8.
			"\x1b[01;32mjsn@ydell\x1b[0m \x1b[01;34m~/x\x1b[0m\r\n$ "))

	p := &Pane{
		term:            term,
		cmd:             &exec.Cmd{},
		w:               w,
		h:               h,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(w, h)
	renderPane(scr, p, testTheme())

	// Find any row containing "jsn@ydell" — that's PS1.  Every cell on
	// that row, AND every cell on the row containing the cursor "$ ",
	// must be link-free.
	for row := 0; row < h; row++ {
		var rowText []rune
		for col := 0; col < w; col++ {
			r, _, _, _ := scr.GetContent(col, row) //nolint:staticcheck // rune-level access
			rowText = append(rowText, r)
		}
		s := string(rowText)
		if !strings.Contains(s, "jsn@ydell") && !strings.Contains(s, "$ ") {
			continue
		}
		for col := 0; col < w; col++ {
			_, _, style, _ := scr.GetContent(col, row) //nolint:staticcheck // rune-level access
			if hasURL(style) {
				t.Errorf("PS1 row %d col %d (%q) carries a URL — link leaked past ls output",
					row, col, rowText[col])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// drawSearchBar: chord-set hint on the right.
// ---------------------------------------------------------------------------

func TestCompactKeyName(t *testing.T) {
	cases := map[string]string{
		"ctrl+n":     "^N",
		"ctrl+v":     "^V",
		"ctrl+p":     "^P",
		"alt+r":      "M-R",
		"shift+pgup": "S-Pgup",
		"escape":     "Esc",
		"esc":        "Esc",
		"enter":      "↵",
		"return":     "↵",
		"tab":        "⇥",
		"backspace":  "⌫",
		"f1":         "F1",
		"pgdn":       "Pgdn",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := compactKeyName(in); got != want {
				t.Errorf("compactKeyName(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// readBarRow reads the bottom row of pane p as a string of visible runes.
func readBarRow(t *testing.T, scr tcell.SimulationScreen, x, y, w int) string {
	t.Helper()
	var b strings.Builder
	for col := x; col < x+w; col++ {
		r, _, _, _ := scr.GetContent(col, y) //nolint:staticcheck // rune-level access
		b.WriteRune(r)
	}
	return b.String()
}

func TestDrawSearchBar_WideEnoughShowsHint(t *testing.T) {
	const w, h = 80, 5
	p := &Pane{x: 0, y: 0, w: w, h: h}
	kb := resolveKeybindings(nil) // built-in defaults

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(w, h)
	drawSearchBar(scr, p, &kb, "needle", 2, 5)

	row := readBarRow(t, scr, 0, h-1, w)
	// Left side: query and counter.
	if !strings.Contains(row, " Search: needle  3/5 ") {
		t.Errorf("missing label in row: %q", row)
	}
	// Right side: hint mentions next/prev/exit (paste intentionally omitted —
	// users discover Ctrl+V via OS muscle memory; we save horizontal space).
	for _, want := range []string{"^N next", "^P prev", "Esc exit"} {
		if !strings.Contains(row, want) {
			t.Errorf("hint missing %q in row: %q", want, row)
		}
	}
	if strings.Contains(row, "paste") {
		t.Errorf("hint should not mention paste, got: %q", row)
	}
}

func TestDrawSearchBar_NarrowSuppressesHint(t *testing.T) {
	// 24 cols leaves no room for the hint after label + cursor + gap.
	const w, h = 24, 5
	p := &Pane{x: 0, y: 0, w: w, h: h}
	kb := resolveKeybindings(nil)

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(w, h)
	drawSearchBar(scr, p, &kb, "x", 0, 1)

	row := readBarRow(t, scr, 0, h-1, w)
	// Hint markers must NOT appear — we drop the hint rather than truncate it.
	if strings.Contains(row, "next") || strings.Contains(row, "exit") {
		t.Errorf("narrow bar should suppress hint, but got: %q", row)
	}
}

func TestDrawSearchBar_RemappedKeysAppearInHint(t *testing.T) {
	// User remapped search_next to f3; hint should reflect it.
	const w, h = 80, 5
	p := &Pane{x: 0, y: 0, w: w, h: h}
	kb := resolveKeybindings(map[string]string{"search_next": "f3"})

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(w, h)
	drawSearchBar(scr, p, &kb, "x", 0, 1)

	row := readBarRow(t, scr, 0, h-1, w)
	if !strings.Contains(row, "F3 next") {
		t.Errorf("hint should show F3 next after remap, got: %q", row)
	}
	if strings.Contains(row, "^N next") {
		t.Errorf("hint should NOT still show ^N next after remap, got: %q", row)
	}
}

// captureTty implements tcell.Tty by writing into a bytes.Buffer.  Used to
// inspect the byte stream a real terminfo Screen emits, including the
// OSC 8 transitions that earlier tests (using SimulationScreen) miss.
type captureTty struct {
	out      bytes.Buffer
	resizeCb func()
}

func (t *captureTty) Read(p []byte) (int, error)  { return 0, nil } // no input
func (t *captureTty) Write(p []byte) (int, error) { return t.out.Write(p) }
func (t *captureTty) Close() error                { return nil }
func (t *captureTty) Start() error                { return nil }
func (t *captureTty) Stop() error                 { return nil }
func (t *captureTty) Drain() error                { return nil }
func (t *captureTty) NotifyResize(cb func())      { t.resizeCb = cb }
func (t *captureTty) WindowSize() (tcell.WindowSize, error) {
	return tcell.WindowSize{Width: 80, Height: 24, PixelWidth: 800, PixelHeight: 600}, nil
}

// TestRender_HostByteStream_NoLinkLeakAcrossFrames is the byte-stream
// regression test for the user-reported bug: after `ls --hyperlink=auto` the
// next PS1 line was rendered as part of the last filename's hyperlink.
//
// The bug is CROSS-FRAME — within a single tcell.Show() draw pass, tcell
// correctly emits OSC 8 transitions between cells.  The leak happens between
// frames: tcell.draw() resets t.curstyle = styleInvalid (whose .url is "")
// at the start of each frame, so a frame whose first dirty cell has url=""
// never triggers an exitUrl emission even though the previous frame ended
// with the host inside a hyperlink.
//
// This test reproduces the cross-frame scenario:
//  1. Frame 1: render a hyperlinked filename so tcell ends with url="X" on
//     the wire.  scr.Show() flushes, leaving the host inside the link.
//  2. Frame 2: vt10x writes plain PS1 text into a different row.  Only the
//     PS1 row is dirty.  scr.Show() flushes again — without our fix, no
//     exitUrl is emitted, and PS1 chars land inside the leftover hyperlink.
//
// We assert that between frame 1 and frame 2, the byte stream contains an
// OSC 8 close before any PS1 character.  If closeHostHyperlink() is removed
// from render's frame-trailer code path, this test fails.
func TestRender_HostByteStream_NoLinkLeakAcrossFrames(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	tty := &captureTty{}
	scr, err := tcell.NewTerminfoScreenFromTty(tty)
	if err != nil {
		t.Skipf("terminfo screen unavailable: %v", err)
	}
	if err := scr.Init(); err != nil {
		t.Skipf("screen Init failed: %v", err)
	}
	defer scr.Fini()
	const w, h = 80, 24
	scr.SetSize(w, h)

	term := vt10x.New(vt10x.WithSize(w-1, h))

	// --- Frame 1: hyperlinked filename, leaves tcell.curstyle.url == "X".
	term.Write([]byte("\x1b]8;;file:///A\x1b\\AGENTS.MD\x1b]8;;\x1b\\")) //nolint:errcheck
	p := &Pane{
		term: term, cmd: &exec.Cmd{}, w: w, h: h,
		scrollbackLines: 100, sb: sbRing{maxLines: 100},
	}
	renderPane(scr, p, testTheme())
	closeHostHyperlink(&tty.out) // mirror render() trailer
	scr.Show()
	frame1Bytes := tty.out.Len()

	// --- Frame 2: write PS1 on a fresh row.  This dirties new cells whose
	// style.url == "" — without an explicit exitUrl from us, tcell will not
	// emit one (per the styleInvalid.url = "" bug in tscreen.go).
	term.Write([]byte("\r\n\x1b[01;32mjsn@ydell\x1b[0m\r\n$ ")) //nolint:errcheck
	renderPane(scr, p, testTheme())
	closeHostHyperlink(&tty.out) // ← the line under test
	scr.Show()

	// Inspect ONLY the bytes emitted by frame 2 (to avoid matching the
	// frame-1 in-stream OSC 8 closes that bracket the filename itself).
	frame2 := tty.out.String()[frame1Bytes:]
	closeIdx := strings.Index(frame2, "\x1b]8;;\x1b\\")
	psIdx := strings.Index(frame2, "jsn@ydell")
	if psIdx == -1 {
		t.Fatalf("frame 2 did not paint PS1:\n%q", frame2)
	}
	if closeIdx == -1 || closeIdx > psIdx {
		t.Errorf("frame 2 emitted PS1 chars without a preceding OSC 8 close:\n  closeIdx=%d psIdx=%d\n  bytes=%q",
			closeIdx, psIdx, frame2)
	}
}

// TestRenderPane_TrailingClearedCellsAfterLink covers the scrollUp /
// clear-to-EOL path: when clear() runs while cur.Attr.Link != 0 it must NOT
// stamp the trailing whitespace of the affected row with a hyperlink.  This
// catches the in-memory leak fixed by zeroing Link in clear().
func TestRenderPane_TrailingClearedCellsAfterLink(t *testing.T) {
	const (
		w = 20
		h = 3
	)
	term := vt10x.New(vt10x.WithSize(w-1, h))
	// Open link, write 3 chars, clear-to-EOL while link is still open, close.
	term.Write([]byte("\x1b]8;;file:///x\x1b\\foo\x1b[K\x1b]8;;\x1b\\")) //nolint:errcheck

	p := &Pane{
		term:            term,
		cmd:             &exec.Cmd{},
		w:               w,
		h:               h,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init() //nolint:errcheck // simulation screen init does not fail in tests
	defer scr.Fini()
	scr.SetSize(w, h)
	renderPane(scr, p, testTheme())

	// "foo" carries the link.
	for col := 0; col < 3; col++ {
		_, _, style, _ := scr.GetContent(col, 0) //nolint:staticcheck // rune-level access
		if !hasURL(style) {
			t.Errorf("col %d (inside link): style has no URL", col)
		}
	}
	// Trailing cells must not.
	for col := 3; col < w-1; col++ {
		_, _, style, _ := scr.GetContent(col, 0) //nolint:staticcheck // rune-level access
		if hasURL(style) {
			t.Errorf("col %d (trailing cleared cell): carries URL — clear() leaked Link", col)
		}
	}
}
