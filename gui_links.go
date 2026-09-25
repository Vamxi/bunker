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
	"slices"
	"strings"

	"bunker/internal/vt10x"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// linkSpan is a link under the pointer. A URL wrapped onto following rows
// has one segment per row.
type linkSpan struct {
	pane     *Pane
	segs     []linkSeg
	url      string
	explicit bool // OSC 8 (vs. a URL detected in the text)
}

// linkSeg is the part of a link on one frame row: columns [c0, c1).
type linkSeg struct{ row, c0, c1 int }

func (l *linkSpan) equal(o *linkSpan) bool {
	if l == nil || o == nil {
		return l == o
	}
	return l.pane == o.pane && l.url == o.url && l.explicit == o.explicit && slices.Equal(l.segs, o.segs)
}

// plainURL matches URLs printed as text.
var plainURL = regexp.MustCompile(`(?i)\b(?:https?://|mailto:|file://)[^\s<>"'` + "`" + `]+`)

// maxLinkRows bounds how many rows a wrapped link is followed across.
const maxLinkRows = 16

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
		if id := f.grid[row][col].Link; id != 0 {
			p.mu.Lock()
			u := p.term.Link(id)
			p.mu.Unlock()
			if u == "" {
				return nil
			}
			return &linkSpan{pane: p, segs: explicitLinkSegs(f.grid, row, col), url: u, explicit: true}
		}
		if u, segs, ok := plainURLAt(f.grid, row, col); ok {
			return &linkSpan{pane: p, segs: segs, url: u}
		}
		return nil
	}
	return nil
}

// explicitLinkSegs returns the cells of the OSC 8 link at (row, col),
// following it onto the rows above and below when it runs across a row edge.
func explicitLinkSegs(grid [][]vt10x.Glyph, row, col int) []linkSeg {
	id := grid[row][col].Link
	run := func(r, c int) linkSeg {
		line := grid[r]
		c0, c1 := c, c+1
		for c0 > 0 && line[c0-1].Link == id {
			c0--
		}
		for c1 < len(line) && line[c1].Link == id {
			c1++
		}
		return linkSeg{r, c0, c1}
	}
	seg := run(row, col)
	segs := []linkSeg{seg}
	for r := row; seg.c0 == 0 && r > 0 && row-r < maxLinkRows; r-- {
		above := grid[r-1]
		if len(above) == 0 || above[len(above)-1].Link != id {
			break
		}
		seg = run(r-1, len(above)-1)
		segs = append([]linkSeg{seg}, segs...)
	}
	seg = segs[len(segs)-1]
	for r := row; seg.c1 == len(grid[r]) && r+1 < len(grid) && r-row < maxLinkRows; r++ {
		if len(grid[r+1]) == 0 || grid[r+1][0].Link != id {
			break
		}
		seg = run(r+1, 0)
		segs = append(segs, seg)
	}
	return segs
}

// continuesBelow reports whether row r runs on into row r+1: the terminal
// soft-wrapped it, or text fills the last column and carries on in the first,
// which is how programs that position the cursor themselves lay out a long URL.
func continuesBelow(grid [][]vt10x.Glyph, r int) bool {
	if r+1 >= len(grid) || len(grid[r]) == 0 || len(grid[r+1]) == 0 {
		return false
	}
	last := grid[r][len(grid[r])-1]
	if last.Mode&vt10x.AttrWrap != 0 {
		return true
	}
	return urlCell(last) && urlCell(grid[r+1][0])
}

// urlCell reports whether g could be part of a URL.
func urlCell(g vt10x.Glyph) bool {
	return g.Width == 1 && g.Char > ' ' && !strings.ContainsRune(`<>"'`+"`", g.Char)
}

// plainURLAt finds a URL printed in the text that covers (row, col), joining
// rows the URL wraps across, and returns it with its cells.
func plainURLAt(grid [][]vt10x.Glyph, row, col int) (u string, segs []linkSeg, ok bool) {
	top, bot := row, row
	for top > 0 && row-top < maxLinkRows && continuesBelow(grid, top-1) {
		top--
	}
	for bot-row < maxLinkRows && continuesBelow(grid, bot) {
		bot++
	}
	type cell struct{ row, col int }
	before := func(a, b cell) bool { return a.row < b.row || (a.row == b.row && a.col < b.col) }
	var text strings.Builder
	var cellOf []cell // byte offset in text → cell
	for r := top; r <= bot; r++ {
		for c, g := range grid[r] {
			s := g.Text()
			for range len(s) {
				cellOf = append(cellOf, cell{r, c})
			}
			text.WriteString(s)
		}
	}
	cellOf = append(cellOf, cell{bot, len(grid[bot])})
	at := cell{row, col}
	for _, m := range plainURL.FindAllStringIndex(text.String(), -1) {
		start, end := m[0], trimURLEnd(text.String(), m[0], m[1])
		c0, c1 := cellOf[start], cellOf[end]
		if before(at, c0) || !before(at, c1) {
			continue
		}
		for r := c0.row; r <= c1.row; r++ {
			seg := linkSeg{r, 0, len(grid[r])}
			if r == c0.row {
				seg.c0 = c0.col
			}
			if r == c1.row {
				seg.c1 = c1.col
			}
			if seg.c0 < seg.c1 {
				segs = append(segs, seg)
			}
		}
		return text.String()[start:end], segs, true
	}
	return "", nil, false
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
		if parsed.Path == "" || (parsed.Host != "" && parsed.Host != "localhost" && !strings.EqualFold(parsed.Host, host)) {
			return false
		}
		return safeLocalFile(parsed.Path)
	}
	return false
}

// safeLocalFile refuses what the desktop would run rather than show:
// executable files and .desktop launchers. Directories and documents open.
func safeLocalFile(path string) bool {
	if strings.HasSuffix(strings.ToLower(path), ".desktop") {
		return false
	}
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return st.IsDir() || st.Mode().Perm()&0o111 == 0
}

// updateHoverLink tracks the link under the pointer for underline and
// cursor feedback.
func (v *termView) updateHoverLink(x, y float64) {
	link := v.linkAt(x, y)
	if link != nil && !safeLink(link.url) {
		link = nil
	}
	if link.equal(v.hoverLink) {
		return
	}
	v.hoverLink = link
	if link != nil {
		v.SetCursorFromName("pointer")
		v.SetTooltipText("Ctrl+click to open")
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
	if l == nil || l.pane != p {
		return
	}
	ul := math.Round(min(v.ulPos, v.cellH-v.ulThick)) // on whole pixels, like text underlines
	for _, seg := range l.segs {
		if seg.row >= f.rows {
			continue
		}
		x0, x1 := v.pad+v.colX(f.x+seg.c0), v.pad+v.colX(f.x+seg.c1)
		y := v.pad + float64(f.y+seg.row)*v.cellH
		v.fillRect(s, x0, y+ul, x1-x0, v.ulThick, rgba(v.theme.palette[4], 1))
	}
}
