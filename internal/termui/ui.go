// Package termui provides a small immediate-mode, keyboard-driven terminal UI.
// Applications own their data and declare every widget again for each frame.
package termui

import "strings"

// Key identifies one terminal input event.
type Key uint8

const (
	KeyNone Key = iota
	KeyRune
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyTab
	KeyBackTab
	KeySpace
	KeyEnter
	KeyEscape
	KeyCtrlC
	KeyResize
)

// Event is either a key press or a terminal size change. Rune is set for KeyRune.
type Event struct {
	Key  Key
	Rune rune
}

// Style selects a built-in, terminal-safe text appearance.
type Style uint8

const (
	StyleNormal Style = iota
	StyleTitle
	StyleMuted
	StyleAccent
	StyleSuccess
	StyleError
	StyleFocused
	StyleDisabled
)

// Frame is a complete screen. Construct it with Context.EndFrame.
// Width includes the physical final column, which is kept empty to avoid wrapping.
type Frame struct {
	Width, Height int
	rows          []string
}

// Context retains only keyboard focus bookkeeping between frames. Its zero
// value is ready to use. Checkbox values and other application state stay outside
// the context; widget activation is true only in the frame handling that event.
type Context struct {
	focused  string
	previous []string
	current  []string
	event    Event
	consumed bool
	canvas   canvas
}

// Focus requests a stable widget ID. It can be called before or during a frame.
func (c *Context) Focus(id string) { c.focused = id }

// Focused returns the current stable widget ID, including navigation performed
// by BeginFrame. Applications can use it to keep their viewport on that widget.
func (c *Context) Focused() string { return c.focused }

// BeginFrame starts a fresh immediate-mode declaration. Coordinates are zero
// based. Labels outside the screen are clipped, so offscreen widgets can still
// be declared to participate in keyboard navigation.
func (c *Context) BeginFrame(width, height int, event Event) {
	c.canvas = newCanvas(width, height)
	c.current = c.current[:0]
	c.event = event
	c.consumed = false
	step := 0
	switch event.Key {
	case KeyTab, KeyDown, KeyRight:
		step = 1
	case KeyBackTab, KeyUp, KeyLeft:
		step = -1
	}
	if step != 0 && len(c.previous) > 0 {
		index := -1
		for i, id := range c.previous {
			if id == c.focused {
				index = i
				break
			}
		}
		if index < 0 {
			if step > 0 {
				index = 0
			} else {
				index = len(c.previous) - 1
			}
		} else {
			index = (index + step + len(c.previous)) % len(c.previous)
		}
		c.focused = c.previous[index]
	}
}

// Label draws one clipped line. Terminal escapes and control characters in text
// are removed or replaced before drawing; callers may pass untrusted log lines.
func (c *Context) Label(x, y int, text string, style Style) {
	c.canvas.text(x, y, text, style)
}

// FocusableLabel draws a read-only row that participates in keyboard focus.
// This is useful for inspecting items whose checkbox or action is unavailable.
func (c *Context) FocusableLabel(id string, x, y int, text string, style Style) {
	if c.widget(id, true) {
		style = StyleFocused
	}
	c.Label(x, y, text, style)
}

func (c *Context) widget(id string, enabled bool) bool {
	if !enabled || id == "" {
		return false
	}
	for _, existing := range c.current {
		if existing == id {
			return false
		}
	}
	c.current = append(c.current, id)
	if c.focused == "" {
		c.focused = id
	}
	return id == c.focused
}

func (c *Context) activate(focused bool) bool {
	if !focused || c.consumed || (c.event.Key != KeySpace && c.event.Key != KeyEnter) {
		return false
	}
	c.consumed = true
	return true
}

// Checkbox draws a checkbox backed by an external value. It returns true when
// the value changes. Disabled widgets do not participate in keyboard navigation.
func (c *Context) Checkbox(id string, x, y int, label string, value *bool, enabled bool) bool {
	if value == nil {
		enabled = false
	}
	focused := c.widget(id, enabled)
	changed := c.activate(focused)
	if changed {
		*value = !*value
	}
	mark := "[ ] "
	if value != nil && *value {
		mark = "[x] "
	}
	style := StyleNormal
	if !enabled {
		style = StyleDisabled
	} else if focused {
		style = StyleFocused
	}
	c.Label(x, y, mark+label, style)
	return changed
}

// Button draws a button and returns its transient activation event.
func (c *Context) Button(id string, x, y int, label string, enabled bool) bool {
	focused := c.widget(id, enabled)
	style := StyleAccent
	if !enabled {
		style = StyleDisabled
	} else if focused {
		style = StyleFocused
	}
	c.Label(x, y, "[ "+label+" ]", style)
	return c.activate(focused)
}

// EndFrame finishes the declaration and retains only the IDs needed for focus
// navigation. If the focused widget disappeared, focus moves to the first one.
func (c *Context) EndFrame() Frame {
	found := false
	for _, id := range c.current {
		if id == c.focused {
			found = true
			break
		}
	}
	if !found {
		c.focused = ""
		if len(c.current) > 0 {
			c.focused = c.current[0]
		}
	}
	c.previous = append(c.previous[:0], c.current...)
	return c.canvas.frame()
}

// Text returns the frame's plain text rows without styling, for snapshots and
// accessibility integrations. It never contains terminal escape instructions.
func (f Frame) Text() string {
	rows := make([]string, len(f.rows))
	for i, row := range f.rows {
		rows[i] = Sanitize(row)
	}
	return strings.Join(rows, "\n")
}
