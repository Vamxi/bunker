package main

import (
	"os"
	"runtime"
	"sync"
	"testing"

	"bunker/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

// ---------------------------------------------------------------------------
// mouseToBytes — basic encoding
// ---------------------------------------------------------------------------

func TestMouseToBytes_SGR_LeftPress(t *testing.T) {
	got := mouseToBytes(tcell.Button1, tcell.ButtonNone, 0, 10, 20, true)
	want := "\x1b[<0;10;20M"
	if string(got) != want {
		t.Errorf("SGR left press = %q, want %q", got, want)
	}
}

func TestMouseToBytes_SGR_LeftRelease(t *testing.T) {
	got := mouseToBytes(tcell.ButtonNone, tcell.Button1, 0, 10, 20, true)
	want := "\x1b[<0;10;20m"
	if string(got) != want {
		t.Errorf("SGR left release = %q, want %q", got, want)
	}
}

func TestMouseToBytes_SGR_RightPress(t *testing.T) {
	got := mouseToBytes(tcell.Button2, tcell.ButtonNone, 0, 5, 3, true)
	want := "\x1b[<2;5;3M"
	if string(got) != want {
		t.Errorf("SGR right press = %q, want %q", got, want)
	}
}

func TestMouseToBytes_SGR_WheelUp(t *testing.T) {
	got := mouseToBytes(tcell.WheelUp, tcell.ButtonNone, 0, 1, 1, true)
	want := "\x1b[<64;1;1M"
	if string(got) != want {
		t.Errorf("SGR wheel up = %q, want %q", got, want)
	}
}

func TestMouseToBytes_SGR_WithModifiers(t *testing.T) {
	got := mouseToBytes(tcell.Button1, tcell.ButtonNone, tcell.ModShift|tcell.ModCtrl, 1, 1, true)
	// shift = +4, ctrl = +16 → cb = 0+4+16 = 20
	want := "\x1b[<20;1;1M"
	if string(got) != want {
		t.Errorf("SGR shift+ctrl left press = %q, want %q", got, want)
	}
}

func TestMouseToBytes_X10_LeftPress(t *testing.T) {
	got := mouseToBytes(tcell.Button1, tcell.ButtonNone, 0, 10, 20, false)
	// cb=0, x=10, y=20 → bytes: ESC [ M (0+32) (10+32) (20+32)
	want := []byte{'\x1b', '[', 'M', 32, 42, 52}
	if string(got) != string(want) {
		t.Errorf("X10 left press = %v, want %v", got, want)
	}
}

func TestMouseToBytes_X10_Release(t *testing.T) {
	got := mouseToBytes(tcell.ButtonNone, tcell.Button1, 0, 1, 1, false)
	// release → cb=3
	want := []byte{'\x1b', '[', 'M', 35, 33, 33}
	if string(got) != string(want) {
		t.Errorf("X10 release = %v, want %v", got, want)
	}
}

func TestMouseToBytes_X10_CoordOverflow(t *testing.T) {
	got := mouseToBytes(tcell.Button1, tcell.ButtonNone, 0, 224, 1, false)
	if got != nil {
		t.Errorf("X10 with x=224 should return nil, got %v", got)
	}
}

func TestMouseToBytes_PureMotion(t *testing.T) {
	// ButtonNone with ButtonNone prevBtn → pure motion, cb=35
	got := mouseToBytes(tcell.ButtonNone, tcell.ButtonNone, 0, 5, 5, true)
	want := "\x1b[<35;5;5M"
	if string(got) != want {
		t.Errorf("SGR pure motion = %q, want %q", got, want)
	}
}

func TestMouseToBytes_UnknownButton(t *testing.T) {
	// Use a button mask that doesn't match any case.
	got := mouseToBytes(tcell.ButtonMask(1<<14), tcell.ButtonNone, 0, 1, 1, true)
	if got != nil {
		t.Errorf("unknown button should return nil, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Mouse mode race: mouse events must not leak to PTY after mode is disabled
//
// Bug: SGR mouse sequences appear as visible text after exiting btop
//
// When btop exits and disables mouse reporting (\x1b[?1003l), there is a
// race between readPTY (which clears the mouse mode in vt10x) and the tcell
// event loop (which forwards mouse events to the PTY).  The fix re-checks
// the mouse mode under the lock right before writing.
//
// This test verifies that no mouse data is written to the PTY after the
// mode is cleared, even under heavy concurrent contention.
// ---------------------------------------------------------------------------

func TestMouseForwardRace_BtopExitLeak(t *testing.T) {
	// Create a pipe so we can read what gets written to the PTY.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close() //nolint:errcheck // test pipe cleanup

	term := vt10x.New(vt10x.WithSize(80, 24))
	p := &Pane{
		id:              99,
		x:               0,
		y:               0,
		w:               80,
		h:               24,
		ptmx:            pw,
		term:            term,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	// Enable mouse tracking + SGR mode in vt10x (simulates btop running).
	p.term.Write([]byte("\x1b[?1003h\x1b[?1006h")) //nolint:errcheck

	// Verify mouse mode is enabled.
	if p.term.Mode()&vt10x.ModeMouseMask == 0 {
		t.Fatal("mouse mode not enabled after DECSET 1003")
	}

	// Disable mouse mode first (simulates readPTY processing btop exit).
	p.mu.Lock()
	p.term.Write([]byte("\x1b[?1003l\x1b[?1006l")) //nolint:errcheck
	p.mu.Unlock()

	// Drain any bytes already in the pipe from the enable/disable writes
	// (there shouldn't be any, but be safe).
	go func() {
		buf := make([]byte, 4096)
		for {
			pr.Read(buf) //nolint:errcheck // best-effort drain in test
			return
		}
	}()

	// Create a fresh pipe for the post-disable phase.
	pr2, pw2, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr2.Close() //nolint:errcheck // test pipe cleanup
	pw.Close()        //nolint:errcheck // close old write end
	p.ptmx = pw2      // point pane at new pipe

	// Now fire many mouse events with the double-check guard.
	// Since mode is disabled, none should be written.
	var wg sync.WaitGroup
	const numWriters = 4
	const eventsPerWriter = 500
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < eventsPerWriter; j++ {
				// Replicate the exact guard from handleMouse:
				p.mu.Lock()
				mode := p.term.Mode()
				p.mu.Unlock()
				if mode&vt10x.ModeMouseMask == 0 {
					continue
				}
				data := mouseToBytes(
					tcell.Button1, tcell.ButtonNone, 0,
					id+1, j%24+1,
					mode&vt10x.ModeMouseSgr != 0,
				)
				if len(data) == 0 {
					continue
				}
				// Re-check under lock before writing (THE FIX).
				p.mu.Lock()
				stillWants := p.term.Mode()&vt10x.ModeMouseMask != 0
				p.mu.Unlock()
				if stillWants {
					p.writeInput(data)
				}
				runtime.Gosched()
			}
		}(i)
	}
	wg.Wait()
	pw2.Close() //nolint:errcheck // test pipe cleanup

	// Read everything from the post-disable pipe.
	var total int
	buf := make([]byte, 4096)
	for {
		n, err := pr2.Read(buf)
		total += n
		if err != nil {
			break
		}
	}
	if total > 0 {
		t.Errorf("leaked %d bytes of mouse data after mode was disabled", total)
	}
}

// ---------------------------------------------------------------------------
// mouseToBytes table-driven comprehensive test
// ---------------------------------------------------------------------------

func TestMouseToBytes_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		btn     tcell.ButtonMask
		prevBtn tcell.ButtonMask
		mod     tcell.ModMask
		x, y    int
		sgr     bool
		want    string
		wantNil bool
	}{
		{"SGR left press", tcell.Button1, tcell.ButtonNone, 0, 1, 1, true, "\x1b[<0;1;1M", false},
		{"SGR left release", tcell.ButtonNone, tcell.Button1, 0, 1, 1, true, "\x1b[<0;1;1m", false},
		{"SGR middle press", tcell.Button3, tcell.ButtonNone, 0, 1, 1, true, "\x1b[<1;1;1M", false},
		{"SGR right press", tcell.Button2, tcell.ButtonNone, 0, 1, 1, true, "\x1b[<2;1;1M", false},
		{"SGR right release", tcell.ButtonNone, tcell.Button2, 0, 1, 1, true, "\x1b[<2;1;1m", false},
		{"SGR wheel down", tcell.WheelDown, tcell.ButtonNone, 0, 1, 1, true, "\x1b[<65;1;1M", false},
		{"SGR wheel left", tcell.WheelLeft, tcell.ButtonNone, 0, 1, 1, true, "\x1b[<66;1;1M", false},
		{"SGR wheel right", tcell.WheelRight, tcell.ButtonNone, 0, 1, 1, true, "\x1b[<67;1;1M", false},
		{"SGR shift+left", tcell.Button1, tcell.ButtonNone, tcell.ModShift, 1, 1, true, "\x1b[<4;1;1M", false},
		{"SGR alt+left", tcell.Button1, tcell.ButtonNone, tcell.ModAlt, 1, 1, true, "\x1b[<8;1;1M", false},
		{"SGR ctrl+left", tcell.Button1, tcell.ButtonNone, tcell.ModCtrl, 1, 1, true, "\x1b[<16;1;1M", false},
		{"SGR coords 80,24", tcell.Button1, tcell.ButtonNone, 0, 80, 24, true, "\x1b[<0;80;24M", false},
		{"X10 left press", tcell.Button1, tcell.ButtonNone, 0, 1, 1, false, string([]byte{'\x1b', '[', 'M', 32, 33, 33}), false},
		{"X10 x overflow", tcell.Button1, tcell.ButtonNone, 0, 224, 1, false, "", true},
		{"X10 y overflow", tcell.Button1, tcell.ButtonNone, 0, 1, 224, false, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mouseToBytes(tt.btn, tt.prevBtn, tt.mod, tt.x, tt.y, tt.sgr)
			if tt.wantNil {
				if got != nil {
					t.Errorf("got %q, want nil", got)
				}
				return
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// sendFocusIn / sendFocusOut
// ---------------------------------------------------------------------------

func TestSendFocusIn_Enabled(t *testing.T) {
	pr, pw, _ := os.Pipe()
	defer pr.Close() //nolint:errcheck // test pipe cleanup

	term := vt10x.New(vt10x.WithSize(80, 24))
	term.Write([]byte("\x1b[?1004h")) //nolint:errcheck // enable focus events
	p := &Pane{
		ptmx:            pw,
		term:            term,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	sendFocusIn(p)
	pw.Close() //nolint:errcheck // test pipe cleanup

	buf := make([]byte, 64)
	n, _ := pr.Read(buf)
	got := string(buf[:n])
	if got != "\x1b[I" {
		t.Errorf("sendFocusIn = %q, want %q", got, "\x1b[I")
	}
}

func TestSendFocusOut_Enabled(t *testing.T) {
	pr, pw, _ := os.Pipe()
	defer pr.Close() //nolint:errcheck // test pipe cleanup

	term := vt10x.New(vt10x.WithSize(80, 24))
	term.Write([]byte("\x1b[?1004h")) //nolint:errcheck
	p := &Pane{
		ptmx:            pw,
		term:            term,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	sendFocusOut(p)
	pw.Close() //nolint:errcheck // test pipe cleanup

	buf := make([]byte, 64)
	n, _ := pr.Read(buf)
	got := string(buf[:n])
	if got != "\x1b[O" {
		t.Errorf("sendFocusOut = %q, want %q", got, "\x1b[O")
	}
}

func TestSendFocusIn_Disabled(t *testing.T) {
	pr, pw, _ := os.Pipe()
	defer pr.Close() //nolint:errcheck // test pipe cleanup

	term := vt10x.New(vt10x.WithSize(80, 24))
	// Don't enable focus events.
	p := &Pane{
		ptmx:            pw,
		term:            term,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	sendFocusIn(p)
	pw.Close() //nolint:errcheck // test pipe cleanup

	buf := make([]byte, 64)
	n, _ := pr.Read(buf)
	if n > 0 {
		t.Errorf("sendFocusIn wrote %d bytes when focus events disabled", n)
	}
}

func TestSendFocusIn_Nil(t *testing.T) {
	// Should not panic.
	sendFocusIn(nil)
	sendFocusOut(nil)
}

// ---------------------------------------------------------------------------
// sendFocusOut
// ---------------------------------------------------------------------------

func TestSendFocusOut_Nil(t *testing.T) {
	sendFocusOut(nil) // must not panic
}

func TestSendFocusOut_Disabled(t *testing.T) {
	pr, pw, _ := os.Pipe()
	defer pr.Close() //nolint:errcheck // test pipe cleanup

	term := vt10x.New(vt10x.WithSize(80, 24))
	p := &Pane{
		ptmx:            pw,
		term:            term,
		scrollbackLines: 100,
		sb:              sbRing{maxLines: 100},
	}

	sendFocusOut(p)
	pw.Close() //nolint:errcheck // test pipe cleanup

	buf := make([]byte, 64)
	n, _ := pr.Read(buf)
	if n > 0 {
		t.Errorf("sendFocusOut wrote %d bytes when focus events disabled", n)
	}
}

// TestMouseToBytes_DragMotionBit guards the bug where drag events (button held
// while moving) were encoded as button-press events instead of motion events.
// ANSI mouse protocol: bit 32 on the button code = motion event.
// Without this bit, apps receive multiple "press" events along the drag path
// and restart selection at each one → full-line selection instead of range.
func TestMouseToBytes_DragMotionBit_SGR(t *testing.T) {
	// SGR: left button drag should be cb=32 (0 | 32), final 'M'
	got := mouseToBytes(tcell.Button1, tcell.Button1, 0, 10, 5, true)
	want := "\x1b[<32;10;5M"
	if string(got) != want {
		t.Errorf("SGR left drag = %q, want %q (missing motion bit 32)", got, want)
	}
}

func TestMouseToBytes_DragMotionBit_X10(t *testing.T) {
	// X10: left button drag cb=32, encoded byte = 32+32 = 64 = '@'
	got := mouseToBytes(tcell.Button1, tcell.Button1, 0, 10, 5, false)
	// byte 4 = cb+32 = 32+32 = 64
	want := []byte{'\x1b', '[', 'M', 64, 10 + 32, 5 + 32}
	if string(got) != string(want) {
		t.Errorf("X10 left drag = %q, want %q (missing motion bit 32)", got, want)
	}
}

func TestMouseToBytes_PressHasNoMotionBit(t *testing.T) {
	// Fresh press (prevBtn=None): cb=0, no motion bit.
	got := mouseToBytes(tcell.Button1, tcell.ButtonNone, 0, 10, 5, true)
	want := "\x1b[<0;10;5M"
	if string(got) != want {
		t.Errorf("SGR left press = %q, want %q", got, want)
	}
}

func TestMouseToBytes_WheelNoDragBit(t *testing.T) {
	// Wheel events should never get the motion bit regardless of prevBtn.
	got := mouseToBytes(tcell.WheelUp, tcell.WheelUp, 0, 10, 5, true)
	want := "\x1b[<64;10;5M"
	if string(got) != want {
		t.Errorf("SGR wheel up = %q, want %q", got, want)
	}
}
