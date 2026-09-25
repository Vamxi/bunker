# Bunk's tcell patches

Based on github.com/gdamore/tcell/v2 v2.13.10 (Apache-2.0).
The root package and terminfo sources/tests are retained; unused examples,
web assets, encoding registration, and views packages are omitted.

Local extensions:

- SGR 53 overline style, emitted by the terminal renderer.
- Preserve keypad identity through SS3 and Kitty keyboard decoding.
- Correct emoji keycap display widths (matching the pane emulator).
- Retain complete long combining sequences when encoding host output.

Run `go test -race ./...` in this directory when changing these patches.
