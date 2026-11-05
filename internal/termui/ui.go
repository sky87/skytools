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
	KeyPageUp
	KeyPageDown
	KeyHome
	KeyEnd
	KeyBackspace
)

// Event is either a key press or a terminal size change. Rune is set for KeyRune.
type Event struct {
	Key  Key
	Rune rune
}

// Style combines one of the 16 basic ANSI colors with text attributes. Basic
// colors follow the terminal's own theme. Meaning must never rely on color alone:
// NO_COLOR removes the color and keeps the attributes.
type Style uint8

const (
	colorMask Style = 0b0111

	colorRed    Style = 1
	colorGreen  Style = 2
	colorYellow Style = 3
	colorCyan   Style = 6

	attrBold    Style = 1 << 3
	attrDim     Style = 1 << 4
	attrReverse Style = 1 << 5
)

const (
	StyleNormal      Style = 0
	StyleTitle             = attrBold
	StyleMuted             = attrDim
	StyleDisabled          = attrDim
	StyleFocused           = attrReverse
	StyleAccent            = colorCyan
	StyleAccentBold        = colorCyan | attrBold
	StyleSuccess           = colorGreen
	StyleSuccessBold       = colorGreen | attrBold
	StyleWarning           = colorYellow
	StyleWarningBold       = colorYellow | attrBold
	StyleError             = colorRed
	StyleErrorBold         = colorRed | attrBold
)

// Display describes what the terminal can show. Its zero value is full Unicode
// and color.
type Display struct {
	ASCII   bool // Replace non-ASCII text with same-width ASCII.
	NoColor bool // Keep bold and reverse, drop colors.
}

// DisplayFromEnv applies NO_COLOR and the locale rules: LC_ALL, then LC_CTYPE,
// then LANG. Containers often set none of them, which means the C locale.
func DisplayFromEnv(getenv func(string) string) Display {
	display := Display{NoColor: getenv("NO_COLOR") != "", ASCII: true}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if value := getenv(name); value != "" {
			value = strings.ToLower(value)
			display.ASCII = !strings.Contains(value, "utf-8") && !strings.Contains(value, "utf8")
			break
		}
	}
	return display
}

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
	display  Display
}

// SetDisplay applies terminal capabilities to the frames that follow.
func (c *Context) SetDisplay(display Display) { c.display = display }

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
	c.canvas.display = c.display
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

// Highlight applies style to columns x0 up to x1 of row y, blanks included. Draw
// a row's text first: this replaces every style in the range, so a focused row
// reads as one solid bar.
func (c *Context) Highlight(y, x0, x1 int, style Style) { c.canvas.highlight(y, x0, x1, style) }

// List declares a list as one focus stop. The cursor lives in the application.
// It wraps on arrows and j/k, stops at the ends on paging, and always stays in
// range. It returns true when the focused list consumed the frame's event.
func (c *Context) List(id string, cursor *int, count, page int, enabled bool) bool {
	focused := c.widget(id, enabled)
	if count <= 0 {
		*cursor = 0
		return false
	}
	*cursor = max(0, min(*cursor, count-1))
	if !focused || c.consumed {
		return false
	}
	switch {
	case c.event.Key == KeyUp, c.event.Key == KeyRune && c.event.Rune == 'k':
		*cursor = (*cursor - 1 + count) % count
	case c.event.Key == KeyDown, c.event.Key == KeyRune && c.event.Rune == 'j':
		*cursor = (*cursor + 1) % count
	case c.event.Key == KeyPageUp:
		*cursor = max(0, *cursor-max(1, page))
	case c.event.Key == KeyPageDown:
		*cursor = min(count-1, *cursor+max(1, page))
	case c.event.Key == KeyHome, c.event.Key == KeyRune && c.event.Rune == 'g':
		*cursor = 0
	case c.event.Key == KeyEnd, c.event.Key == KeyRune && c.event.Rune == 'G':
		*cursor = count - 1
	default:
		return false
	}
	c.consumed = true
	return true
}

// Scroll declares scrollable text as one focus stop. The top line offset lives
// in the application and is clamped so that the last page stays full.
func (c *Context) Scroll(id string, offset *int, total, page int, enabled bool) bool {
	focused := c.widget(id, enabled)
	limit := max(0, total-max(1, page))
	*offset = max(0, min(*offset, limit))
	if !focused || c.consumed {
		return false
	}
	switch {
	case c.event.Key == KeyUp, c.event.Key == KeyRune && c.event.Rune == 'k':
		*offset = max(0, *offset-1)
	case c.event.Key == KeyDown, c.event.Key == KeyRune && c.event.Rune == 'j':
		*offset = min(limit, *offset+1)
	case c.event.Key == KeyPageUp:
		*offset = max(0, *offset-max(1, page))
	case c.event.Key == KeyPageDown:
		*offset = min(limit, *offset+max(1, page))
	case c.event.Key == KeyHome, c.event.Key == KeyRune && c.event.Rune == 'g':
		*offset = 0
	case c.event.Key == KeyEnd, c.event.Key == KeyRune && c.event.Rune == 'G':
		*offset = limit
	default:
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
