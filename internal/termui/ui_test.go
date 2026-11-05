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

func TestListIsOneFocusStopWithApplicationCursor(t *testing.T) {
	var ui Context
	cursor := 0
	step := func(event Event) bool {
		ui.BeginFrame(40, 10, event)
		consumed := ui.List("rows", &cursor, 10, 4, true)
		ui.EndFrame()
		return consumed
	}
	step(Event{})
	for _, tt := range []struct {
		event Event
		want  int
	}{
		{Event{Key: KeyUp}, 9}, {Event{Key: KeyDown}, 0}, {Event{Key: KeyRune, Rune: 'j'}, 1}, {Event{Key: KeyRune, Rune: 'k'}, 0},
		{Event{Key: KeyPageDown}, 4}, {Event{Key: KeyPageUp}, 0}, {Event{Key: KeyPageUp}, 0}, {Event{Key: KeyEnd}, 9},
		{Event{Key: KeyPageDown}, 9}, {Event{Key: KeyHome}, 0}, {Event{Key: KeyRune, Rune: 'G'}, 9}, {Event{Key: KeyRune, Rune: 'g'}, 0},
	} {
		if !step(tt.event) || cursor != tt.want {
			t.Fatalf("%+v: cursor = %d, want %d", tt.event, cursor, tt.want)
		}
		if ui.Focused() != "rows" {
			t.Fatalf("list lost focus: %q", ui.Focused())
		}
	}
	if step(Event{Key: KeyRune, Rune: 'x'}) {
		t.Fatal("list consumed an unrelated key")
	}
	cursor = 50
	ui.BeginFrame(40, 10, Event{})
	ui.List("rows", &cursor, 3, 1, false)
	ui.EndFrame()
	if cursor != 2 {
		t.Fatalf("cursor was not clamped: %d", cursor)
	}
}

func TestScrollClampsToLastFullPage(t *testing.T) {
	var ui Context
	offset := 0
	step := func(event Event, total int) {
		ui.BeginFrame(40, 10, event)
		ui.Scroll("log", &offset, total, 4, true)
		ui.EndFrame()
	}
	step(Event{}, 10)
	for _, tt := range []struct {
		key  Key
		want int
	}{{KeyDown, 1}, {KeyEnd, 6}, {KeyDown, 6}, {KeyPageUp, 2}, {KeyUp, 1}, {KeyPageDown, 5}, {KeyHome, 0}, {KeyUp, 0}} {
		step(Event{Key: tt.key}, 10)
		if offset != tt.want {
			t.Fatalf("%v: offset = %d, want %d", tt.key, offset, tt.want)
		}
	}
	offset = 6
	step(Event{}, 2)
	if offset != 0 {
		t.Fatalf("short content kept an offset: %d", offset)
	}
}

func TestHighlightMakesASolidBar(t *testing.T) {
	var ui Context
	ui.BeginFrame(12, 1, Event{})
	ui.Label(0, 0, "ab", StyleAccent)
	ui.Label(4, 0, "cd", StyleError)
	ui.Highlight(0, 0, 8, StyleFocused)
	want := "\x1b[0;7mab  cd  "
	if got := ui.EndFrame().rows[0]; got != want {
		t.Fatalf("row = %q, want %q", got, want)
	}
}

func TestStyleCodesResetAndHonorNoColor(t *testing.T) {
	var ui Context
	ui.BeginFrame(20, 1, Event{})
	ui.Label(0, 0, "a", StyleAccentBold)
	ui.Label(1, 0, "b", StyleMuted)
	ui.Label(2, 0, "c", StyleNormal)
	if got, want := ui.EndFrame().rows[0], "\x1b[0;1;36ma\x1b[0;2mb\x1b[0mc"; got != want {
		t.Fatalf("color row = %q, want %q", got, want)
	}
	ui.SetDisplay(Display{NoColor: true})
	ui.BeginFrame(20, 1, Event{})
	ui.Label(0, 0, "a", StyleAccentBold)
	ui.Label(1, 0, "b", StyleFocused)
	if got, want := ui.EndFrame().rows[0], "\x1b[0;1ma\x1b[0;7mb"; got != want {
		t.Fatalf("NO_COLOR row = %q, want %q", got, want)
	}
}

func TestDisplayFromEnv(t *testing.T) {
	for _, tt := range []struct {
		env   map[string]string
		ascii bool
		color bool
	}{
		{nil, true, true},
		{map[string]string{"LANG": "C"}, true, true},
		{map[string]string{"LANG": "en_US.UTF-8"}, false, true},
		{map[string]string{"LC_CTYPE": "en_US.utf8", "LANG": "C"}, false, true},
		{map[string]string{"LC_ALL": "POSIX", "LANG": "en_US.UTF-8"}, true, true},
		{map[string]string{"LANG": "C.UTF-8", "NO_COLOR": "1"}, false, false},
		{map[string]string{"LANG": "C.UTF-8", "NO_COLOR": ""}, false, true},
	} {
		got := DisplayFromEnv(func(name string) string { return tt.env[name] })
		if got.ASCII != tt.ascii || got.NoColor == tt.color {
			t.Errorf("%v: %+v", tt.env, got)
		}
	}
}

func TestASCIIDisplayKeepsColumnsAligned(t *testing.T) {
	var ui Context
	ui.SetDisplay(Display{ASCII: true})
	ui.BeginFrame(30, 3, Event{})
	ui.Label(0, 0, "› ● 1.0 → 2.0 ↑ ✓ ✗ ○ ─…", StyleNormal)
	ui.Label(0, 1, "e\u0301 ⠋⠙⠹⠸", StyleNormal)
	ui.Label(0, 2, "界x", StyleNormal)
	frame := ui.EndFrame()
	if got, want := frame.rows[0], "> * 1.0 > 2.0 ^ + x o -~"; got != want {
		t.Errorf("glyphs = %q, want %q", got, want)
	}
	if got, want := frame.rows[1], "e |/-\\"; got != want {
		t.Errorf("spinner and marks = %q, want %q", got, want)
	}
	if got, want := frame.rows[2], "?x"; got != want {
		t.Errorf("wide rune = %q, want %q", got, want)
	}
	for _, row := range frame.rows {
		for _, r := range row {
			if r >= utf8.RuneSelf {
				t.Fatalf("non-ASCII reached the terminal: %q", row)
			}
		}
	}
}

func TestTruncateAndWrapNeverLoseOrOverflow(t *testing.T) {
	if got := Truncate("abcdef", 4); got != "abc…" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("abc", 3); got != "abc" {
		t.Errorf("Truncate exact = %q", got)
	}
	if got := Truncate("界界界", 4); got != "界…" {
		t.Errorf("Truncate wide = %q", got)
	}
	if got := strings.Join(Wrap("one two  three", 7), "|"); got != "one two|three" {
		t.Errorf("Wrap = %q", got)
	}
	url := "https://example.com/" + strings.Repeat("x", 30)
	lines := Wrap("see "+url+" now", 12)
	if strings.Join(strings.Fields(strings.Join(lines, " ")), "") != strings.ReplaceAll("see "+url+" now", " ", "") {
		t.Errorf("Wrap lost text: %q", lines)
	}
	for _, line := range lines {
		if Width(line) > 12 {
			t.Errorf("Wrap overflowed: %q", line)
		}
	}
	if got := Wrap("界界", 1); len(got) == 0 {
		t.Error("a rune wider than the line vanished")
	}
	if got := Wrap("", 10); len(got) != 1 || got[0] != "" {
		t.Errorf("empty text = %q", got)
	}
	if got := Wrap("a\x1b[31mb", 10); got[0] != "ab" {
		t.Errorf("Wrap did not sanitize: %q", got)
	}
}
