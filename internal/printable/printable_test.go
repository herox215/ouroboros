package printable

import "testing"

// What would steer a terminal is left out, a line break and a tab are a
// space, and of a long text its start is kept.
func TestLine(t *testing.T) {
	for in, want := range map[string]string{
		"luna":                  "luna",
		"\x1b[31mred\x1b[0m":    "[31mred[0m",
		"\x1b]0;pwned\x07evil":  "]0;pwnedevil",
		"two\nlines\tand\rmore": "two lines and more",
		"abc\u202edlrow":        "abcdlrow",
		"\u200b":                "",
		"👩\u200d💻 dev":          "👩\u200d💻 dev",
		"bad \xff utf8":         "bad  utf8",
		"ünïcödé":               "ünïcödé",
		"می\u200cخواهم":         "می\u200cخواهم",
	} {
		if got := Line(in, 100); got != want {
			t.Errorf("Line(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Line("äöü0123456789", 5); got != "äöü01" {
		t.Errorf("cut: %q", got)
	}
}
