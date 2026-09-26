package vt10x

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestAlerts(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     []Alert
	}{
		{"bell", "a\ab", []Alert{{Kind: AlertBell}}},
		{"BEL ending an OSC is not a bell", "\x1b]0;title\a", nil},
		{"OSC 9", "\x1b]9;Build done\a", []Alert{{Kind: AlertNotify, Body: "Build done"}}},
		{"OSC 9 with semicolons", "\x1b]9;a;b\x1b\\", []Alert{{Kind: AlertNotify, Body: "a;b"}}},
		{"ConEmu progress is not a notification", "\x1b]9;4;1;50\a", nil},
		{"OSC 777", "\x1b]777;notify;Claude;Waiting for input\a", []Alert{{Kind: AlertNotify, Title: "Claude", Body: "Waiting for input"}}},
		{"OSC 777 other", "\x1b]777;preexec\a", nil},
		{"OSC 99 one piece", "\x1b]99;;Hello\a", []Alert{{Kind: AlertNotify, Body: "Hello"}}},
		{"OSC 99 in chunks", "\x1b]99;i=1:d=0:p=title;Tests\a\x1b]99;i=1:p=body;All passed\a", []Alert{{Kind: AlertNotify, Title: "Tests", Body: "All passed"}}},
		{"OSC 99 base64 skipped", "\x1b]99;e=1;SGVsbG8=\a", nil},
		{"command start and done", "\x1b]133;C\a\x1b]133;D;2\a", []Alert{{Kind: AlertCommandStart}, {Kind: AlertCommandDone, ExitCode: 2, HasExitCode: true}}},
		{"done without exit code", "\x1b]133;D\a", []Alert{{Kind: AlertCommandDone}}},
		{"prompt markers are quiet", "\x1b]133;A\a\x1b]133;B\a", nil},
		{"controls stripped", "\x1b]9;ev\x01il\u0085\x07", []Alert{{Kind: AlertNotify, Body: "evil"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []Alert
			term := New(WithSize(20, 4), WithWriter(io.Discard), WithAlertCallback(func(a Alert) { got = append(got, a) }))
			if _, err := term.Write([]byte(tc.in)); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("alerts %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAlertTextIsBounded(t *testing.T) {
	var got []Alert
	term := New(WithSize(20, 4), WithWriter(io.Discard), WithAlertCallback(func(a Alert) { got = append(got, a) }))
	if _, err := term.Write([]byte("\x1b]9;" + strings.Repeat("界", 500) + "\a")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Body) > maxAlertText+len("…") || !strings.HasSuffix(got[0].Body, "…") {
		t.Fatalf("body of %d bytes, want at most %d and an ellipsis", len(got[0].Body), maxAlertText)
	}
}
