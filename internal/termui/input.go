package termui

import "unicode/utf8"

// keyDecoder buffers incomplete UTF-8 and escape sequences across reads.
type keyDecoder struct {
	pending []byte
}

func (d *keyDecoder) feed(data []byte) { d.pending = append(d.pending, data...) }

func (d *keyDecoder) next(flushEscape bool) (Event, bool) {
	for len(d.pending) > 0 {
		first := d.pending[0]
		if first == 0x1b {
			if len(d.pending) == 1 {
				if !flushEscape {
					return Event{}, false
				}
				d.pending = d.pending[1:]
				return Event{Key: KeyEscape}, true
			}
			if d.pending[1] == '[' || d.pending[1] == 'O' {
				end := 2
				for end < len(d.pending) && !(d.pending[end] >= 0x40 && d.pending[end] <= 0x7e) {
					end++
				}
				if end == len(d.pending) {
					if !flushEscape && len(d.pending) < 64 {
						return Event{}, false
					}
					// A malformed or abandoned escape is discarded as one unit.
					d.pending = nil
					return Event{Key: KeyEscape}, true
				}
				final := d.pending[end]
				d.pending = d.pending[end+1:]
				switch final {
				case 'A':
					return Event{Key: KeyUp}, true
				case 'B':
					return Event{Key: KeyDown}, true
				case 'C':
					return Event{Key: KeyRight}, true
				case 'D':
					return Event{Key: KeyLeft}, true
				case 'Z':
					return Event{Key: KeyBackTab}, true
				}
				continue
			}
			// Preserve the following printable key for the next event.
			d.pending = d.pending[1:]
			return Event{Key: KeyEscape}, true
		}
		d.pending = d.pending[1:]
		switch first {
		case 3:
			return Event{Key: KeyCtrlC}, true
		case '\t':
			return Event{Key: KeyTab}, true
		case '\r', '\n':
			return Event{Key: KeyEnter}, true
		case ' ':
			return Event{Key: KeySpace}, true
		}
		if first < 0x20 || first == 0x7f {
			continue
		}
		if first < utf8.RuneSelf {
			return Event{Key: KeyRune, Rune: rune(first)}, true
		}
		// Restore the leading byte while deciding whether this read ended in
		// the middle of a multibyte rune.
		d.pending = append([]byte{first}, d.pending...)
		if !utf8.FullRune(d.pending) {
			return Event{}, false
		}
		r, n := utf8.DecodeRune(d.pending)
		d.pending = d.pending[n:]
		if r != utf8.RuneError {
			return Event{Key: KeyRune, Rune: r}, true
		}
	}
	return Event{}, false
}
