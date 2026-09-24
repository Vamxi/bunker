// cellaspect.go - terminal cell pixel aspect ratio (H/W).
//
// Used by splitActive() to decide vertical vs. horizontal split based on
// actual pixel dimensions rather than just character-cell counts.
//
// The default of 2.25 suits common monospace fonts (e.g. Noto Sans Mono 11pt
// in Ptyxis).  Override via cell_aspect in ~/.config/bunker/config.toml.
package main

// queryCellAspect returns the cell pixel aspect ratio (height / width).
// cfgAspect is the value from config (0 = not set → use default 2.25).
func queryCellAspect(cfgAspect float64) float64 {
	if cfgAspect > 0 {
		L.Debug("queryCellAspect: using config value", "aspect", cfgAspect)
		return cfgAspect
	}
	L.Debug("queryCellAspect: using default", "aspect", 2.25)
	return 2.25
}
