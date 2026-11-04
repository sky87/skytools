package termui

import "testing"

func TestDecoderFragmentedKeys(t *testing.T) {
	var d keyDecoder
	for _, part := range [][]byte{{0x1b}, {'['}} {
		d.feed(part)
		if event, ok := d.next(false); ok {
			t.Fatalf("partial escape emitted %+v", event)
		}
	}
	d.feed([]byte("A"))
	if event, ok := d.next(false); !ok || event.Key != KeyUp {
		t.Fatalf("split up arrow = %+v, %v", event, ok)
	}
	encoded := []byte("界")
	d.feed(encoded[:1])
	if _, ok := d.next(false); ok {
		t.Fatal("partial UTF-8 emitted a key")
	}
	d.feed(encoded[1:2])
	if _, ok := d.next(false); ok {
		t.Fatal("second partial UTF-8 emitted a key")
	}
	d.feed(encoded[2:])
	if event, ok := d.next(false); !ok || event.Key != KeyRune || event.Rune != '界' {
		t.Fatalf("split rune = %+v, %v", event, ok)
	}
}

func TestDecoderKeysAndEscapeDisambiguation(t *testing.T) {
	var d keyDecoder
	d.feed([]byte("\x1b[B\x1b[C\x1b[D\x1b[Z\x1bOA\t \r\x03q"))
	for _, key := range []Key{KeyDown, KeyRight, KeyLeft, KeyBackTab, KeyUp, KeyTab, KeySpace, KeyEnter, KeyCtrlC, KeyRune} {
		event, ok := d.next(false)
		if !ok || event.Key != key {
			t.Fatalf("event=%+v ok=%v, want key %v", event, ok, key)
		}
		if key == KeyRune && event.Rune != 'q' {
			t.Fatalf("rune = %q", event.Rune)
		}
	}
	d.feed([]byte{0x1b})
	if _, ok := d.next(false); ok {
		t.Fatal("lone escape did not wait")
	}
	if event, ok := d.next(true); !ok || event.Key != KeyEscape {
		t.Fatalf("flushed escape = %+v, %v", event, ok)
	}
	d.feed([]byte("\x1b[999~\x1b[1;5A\xffx"))
	if event, ok := d.next(false); !ok || event.Key != KeyUp {
		t.Fatalf("modified arrow after unknown escape = %+v, %v", event, ok)
	}
	if event, ok := d.next(false); !ok || event.Rune != 'x' {
		t.Fatalf("valid key after invalid UTF-8 = %+v, %v", event, ok)
	}
}
