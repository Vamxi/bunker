package main

import (
	"fmt"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// helper: build an EventKey for a plain rune (no modifier, no special key).
func runeEv(r rune, mod tcell.ModMask) *tcell.EventKey {
	return tcell.NewEventKey(tcell.KeyRune, r, mod)
}

// helper: build an EventKey for a special (non-rune) key.
func keyEv(k tcell.Key, mod tcell.ModMask) *tcell.EventKey {
	return tcell.NewEventKey(k, 0, mod)
}

// ---------------------------------------------------------------------------
// parseKey + Keybinding.Matches: alt+letter, ctrl+letter, case-fold.
// ---------------------------------------------------------------------------

func TestParseKey_AltLetter(t *testing.T) {
	kb, err := parseKey("alt+r")
	if err != nil {
		t.Fatalf("parseKey(alt+r): %v", err)
	}
	if kb.key != tcell.KeyRune {
		t.Errorf("key = %v, want KeyRune", kb.key)
	}
	if kb.r != 'r' {
		t.Errorf("rune = %q, want 'r'", kb.r)
	}
	if kb.mod != tcell.ModAlt {
		t.Errorf("mod = %v, want ModAlt", kb.mod)
	}
}

func TestKeybinding_Matches_AltR(t *testing.T) {
	kb, err := parseKey("alt+r")
	if err != nil {
		t.Fatalf("parseKey: %v", err)
	}
	cases := []struct {
		name string
		ev   *tcell.EventKey
		want bool
	}{
		{"alt+r matches", runeEv('r', tcell.ModAlt), true},
		{"alt+R (caps) matches via case-fold", runeEv('R', tcell.ModAlt), true},
		{"plain r does NOT match", runeEv('r', 0), false},
		{"ctrl+r does NOT match (different key encoding)", keyEv(tcell.KeyCtrlR, 0), false},
		{"alt+s does NOT match", runeEv('s', tcell.ModAlt), false},
		{"alt+shift+r does NOT match (different mod set)", runeEv('R', tcell.ModAlt|tcell.ModShift), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := kb.Matches(c.ev); got != c.want {
				t.Errorf("Matches = %v, want %v", got, c.want)
			}
		})
	}
}

func TestKeybinding_Matches_CtrlLetterUnaffected(t *testing.T) {
	// Ensure the new alt+letter path didn't break the existing
	// ctrl+letter encoding (KeyCtrlA…KeyCtrlZ).
	kb, err := parseKey("ctrl+c")
	if err != nil {
		t.Fatalf("parseKey: %v", err)
	}
	if !kb.Matches(tcell.NewEventKey(tcell.KeyCtrlC, 0, 0)) {
		t.Error("ctrl+c should match KeyCtrlC")
	}
	if kb.Matches(runeEv('c', tcell.ModAlt)) {
		t.Error("ctrl+c should NOT match alt+c")
	}
}

func TestKeyToBytes(t *testing.T) {
	tests := []struct {
		name       string
		ev         *tcell.EventKey
		kittyFlags int
		want       string // expected bytes as a string (allows \x1b etc.)
		wantNil    bool
	}{
		// -----------------------------------------------------------------
		// 1. Basic rune keys, no modifiers, legacy mode
		// -----------------------------------------------------------------
		{
			name: "plain 'a'",
			ev:   runeEv('a', 0),
			want: "a",
		},
		{
			name: "plain 'Z'",
			ev:   runeEv('Z', 0),
			want: "Z",
		},
		{
			name: "plain '1'",
			ev:   runeEv('1', 0),
			want: "1",
		},
		{
			name: "plain space",
			ev:   runeEv(' ', 0),
			want: " ",
		},
		{
			name: "unicode rune U+00E9 (e-acute)",
			ev:   runeEv('\u00e9', 0),
			want: "\u00e9",
		},
		{
			name: "unicode rune U+4E16 (CJK)",
			ev:   runeEv('\u4e16', 0),
			want: "\u4e16",
		},

		// -----------------------------------------------------------------
		// 2. Alt+rune in legacy mode → ESC prefix
		// -----------------------------------------------------------------
		{
			name: "Alt+a legacy",
			ev:   runeEv('a', tcell.ModAlt),
			want: "\x1ba",
		},
		{
			name: "Alt+Z legacy",
			ev:   runeEv('Z', tcell.ModAlt),
			want: "\x1bZ",
		},
		{
			name: "Alt+1 legacy",
			ev:   runeEv('1', tcell.ModAlt),
			want: "\x1b1",
		},

		// -----------------------------------------------------------------
		// 3. Ctrl+letter in legacy mode → raw bytes 1-26
		// -----------------------------------------------------------------
		{
			name: "Ctrl+A legacy",
			ev:   keyEv(tcell.KeyCtrlA, tcell.ModCtrl),
			want: "\x01",
		},
		{
			name: "Ctrl+C legacy",
			ev:   keyEv(tcell.KeyCtrlC, tcell.ModCtrl),
			want: "\x03",
		},
		{
			name: "Ctrl+D legacy",
			ev:   keyEv(tcell.KeyCtrlD, tcell.ModCtrl),
			want: "\x04",
		},
		{
			name: "Ctrl+L legacy",
			ev:   keyEv(tcell.KeyCtrlL, tcell.ModCtrl),
			want: "\x0c",
		},
		{
			name: "Ctrl+Z legacy",
			ev:   keyEv(tcell.KeyCtrlZ, tcell.ModCtrl),
			want: "\x1a",
		},

		// -----------------------------------------------------------------
		// 4. Special keys in legacy mode
		// -----------------------------------------------------------------
		// Arrows
		{
			name: "Up legacy",
			ev:   keyEv(tcell.KeyUp, 0),
			want: "\x1b[A",
		},
		{
			name: "Down legacy",
			ev:   keyEv(tcell.KeyDown, 0),
			want: "\x1b[B",
		},
		{
			name: "Right legacy",
			ev:   keyEv(tcell.KeyRight, 0),
			want: "\x1b[C",
		},
		{
			name: "Left legacy",
			ev:   keyEv(tcell.KeyLeft, 0),
			want: "\x1b[D",
		},
		// Editing keys
		{
			name: "Home legacy",
			ev:   keyEv(tcell.KeyHome, 0),
			want: "\x1b[H",
		},
		{
			name: "End legacy",
			ev:   keyEv(tcell.KeyEnd, 0),
			want: "\x1b[F",
		},
		{
			name: "PgUp legacy",
			ev:   keyEv(tcell.KeyPgUp, 0),
			want: "\x1b[5~",
		},
		{
			name: "PgDn legacy",
			ev:   keyEv(tcell.KeyPgDn, 0),
			want: "\x1b[6~",
		},
		{
			name: "Insert legacy",
			ev:   keyEv(tcell.KeyInsert, 0),
			want: "\x1b[2~",
		},
		{
			name: "Delete legacy",
			ev:   keyEv(tcell.KeyDelete, 0),
			want: "\x1b[3~",
		},
		// Whitespace / control
		{
			name: "Enter legacy",
			ev:   keyEv(tcell.KeyEnter, 0),
			want: "\r",
		},
		{
			name: "Tab legacy",
			ev:   keyEv(tcell.KeyTab, 0),
			want: "\t",
		},
		{
			name: "BackTab legacy",
			ev:   keyEv(tcell.KeyBacktab, 0),
			want: "\x1b[Z",
		},
		{
			name: "Backspace (KeyBackspace) legacy",
			ev:   keyEv(tcell.KeyBackspace, 0),
			want: "\x08",
		},
		// NOTE: tcell.NewEventKey normalizes KeyBackspace2 (127) to
		// KeyBackspace (8), so constructing a KeyBackspace2 event via
		// NewEventKey is not possible.  The KeyBackspace2 branch in
		// keyToBytes is reachable in practice when the terminal driver
		// synthesizes the event directly.  We test the reachable path
		// (KeyBackspace → 0x08) above and the Kitty path below.
		{
			name: "Esc legacy",
			ev:   keyEv(tcell.KeyEsc, 0),
			want: "\x1b",
		},
		// F-keys
		{
			name: "F2 legacy",
			ev:   keyEv(tcell.KeyF2, 0),
			want: "\x1bOQ",
		},
		{
			name: "F3 legacy",
			ev:   keyEv(tcell.KeyF3, 0),
			want: "\x1bOR",
		},
		{
			name: "F4 legacy",
			ev:   keyEv(tcell.KeyF4, 0),
			want: "\x1bOS",
		},
		{
			name: "F5 legacy",
			ev:   keyEv(tcell.KeyF5, 0),
			want: "\x1b[15~",
		},
		{
			name: "F6 legacy",
			ev:   keyEv(tcell.KeyF6, 0),
			want: "\x1b[17~",
		},
		{
			name: "F7 legacy",
			ev:   keyEv(tcell.KeyF7, 0),
			want: "\x1b[18~",
		},
		{
			name: "F8 legacy",
			ev:   keyEv(tcell.KeyF8, 0),
			want: "\x1b[19~",
		},
		{
			name: "F9 legacy",
			ev:   keyEv(tcell.KeyF9, 0),
			want: "\x1b[20~",
		},
		{
			name: "F10 legacy",
			ev:   keyEv(tcell.KeyF10, 0),
			want: "\x1b[21~",
		},
		{
			name: "F11 legacy",
			ev:   keyEv(tcell.KeyF11, 0),
			want: "\x1b[23~",
		},
		{
			name: "F12 legacy",
			ev:   keyEv(tcell.KeyF12, 0),
			want: "\x1b[24~",
		},

		// -----------------------------------------------------------------
		// 5. Kitty CSI u encoding for runes with modifiers
		//
		// Bug: Alt+1, Shift+Tab, modified keys not working in neovim/helix
		//
		// Apps that request Kitty keyboard protocol (via \x1b[>1u) need
		// CSI u sequences for modified keys instead of legacy escapes.
		// -----------------------------------------------------------------
		// NOTE: tcell.NewEventKey strips ModShift from rune events (the
		// shift is implicit in the uppercase rune), so we cannot construct
		// a Shift-only rune event via NewEventKey.  The Shift+Enter test
		// (section 6) and the modifier bitmask tests (section 9) cover the
		// shift bit in kittyMod.  Here we test Shift+Alt which tcell does
		// preserve for runes.
		{
			name:       "Kitty Shift+Alt+b",
			ev:         runeEv('b', tcell.ModShift|tcell.ModAlt),
			kittyFlags: 1,
			want:       "\x1b[98;4u", // 'b'=98, shift+alt → kittyMod=1+1+2=4
		},
		{
			name:       "Kitty Alt+a",
			ev:         runeEv('a', tcell.ModAlt),
			kittyFlags: 1,
			want:       "\x1b[97;3u", // 'a'=97, alt → kittyMod=3
		},
		{
			name:       "Kitty Ctrl+Shift+x",
			ev:         runeEv('X', tcell.ModCtrl|tcell.ModShift),
			kittyFlags: 1,
			want:       "\x1b[88;6u", // 'X'=88, ctrl+shift → 1+4+1=6
		},
		{
			name:       "Kitty rune no mods → plain UTF-8",
			ev:         runeEv('z', 0),
			kittyFlags: 1,
			want:       "z",
		},

		// -----------------------------------------------------------------
		// 6. Kitty CSI u for special keys (Enter, Tab, Backspace, Esc)
		// -----------------------------------------------------------------
		{
			name:       "Kitty Enter no mods keeps legacy bytes",
			ev:         keyEv(tcell.KeyEnter, 0),
			kittyFlags: 1,
			want:       "\r",
		},
		{
			name:       "Kitty Tab no mods keeps legacy bytes",
			ev:         keyEv(tcell.KeyTab, 0),
			kittyFlags: 1,
			want:       "\t",
		},
		{
			name:       "Kitty Backspace no mods keeps legacy bytes",
			ev:         keyEv(tcell.KeyBackspace2, 0),
			kittyFlags: 1,
			want:       "\x7f",
		},
		{
			name:       "Kitty report-all Enter",
			ev:         keyEv(tcell.KeyEnter, 0),
			kittyFlags: 1 | 8,
			want:       "\x1b[13u",
		},
		{
			name:       "Kitty report-all Tab",
			ev:         keyEv(tcell.KeyTab, 0),
			kittyFlags: 1 | 8,
			want:       "\x1b[9u",
		},
		{
			name:       "Kitty report-all Backspace",
			ev:         keyEv(tcell.KeyBackspace, 0),
			kittyFlags: 1 | 8,
			want:       "\x1b[127u",
		},
		{
			name:       "Kitty Esc no mods",
			ev:         keyEv(tcell.KeyEsc, 0),
			kittyFlags: 1,
			want:       "\x1b[27u",
		},
		{
			name:       "Kitty Enter+Shift",
			ev:         keyEv(tcell.KeyEnter, tcell.ModShift),
			kittyFlags: 1,
			want:       "\x1b[13;2u",
		},
		{
			name:       "Kitty Backspace+Ctrl",
			ev:         keyEv(tcell.KeyBackspace, tcell.ModCtrl),
			kittyFlags: 1,
			want:       "\x1b[127;5u",
		},
		{
			name:       "Kitty Esc+Alt",
			ev:         keyEv(tcell.KeyEsc, tcell.ModAlt),
			kittyFlags: 1,
			want:       "\x1b[27;3u",
		},

		// -----------------------------------------------------------------
		// 7. Kitty BackTab: shift bit forced even though tcell strips it
		//
		// Regression: the original code used `km |= 2` which sets the ALT
		// bit (kitty modifier bit-1 = +2) instead of the SHIFT bit (bit-0
		// = +1), causing copilot-cli to receive Alt+Tab instead of Shift+Tab.
		// The correct fix is `km += 1`.
		//
		// Kitty modifier encoding: param = 1 + shift(1) + alt(2) + ctrl(4)
		//   Shift only      → 2   (\x1b[9;2u)
		//   Shift+Alt       → 4   (\x1b[9;4u)
		//   Wrong (old bug) → 3   (\x1b[9;3u, decoded as Alt-only)
		// -----------------------------------------------------------------
		{
			name:       "Kitty BackTab (Shift+Tab)",
			ev:         keyEv(tcell.KeyBacktab, 0),
			kittyFlags: 1,
			// km starts at 1 (no mods; tcell strips Shift from BackTab).
			// BackTab adds shift (+1) → km=2. cp=9. → \x1b[9;2u
			want: "\x1b[9;2u",
		},
		{
			name: "Kitty BackTab with Alt (Shift+Alt+Tab)",
			// tcell strips Shift but preserves Alt; mod=Alt.
			ev:         keyEv(tcell.KeyBacktab, tcell.ModAlt),
			kittyFlags: 1,
			// kittyMod = 1 + 2(alt) = 3, then +1(shift) = 4 → \x1b[9;4u
			want: "\x1b[9;4u",
		},

		// -----------------------------------------------------------------
		// 8. Kitty CSI u for Ctrl+letter
		// -----------------------------------------------------------------
		{
			name:       "Kitty Ctrl+A",
			ev:         keyEv(tcell.KeyCtrlA, tcell.ModCtrl),
			kittyFlags: 1,
			// cp = 'a' = 97, km = 1 | 5 = 5 (ctrl bit forced)
			want: "\x1b[97;5u",
		},
		{
			name:       "Kitty Ctrl+C",
			ev:         keyEv(tcell.KeyCtrlC, tcell.ModCtrl),
			kittyFlags: 1,
			want:       "\x1b[99;5u",
		},
		{
			name:       "Kitty Ctrl+Z",
			ev:         keyEv(tcell.KeyCtrlZ, tcell.ModCtrl),
			kittyFlags: 1,
			// cp = 'z' = 122, km = 1 | 5 = 5
			want: "\x1b[122;5u",
		},
		{
			name:       "Kitty Ctrl+L",
			ev:         keyEv(tcell.KeyCtrlL, tcell.ModCtrl),
			kittyFlags: 1,
			// cp = 'l' = 108
			want: "\x1b[108;5u",
		},

		// -----------------------------------------------------------------
		// 9. Kitty modifier encoding combinations
		// -----------------------------------------------------------------
		{
			name:       "Kitty Shift+Alt+a → kittyMod=4",
			ev:         runeEv('a', tcell.ModShift|tcell.ModAlt),
			kittyFlags: 1,
			// kittyMod = 1 + 1(shift) + 2(alt) = 4
			want: "\x1b[97;4u",
		},
		{
			name:       "Kitty Ctrl+Alt+a → kittyMod=7",
			ev:         runeEv('a', tcell.ModCtrl|tcell.ModAlt),
			kittyFlags: 1,
			// kittyMod = 1 + 2(alt) + 4(ctrl) = 7
			want: "\x1b[97;7u",
		},
		{
			name:       "Kitty Shift+Ctrl+Alt+a → kittyMod=8",
			ev:         runeEv('a', tcell.ModShift|tcell.ModCtrl|tcell.ModAlt),
			kittyFlags: 1,
			// kittyMod = 1 + 1 + 2 + 4 = 8
			want: "\x1b[97;8u",
		},
		{
			name:       "Kitty Enter+Ctrl+Alt → kittyMod=7",
			ev:         keyEv(tcell.KeyEnter, tcell.ModCtrl|tcell.ModAlt),
			kittyFlags: 1,
			want:       "\x1b[13;7u",
		},

		// -----------------------------------------------------------------
		// 10. Modified arrows — xterm \x1b[1;<mod>X form
		// (Alt+arrows no longer return nil; handleKey consumes them first)
		// -----------------------------------------------------------------
		{
			name: "Alt+Up → xterm modifier seq",
			ev:   keyEv(tcell.KeyUp, tcell.ModAlt),
			want: "\x1b[1;3A", // mod=1+alt(2)=3
		},
		{
			name: "Alt+Down → xterm modifier seq",
			ev:   keyEv(tcell.KeyDown, tcell.ModAlt),
			want: "\x1b[1;3B",
		},
		{
			name: "Alt+Left → xterm modifier seq",
			ev:   keyEv(tcell.KeyLeft, tcell.ModAlt),
			want: "\x1b[1;3D",
		},
		{
			name: "Alt+Right → xterm modifier seq",
			ev:   keyEv(tcell.KeyRight, tcell.ModAlt),
			want: "\x1b[1;3C",
		},
		{
			name: "Ctrl+Up",
			ev:   keyEv(tcell.KeyUp, tcell.ModCtrl),
			want: "\x1b[1;5A", // mod=1+ctrl(4)=5
		},
		{
			name: "Ctrl+Down",
			ev:   keyEv(tcell.KeyDown, tcell.ModCtrl),
			want: "\x1b[1;5B",
		},
		{
			name: "Ctrl+Left",
			ev:   keyEv(tcell.KeyLeft, tcell.ModCtrl),
			want: "\x1b[1;5D",
		},
		{
			name: "Ctrl+Right",
			ev:   keyEv(tcell.KeyRight, tcell.ModCtrl),
			want: "\x1b[1;5C",
		},
		{
			name: "Shift+Up",
			ev:   keyEv(tcell.KeyUp, tcell.ModShift),
			want: "\x1b[1;2A", // mod=1+shift(1)=2
		},

		// -----------------------------------------------------------------
		// 11. F1 + modified F-keys
		// -----------------------------------------------------------------
		{
			name: "F1 legacy",
			ev:   keyEv(tcell.KeyF1, 0),
			want: "\x1bOP",
		},
		{
			name: "Shift+F1",
			ev:   keyEv(tcell.KeyF1, tcell.ModShift),
			want: "\x1b[1;2P",
		},
		{
			name: "Ctrl+F2",
			ev:   keyEv(tcell.KeyF2, tcell.ModCtrl),
			want: "\x1b[1;5Q",
		},
		{
			name: "Alt+F5",
			ev:   keyEv(tcell.KeyF5, tcell.ModAlt),
			want: "\x1b[15;3~",
		},
		{
			name: "Ctrl+F10",
			ev:   keyEv(tcell.KeyF10, tcell.ModCtrl),
			want: "\x1b[21;5~",
		},

		// -----------------------------------------------------------------
		// 12. F13–F24 (Shift+F1–F12 via dedicated tcell key codes)
		// -----------------------------------------------------------------
		{
			name: "F13 (Shift+F1)",
			ev:   keyEv(tcell.KeyF13, 0),
			want: "\x1b[1;2P",
		},
		{
			name: "F14 (Shift+F2)",
			ev:   keyEv(tcell.KeyF14, 0),
			want: "\x1b[1;2Q",
		},
		{
			name: "F17 (Shift+F5)",
			ev:   keyEv(tcell.KeyF17, 0),
			want: "\x1b[15;2~",
		},
		{
			name: "F23 (Shift+F11)",
			ev:   keyEv(tcell.KeyF23, 0),
			want: "\x1b[23;2~",
		},
		{
			name: "F24 (Shift+F12)",
			ev:   keyEv(tcell.KeyF24, 0),
			want: "\x1b[24;2~",
		},

		// -----------------------------------------------------------------
		// 13. Modified Home/End/PgUp/PgDn/Insert/Delete
		// -----------------------------------------------------------------
		{
			name: "Ctrl+Home",
			ev:   keyEv(tcell.KeyHome, tcell.ModCtrl),
			want: "\x1b[1;5H",
		},
		{
			name: "Shift+End",
			ev:   keyEv(tcell.KeyEnd, tcell.ModShift),
			want: "\x1b[1;2F",
		},
		{
			name: "Ctrl+Delete",
			ev:   keyEv(tcell.KeyDelete, tcell.ModCtrl),
			want: "\x1b[3;5~",
		},
		{
			name: "Shift+Insert",
			ev:   keyEv(tcell.KeyInsert, tcell.ModShift),
			want: "\x1b[2;2~",
		},

		// -----------------------------------------------------------------
		// Edge cases / nil return for genuinely unhandled keys
		// -----------------------------------------------------------------
		{
			name:    "Unhandled key returns nil",
			ev:      keyEv(tcell.KeyF25, 0),
			wantNil: true,
		},

		// -----------------------------------------------------------------
		// Kitty: arrows/F-keys fall through to legacy (not CSI u)
		// -----------------------------------------------------------------
		{
			name:       "Kitty Up → legacy escape (no CSI u)",
			ev:         keyEv(tcell.KeyUp, 0),
			kittyFlags: 1,
			want:       "\x1b[A",
		},
		{
			name:       "Kitty F5 → legacy escape (no CSI u)",
			ev:         keyEv(tcell.KeyF5, 0),
			kittyFlags: 1,
			want:       "\x1b[15~",
		},
		{
			name:       "Kitty Home → legacy escape (no CSI u)",
			ev:         keyEv(tcell.KeyHome, 0),
			kittyFlags: 1,
			want:       "\x1b[H",
		},

		// -----------------------------------------------------------------
		// kittyFlags with bit 0 not set → legacy even if other bits are set
		// -----------------------------------------------------------------
		{
			name:       "kittyFlags=2 (bit 0 unset) → legacy Alt+a",
			ev:         runeEv('a', tcell.ModAlt),
			kittyFlags: 2,
			want:       "\x1ba",
		},
		{
			name:       "kittyFlags=0 Ctrl+A → legacy raw byte",
			ev:         keyEv(tcell.KeyCtrlA, tcell.ModCtrl),
			kittyFlags: 0,
			want:       "\x01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := keyToBytes(tt.ev, tt.kittyFlags)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %q", got)
				}
				return
			}
			if got == nil {
				t.Fatal("got nil, want non-nil")
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestKeyToBytesCtrlLettersLegacy walks through every Ctrl+A..Z in legacy mode
// and checks each produces the expected raw byte (0x01..0x1A).
func TestKeyToBytesCtrlLettersLegacy(t *testing.T) {
	for i := 0; i < 26; i++ {
		k := tcell.KeyCtrlA + tcell.Key(i)
		letter := rune('A' + i)
		t.Run(fmt.Sprintf("Ctrl+%c", letter), func(t *testing.T) {
			ev := keyEv(k, tcell.ModCtrl)
			got := keyToBytes(ev, 0)
			want := []byte{byte(i + 1)}
			if len(got) != 1 || got[0] != want[0] {
				t.Errorf("Ctrl+%c: got %q, want %q", letter, got, want)
			}
		})
	}
}

// TestKeyToBytesCtrlLettersKitty walks through every Ctrl+A..Z in Kitty
// disambiguate mode and verifies CSI u output with the correct codepoint.
func TestKeyToBytesCtrlLettersKitty(t *testing.T) {
	for i := 0; i < 26; i++ {
		k := tcell.KeyCtrlA + tcell.Key(i)
		letter := rune('a' + i)
		t.Run(fmt.Sprintf("Kitty Ctrl+%c", letter), func(t *testing.T) {
			ev := keyEv(k, tcell.ModCtrl)
			got := keyToBytes(ev, 1)
			want := fmt.Sprintf("\x1b[%d;5u", letter)
			if string(got) != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

// TestKeyToBytesKittyModifierBitmask verifies the kittyMod encoding formula
// (1 + bitmask where shift=1, alt=2, ctrl=4) for all 7 non-trivial combos.
//
// NOTE: tcell.NewEventKey strips ModShift from rune events (the shift is
// already reflected in the rune value).  For combos that include only
// ModShift we use a special key (Enter, cp=13) where tcell preserves the
// modifier.  For combos that include Alt or Ctrl (which tcell preserves on
// runes) we use rune 'x' (cp=120).
func TestKeyToBytesKittyModifierBitmask(t *testing.T) {
	tests := []struct {
		name string
		ev   *tcell.EventKey
		// Expected CSI u sequence
		want string
	}{
		// Shift alone: use Enter since tcell preserves ModShift on non-rune keys.
		{"Shift (Enter)", keyEv(tcell.KeyEnter, tcell.ModShift), "\x1b[13;2u"},
		// Alt alone: rune 'x' (120) with Alt.
		{"Alt", runeEv('x', tcell.ModAlt), "\x1b[120;3u"},
		// Ctrl alone on rune: tcell normalizes to KeyCtrlX, so it goes
		// through the Ctrl+letter path → cp='x'=120, km|=5.
		{"Ctrl", keyEv(tcell.KeyCtrlX, tcell.ModCtrl), "\x1b[120;5u"},
		// Multi-modifier combos that include Shift stay as KeyRune in tcell.
		{"Shift+Alt", runeEv('x', tcell.ModShift|tcell.ModAlt), "\x1b[120;4u"},
		{"Shift+Ctrl", runeEv('x', tcell.ModShift|tcell.ModCtrl), "\x1b[120;6u"},
		{"Alt+Ctrl", runeEv('x', tcell.ModAlt|tcell.ModCtrl), "\x1b[120;7u"},
		{"Shift+Alt+Ctrl", runeEv('x', tcell.ModShift|tcell.ModAlt|tcell.ModCtrl), "\x1b[120;8u"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := keyToBytes(tt.ev, 1)
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
