package termui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type cell struct {
	text  string
	style Style
	wide  bool
	cont  bool
}

type canvas struct {
	width, height int
	cells         [][]cell
}

func newCanvas(width, height int) canvas {
	// Keep allocations bounded even if a caller or a broken terminal reports an
	// unreasonable size. Leaving the physical final column empty avoids implicit
	// wrapping, including on the bottom row.
	width = max(0, min(width, 4096))
	height = max(0, min(height, 1024))
	c := canvas{width: width, height: height, cells: make([][]cell, height)}
	for y := range c.cells {
		c.cells[y] = make([]cell, max(0, width-1))
	}
	return c
}

func (c *canvas) clearCell(x, y int) {
	row := c.cells[y]
	if row[x].cont && x > 0 {
		row[x-1] = cell{}
	}
	if row[x].wide && x+1 < len(row) {
		row[x+1] = cell{}
	}
	row[x] = cell{}
}

func (c *canvas) text(x, y int, text string, style Style) {
	if y < 0 || y >= c.height {
		return
	}
	row := c.cells[y]
	last := -1
	for _, r := range sanitize(text) {
		// Presentation selectors can turn a one-column symbol into a wide
		// emoji. Omit them so width accounting cannot cause an implicit wrap.
		if r == 0xfe0e || r == 0xfe0f {
			continue
		}
		width := runeWidth(r)
		if width == 0 {
			// Combining marks are safe only after a base character drawn by this
			// label. Avoid attaching them to a preceding widget or terminal cell.
			if last >= 0 {
				row[last].text += string(r)
			}
			continue
		}
		if x >= len(row) {
			break
		}
		if x >= 0 && x+width <= len(row) {
			c.clearCell(x, y)
			if width == 2 {
				c.clearCell(x+1, y)
			}
			row[x] = cell{text: string(r), style: style, wide: width == 2}
			if width == 2 {
				row[x+1] = cell{cont: true, style: style}
			}
			last = x
		} else {
			last = -1
		}
		x += width
	}
}

var styleCodes = [...]string{
	"\x1b[0m", "\x1b[1;36m", "\x1b[2m", "\x1b[36m", "\x1b[32m", "\x1b[31m", "\x1b[7m", "\x1b[2m",
}

func (c canvas) frame() Frame {
	frame := Frame{Width: c.width, Height: c.height, rows: make([]string, c.height)}
	for y, row := range c.cells {
		end := len(row)
		for end > 0 && row[end-1].text == "" && !row[end-1].cont {
			end--
		}
		var b strings.Builder
		style := StyleNormal
		for _, pixel := range row[:end] {
			if pixel.cont {
				continue
			}
			if pixel.style >= Style(len(styleCodes)) {
				pixel.style = StyleNormal
			}
			if style != pixel.style {
				b.WriteString(styleCodes[pixel.style])
				style = pixel.style
			}
			if pixel.text == "" {
				b.WriteByte(' ')
			} else {
				b.WriteString(pixel.text)
			}
		}
		frame.rows[y] = b.String()
	}
	return frame
}

// sanitize strips complete ANSI instructions, including OSC/DCS payloads, and
// turns ordinary control characters into spaces. No untrusted escape byte or
// C1 instruction can reach the terminal through a Label.
func sanitize(text string) string {
	var b strings.Builder
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		if r == '\x1b' {
			if len(text) == 0 {
				continue
			}
			kind := text[0]
			text = text[1:]
			switch kind {
			case '[':
				text = skipCSI(text)
			case ']', 'P', 'X', '^', '_':
				text = skipStringEscape(text)
			default:
				// ANSI ESC commands may include intermediate bytes (e.g. ESC ( B).
				for kind >= 0x20 && kind <= 0x2f && len(text) > 0 {
					kind = text[0]
					text = text[1:]
				}
			}
			continue
		}
		switch r {
		case 0x9b:
			text = skipCSI(text)
			continue
		case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
			text = skipStringEscape(text)
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func skipCSI(text string) string {
	for i := 0; i < len(text); i++ {
		if text[i] >= 0x40 && text[i] <= 0x7e {
			return text[i+1:]
		}
	}
	return ""
}

func skipStringEscape(text string) string {
	for i := 0; i < len(text); i++ {
		if text[i] == '\a' {
			return text[i+1:]
		}
		if text[i] == '\x1b' && i+1 < len(text) && text[i+1] == '\\' {
			return text[i+2:]
		}
		if text[i] == 0xc2 && i+1 < len(text) && text[i+1] == 0x9c {
			return text[i+2:]
		}
	}
	return ""
}

func runeWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	// Wide/fullwidth ranges and the common terminal emoji ranges. Ambiguous
	// characters use one column, matching terminals in their default locale.
	if emojiWide(r) || r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) || (r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) || (r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) || (r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x16fe0 && r <= 0x18dff) || (r >= 0x1aff0 && r <= 0x1b2ff) ||
		(r >= 0x1f300 && r <= 0x1faff) || (r >= 0x20000 && r <= 0x3fffd)) {
		return 2
	}
	return 1
}

func emojiWide(r rune) bool {
	return (r >= 0x231a && r <= 0x231b) || (r >= 0x23e9 && r <= 0x23ec) ||
		r == 0x23f0 || r == 0x23f3 || (r >= 0x25fd && r <= 0x25fe) ||
		(r >= 0x2614 && r <= 0x2615) || (r >= 0x2648 && r <= 0x2653) ||
		r == 0x267f || r == 0x2693 || r == 0x26a1 || (r >= 0x26aa && r <= 0x26ab) ||
		(r >= 0x26bd && r <= 0x26be) || (r >= 0x26c4 && r <= 0x26c5) ||
		r == 0x26ce || r == 0x26d4 || r == 0x26ea || (r >= 0x26f2 && r <= 0x26f3) ||
		r == 0x26f5 || r == 0x26fa || r == 0x26fd || r == 0x2705 ||
		(r >= 0x270a && r <= 0x270b) || r == 0x2728 || r == 0x274c || r == 0x274e ||
		(r >= 0x2753 && r <= 0x2755) || r == 0x2757 || (r >= 0x2795 && r <= 0x2797) ||
		r == 0x27b0 || r == 0x27bf || (r >= 0x2b1b && r <= 0x2b1c) ||
		r == 0x2b50 || r == 0x2b55 || r == 0x1f004 || r == 0x1f0cf || r == 0x1f18e ||
		(r >= 0x1f191 && r <= 0x1f19a) || (r >= 0x1f1e6 && r <= 0x1f251)
}

// Sanitize returns safe, single-line terminal text. It strips ANSI escape
// instructions and their payloads, and replaces other controls with spaces.
func Sanitize(text string) string { return sanitize(text) }
