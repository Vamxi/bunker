# bunker's gotk4 patches

Based on github.com/diamondburned/gotk4/pkg v0.4.1 (MPL-2.0 / ISC, see LICENSE).
Unused packages (atk, gtk/v3, gdk/v3, gdkwayland, gdkx11, gdkpixdata) are omitted.

Local fixes:

- `core/glib/go.go` `Overrides`: return the zero value instead of panicking
  when a subclass supplies only its direct parent's overrides and an ancestor's
  overrides type is requested. Upstream (checked at f90c213) panics in
  init_instance for every Go subclass that overrides GtkWidget vfuncs.
- `core/glib/go.go` `OverridesFromObj`: resolve the Go subclass instance
  before calling the `WithOverrides` factory. Upstream passes the `*Object`
  wrapper, so the factory's `obj.(T)` yields nil and every vfunc (measure,
  snapshot, …) runs with a nil receiver. Same zero-value fallback as above.
