module bunk

go 1.25.7

// Pinned tcell with pane-safe overline and keypad input extensions.
replace github.com/gdamore/tcell/v2 => ./third_party/tcell

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/creack/pty v1.1.24
	github.com/gdamore/tcell/v2 v2.13.9
	github.com/rivo/uniseg v0.4.7
	github.com/spf13/cobra v1.10.2
	golang.org/x/sys v0.38.0
	golang.org/x/term v0.37.0
)

require (
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.3.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/text v0.31.0 // indirect
)
