module bunker

go 1.26.0

// Pinned tcell with pane-safe overline and keypad input extensions.
replace github.com/gdamore/tcell/v2 => ./third_party/tcell

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/creack/pty v1.1.24
	github.com/diamondburned/gotk4/pkg v0.4.1
	github.com/gdamore/tcell/v2 v2.13.10
	github.com/rivo/uniseg v0.4.7
	github.com/spf13/cobra v1.10.2
	golang.org/x/sys v0.48.0
	golang.org/x/term v0.46.0
)

require (
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

replace github.com/diamondburned/gotk4/pkg => ./third_party/gotk4
