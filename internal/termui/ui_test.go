package termui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestImmediateModeFocusAndActivation(t *testing.T) {
	var ui Context
	checked := false
	draw := func(event Event, enabled bool) (bool, bool) {
		ui.BeginFrame(40, 4, event)
		changed := ui.Checkbox("tool", 0, -1, "Tool", &checked, enabled)
		clicked := ui.Button("run", 0, 1, "Run", true)
		ui.EndFrame()
		return changed, clicked
	}
	draw(Event{}, true)
	if ui.Focused() != "tool" {
		t.Fatalf("initial focus = %q", ui.Focused())
	}
	if changed, clicked := draw(Event{Key: KeySpace}, true); !changed || clicked || !checked {
		t.Fatalf("checkbox activation: changed=%v clicked=%v checked=%v", changed, clicked, checked)
	}
	if changed, clicked := draw(Event{}, true); changed || clicked {
		t.Fatal("activation persisted into the next frame")
	}
	draw(Event{Key: KeyTab}, true)
	if ui.Focused() != "run" {
		t.Fatalf("tab focus = %q", ui.Focused())
	}
	if _, clicked := draw(Event{Key: KeyEnter}, true); !clicked {
		t.Fatal("focused button did not activate")
	}
	draw(Event{Key: KeyBackTab}, true)
	if ui.Focused() != "tool" {
		t.Fatalf("back-tab focus = %q", ui.Focused())
	}
	draw(Event{}, false)
	if ui.Focused() != "run" {
		t.Fatalf("disabled widget retained focus: %q", ui.Focused())
	}
	ui.BeginFrame(1, 1, Event{})
	ui.EndFrame()
	if ui.Focused() != "" {
		t.Fatal("empty frame retained a focus trap")
	}
}

func TestFocusRequestAndDuplicateWidgetOnlyActivateOnce(t *testing.T) {
	var ui Context
	ui.Focus("second")
	ui.BeginFrame(40, 4, Event{Key: KeyEnter})
	if ui.Button("first", 0, 0, "First", true) {
		t.Fatal("unfocused first button activated")
	}
	if !ui.Button("second", 0, 1, "Second", true) {
		t.Fatal("requested focus did not activate")
	}
	if ui.Button("second", 0, 2, "Duplicate", true) {
		t.Fatal("duplicate ID activated twice")
	}
	ui.EndFrame()
}

func TestFocusableLabelCanInspectUnavailableItem(t *testing.T) {
	var ui Context
	ui.BeginFrame(30, 3, Event{Key: KeyEnter})
	ui.FocusableLabel("installed", 0, 0, "[x] Installed", StyleMuted)
	if ui.Button("run", 0, 1, "Run", true) {
		t.Fatal("read-only row allowed another button to activate")
	}
	frame := ui.EndFrame()
	if ui.Focused() != "installed" || !strings.Contains(frame.Text(), "[x] Installed") {
		t.Fatalf("read-only row missing or unfocusable: focus=%q text=%q", ui.Focused(), frame.Text())
	}
	ui.BeginFrame(30, 3, Event{Key: KeyDown})
	ui.FocusableLabel("installed", 0, 0, "[x] Installed", StyleMuted)
	ui.Button("run", 0, 1, "Run", true)
	ui.EndFrame()
	if ui.Focused() != "run" {
		t.Fatalf("cannot navigate away from read-only row: %q", ui.Focused())
	}
}

func TestCanvasSanitizesClipsAndKeepsUTF8(t *testing.T) {
	var ui Context
	ui.BeginFrame(8, 3, Event{})
	ui.Label(0, 0, "ab界界界", StyleNormal)
	ui.Label(-1, 1, "éxy", StyleNormal)
	ui.Label(0, 2, "a\x1b[2Jb\x1b]0;bad title\a"+"c\r\nd\x00", StyleNormal)
	frame := ui.EndFrame()
	want := []string{"ab界界", "xy", "abc  d "}
	for i, row := range frame.rows {
		if row != want[i] {
			t.Errorf("row %d = %q, want %q", i, row, want[i])
		}
		if !utf8.ValidString(row) {
			t.Errorf("row %d contains split UTF-8", i)
		}
	}
	if strings.Contains(strings.Join(frame.rows, ""), "bad title") {
		t.Fatal("OSC payload was printed")
	}
	ui.BeginFrame(1, 1, Event{})
	ui.Label(0, 0, "text", StyleNormal)
	if got := ui.EndFrame().rows[0]; got != "" {
		t.Fatalf("one-column screen emitted text: %q", got)
	}
	ui.BeginFrame(0, 0, Event{})
	ui.Label(0, 0, "text", StyleNormal)
	if len(ui.EndFrame().rows) != 0 {
		t.Fatal("zero-size screen has rows")
	}
}

func TestCanvasOverwritingWideCells(t *testing.T) {
	c := newCanvas(8, 1)
	c.text(0, 0, "界界", StyleNormal)
	c.text(1, 0, "X", StyleNormal)
	if got := c.frame().rows[0]; got != " X界" {
		t.Fatalf("overwriting continuation = %q", got)
	}
	c.text(2, 0, "Y", StyleNormal)
	if got := c.frame().rows[0]; got != " XY" {
		t.Fatalf("overwriting wide base = %q", got)
	}
}

func TestSanitizeHostileTerminalText(t *testing.T) {
	tests := []struct{ input, want string }{
		{"plain é界", "plain é界"},
		{"a\x1b[31mred\x1b[0mz", "aredz"},
		{"a\x1b]8;;https://host/\x1b\\link\x1b]8;;\x1b\\z", "alinkz"},
		{"a\x1bPdanger\x1b\\z", "az"},
		{"a\u009b2Jb", "ab"},
		{"a\u009dbad\u009cz", "az"},
		{"a\r\n\t\b\x00\u202eb", "a      b"},
		{"a\x1b(Bz", "az"},
		{"a\x1b]unfinished", "a"},
		{"a\xffz", "a�z"},
	}
	for _, tt := range tests {
		if got := Sanitize(tt.input); got != tt.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
