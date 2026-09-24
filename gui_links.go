// gui_links.go - clickable hyperlinks.
//
// In bunk's TUI, OSC 8 links are forwarded to the outer terminal, which
// makes them clickable. In bunker the window is that terminal, so it does it
// itself: explicit OSC 8 links (ls --hyperlink, Claude Code, gcc) and plain
// URLs in the text are underlined on hover with a pointer cursor, and
// Ctrl+click opens them. A plain click still selects.
//
// Only http, https, mailto, and local file links open: terminal output can
// name any scheme, and handing arbitrary ones to the desktop could start
// unexpected handlers.
package main

import (
	"context"
	"math"
	"net/url"
	"os"
	"regexp"
	"strings"

	"bunker/internal/vt10x"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// linkSpan is a link under the pointer: a run of cells in one pane row.
type linkSpan struct {
	pane     *Pane
	row      int // frame row within the pane
	c0, c1   int // columns [c0, c1) within the pane
	url      string
	explicit bool // OSC 8 (vs. a URL detected in the text)
}

// plainURL matches URLs printed as text.
var plainURL = regexp.MustCompile(`(?i)\b(?:https?://|mailto:|file://)[^\s<>"'` + "`" + `]+`)

// linkAt finds the link under widget coordinates (x, y), if any.
func (v *termView) linkAt(x, y float64) *linkSpan {
	gcol, grow := v.cellAt(x, y)
	for p, f := range v.frames {
		if !f.valid || gcol < f.x || gcol >= f.x+f.cols || grow < f.y || grow >= f.y+f.rows {
			continue
		}
		col, row := gcol-f.x, grow-f.y
		if row >= len(f.grid) || col >= len(f.grid[row]) {
			return nil
		}
		line := f.grid[row]
		if id := line[col].Link; id != 0 {
			p.mu.Lock()
			u := p.term.Link(id)
			p.mu.Unlock()
			if u == "" {
				return nil
			}
			c0, c1 := col, col+1
			for c0 > 0 && line[c0-1].Link == id {
				c0--
			}
			for c1 < len(line) && line[c1].Link == id {
				c1++
			}
			return &linkSpan{pane: p, row: row, c0: c0, c1: c1, url: u, explicit: true}
		}
		if u, c0, c1, ok := plainURLAt(line, col); ok {
			return &linkSpan{pane: p, row: row, c0: c0, c1: c1, url: u}
		}
		return nil
	}
	return nil
}

// plainURLAt finds a URL printed in line that covers column col, returning
// it and its cell span [c0, c1).
func plainURLAt(line []vt10x.Glyph, col int) (u string, c0, c1 int, ok bool) {
	var text strings.Builder
	var cellOf []int // byte offset in text → column
	for c, g := range line {
		if g.Width < 0 {
			continue
		}
		s := g.Text()
		for range len(s) {
			cellOf = append(cellOf, c)
		}
		text.WriteString(s)
	}
	cellOf = append(cellOf, len(line))
	for _, m := range plainURL.FindAllStringIndex(text.String(), -1) {
		start, end := m[0], trimURLEnd(text.String(), m[0], m[1])
		if cellOf[start] <= col && col < cellOf[end] {
			return text.String()[start:end], cellOf[start], cellOf[end], true
		}
	}
	return "", 0, 0, false
}

// trimURLEnd drops trailing punctuation that usually ends a sentence rather
// than the URL, keeping a closing bracket that has its opening one inside.
func trimURLEnd(s string, start, end int) int {
	for end > start {
		c := s[end-1]
		switch c {
		case '.', ',', ';', ':', '!', '?':
			end--
			continue
		case ')', ']', '}':
			open := map[byte]byte{')': '(', ']': '[', '}': '{'}[c]
			if strings.Count(s[start:end], string(open)) < strings.Count(s[start:end], string(c)) {
				end--
				continue
			}
		}
		break
	}
	return end
}

// safeLink reports whether u may be handed to the desktop to open.
func safeLink(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Host != ""
	case "mailto":
		return parsed.Opaque != ""
	case "file":
		host, _ := os.Hostname()
		return parsed.Path != "" && (parsed.Host == "" || parsed.Host == "localhost" || strings.EqualFold(parsed.Host, host))
	}
	return false
}

// updateHoverLink tracks the link under the pointer for underline and
// cursor feedback.
func (v *termView) updateHoverLink(x, y float64) {
	link := v.linkAt(x, y)
	if link != nil && !safeLink(link.url) {
		link = nil
	}
	same := (link == nil) == (v.hoverLink == nil) &&
		(link == nil || (*link == *v.hoverLink))
	if same {
		return
	}
	v.hoverLink = link
	if link != nil {
		v.SetCursorFromName("pointer")
		v.SetTooltipText("Ctrl+click to open " + link.url)
	} else {
		v.SetCursorFromName("text")
		v.SetTooltipText("")
	}
	v.QueueDraw()
}

// openLink opens u with the desktop's handler.
func (v *termView) openLink(u string) {
	if v.openURI != nil { // tests
		v.openURI(u)
		return
	}
	var parent *gtk.Window
	if w, ok := v.Root().Cast().(*gtk.ApplicationWindow); ok {
		parent = &w.Window
	}
	launcher := gtk.NewURILauncher(u)
	launcher.Launch(context.Background(), parent, func(res gio.AsyncResulter) {
		if err := launcher.LaunchFinish(res); err != nil {
			L.Warn("gui: open link failed", "url", u, "err", err)
		}
	})
}

// drawHoverLink underlines the hovered link in pane frame f.
func (v *termView) drawHoverLink(s *gtk.Snapshot, p *Pane, f *paneFrame) {
	l := v.hoverLink
	if l == nil || l.pane != p || l.row >= f.rows {
		return
	}
	x0, x1 := v.pad+v.colX(f.x+l.c0), v.pad+v.colX(f.x+l.c1)
	y := v.pad + float64(f.y+l.row)*v.cellH
	ul := math.Round(min(v.ulPos, v.cellH-v.ulThick)) // on whole pixels, like text underlines
	v.fillRect(s, x0, y+ul, x1-x0, v.ulThick, rgba(v.theme.palette[4], 1))
}
