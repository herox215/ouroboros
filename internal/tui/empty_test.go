package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// nameRow is the first row of name as an empty chat draws it at full size,
// without the blanks at its ends: what a view shows of the name, if it does.
func nameRow(name string) string { return strings.TrimSpace(nameArt(name, 2)[0]) }

// The name is in the middle of the viewport, whatever its size: in the
// largest size of the font that fits, and in plain letters where none does.
func TestEmptyChatCentered(t *testing.T) {
	for _, c := range []struct {
		w, h  int
		scale int // 0 for the plain name
	}{
		{80, 21, 2}, {38, 6, 2}, {37, 6, 1}, {38, 5, 1}, {40, 10, 2}, {19, 3, 1}, {18, 3, 0}, {19, 2, 0}, {12, 4, 0}, {1, 1, 0},
	} {
		view := ansi.Strip(emptyChat("Luna", c.w, c.h))
		lines := strings.Split(view, "\n")
		if len(lines) != c.h {
			t.Fatalf("%dx%d: %d rows", c.w, c.h, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) != c.w {
				t.Fatalf("%dx%d: a row of %d columns: %q", c.w, c.h, ansi.StringWidth(line), line)
			}
		}
		rows := []string{ansi.Truncate("Luna", c.w, "")}
		if c.scale > 0 {
			rows = nameArt("Luna", c.scale)
		}
		if art := fitName("Luna", c.w, c.h); len(art) != 3*c.scale {
			t.Fatalf("%dx%d: %d rows, want the size %d", c.w, c.h, len(art), c.scale)
		}
		// What is left over is halved: of an odd rest the larger half is below and to the right.
		top, left := (c.h-len(rows))/2, (c.w-ansi.StringWidth(rows[0]))/2
		for i, row := range rows {
			if want := strings.Repeat(" ", left) + row + strings.Repeat(" ", c.w-left-ansi.StringWidth(row)); lines[top+i] != want {
				t.Fatalf("not centered at %dx%d:\n%s", c.w, c.h, view)
			}
		}
		if row, ok := emptyTaglineRow(c.w, c.h, len(rows)); ok {
			if strings.TrimSpace(lines[row]) != emptyTagline {
				t.Fatalf("%dx%d: missing tagline", c.w, c.h)
			}
			lines[row] = ""
		}
		if blank := strings.TrimSpace(strings.Join(append(lines[:top:top], lines[top+len(rows):]...), "")); blank != "" {
			t.Fatalf("%dx%d: %q around the name", c.w, c.h, blank)
		}
	}
}

func TestEmptyChatTagline(t *testing.T) {
	for _, animated := range []bool{false, true} {
		for _, size := range [][2]int{{80, 21}, {40, 10}, {38, 6}, {12, 4}, {1, 1}} {
			w, h := size[0], size[1]
			view := emptyChat("Luna", w, h)
			if animated {
				view = drawNameAnimation("Luna", w, h, 0, false, nameShine, 1)
			}
			lines := strings.Split(ansi.Strip(view), "\n")
			nameHeight := max(1, len(fitName("Luna", w, h)))
			row, ok := emptyTaglineRow(w, h, nameHeight)
			if ok {
				left := (w - len(emptyTagline)) / 2
				want := strings.Repeat(" ", left) + emptyTagline + strings.Repeat(" ", w-left-len(emptyTagline))
				if lines[row] != want {
					t.Fatalf("animated=%v, %dx%d: tagline %q, want %q", animated, w, h, lines[row], want)
				}
			} else if strings.Contains(view, emptyTagline) {
				t.Fatalf("tagline in small window %dx%d", w, h)
			}
		}
	}
}

func TestEmptyChatUsesCurrentProfileAndDisappears(t *testing.T) {
	m, b := newModel(t)
	b.status.Name = "Luna"
	m.Update(m.refreshStatus())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	marker := strings.TrimSpace(nameArt("Luna", 2)[2])
	if view := view(m); !strings.Contains(view, marker) || strings.Contains(view, "No messages yet") {
		t.Fatalf("empty profile chat:\n%s", view)
	}
	if line := strings.Split(ansi.Strip(m.chatView()), "\n")[1]; strings.Contains(line, "Luna") {
		t.Fatalf("empty chat still has a corner title: %s", line)
	}
	m.chat.entries = []entry{{mine: true, text: "hello"}}
	m.renderHistory(true)
	if view := view(m); strings.Contains(view, marker) || !strings.Contains(view, "hello") {
		t.Fatalf("conversation should replace artwork:\n%s", view)
	}
}

// A window too narrow for the name at full size shows it in the size of the
// splash title, and one too narrow for that in plain letters.
func TestEmptyChatFitsTheNameToTheWindow(t *testing.T) {
	m, b := newModel(t)
	b.status.Name = "herox215"
	m.Update(m.refreshStatus())
	for width, want := range map[int]string{
		82: strings.TrimSpace(nameArt("herox215", 2)[5]),
		81: strings.TrimSpace(nameArt("herox215", 1)[2]),
		43: strings.TrimSpace(nameArt("herox215", 1)[2]),
		42: "herox215",
	} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		if view := view(m); !strings.Contains(view, want) {
			t.Errorf("%d columns: no %q:\n%s", width, want, view)
		}
	}
}

func TestNameArtUsesSplashFont(t *testing.T) {
	if got := nameArt("Ouroboros", 1); strings.Join(got, "\n") != strings.Join(bigTitle, "\n") {
		t.Fatalf("the name is not in the font of the splash title:\n%s", strings.Join(got, "\n"))
	}
	// At twice the size a pixel is a cell high and two wide: every row of
	// the title is two rows, and every column two columns.
	big := nameArt("Ouroboros", 2)
	if len(big) != 6 {
		t.Fatalf("%d rows", len(big))
	}
	for y, row := range big {
		cells := []rune(row)
		if len(cells) != 2*len([]rune(bigTitle[0])) {
			t.Fatalf("row %d has %d columns", y, len(cells))
		}
		for x, r := range cells {
			half := []rune(bigTitle[y/2])[x/2]
			want := ' '
			if half == '█' || half == [2]rune{'▀', '▄'}[y%2] {
				want = '█'
			}
			if r != want {
				t.Fatalf("row %d, column %d: %q, want %q:\n%s", y, x, r, want, strings.Join(big, "\n"))
			}
		}
	}
}

func TestNameArtPreservesUnsupportedNames(t *testing.T) {
	for _, name := range []string{"月", "René", "Luna 🚀"} {
		if art := fitName(name, 200, 50); art != nil {
			t.Fatalf("%q is drawn:\n%s", name, strings.Join(art, "\n"))
		}
		if got := strings.TrimSpace(ansi.Strip(emptyChat(name, 40, 3))); got != name {
			t.Fatalf("%q is shown as %q", name, got)
		}
	}
	if strings.Join(nameArt("Luna", 2), "\n") == strings.Join(nameArt("alpha", 2), "\n") {
		t.Fatal("art must depend on the name")
	}
	// The font has every letter once: a name is drawn whatever its case.
	if strings.Join(nameArt("LuNa", 2), "\n") != strings.Join(nameArt("luna", 2), "\n") {
		t.Fatal("the case of a name changes how it is drawn")
	}
}

// The tagline is shown where the window holds all of it, and a row for it.
func TestEmptyTaglineNeedsItsWidth(t *testing.T) {
	n := len(emptyTagline)
	if row, ok := emptyTaglineRow(n, 20, 4); !ok || row != 13 {
		t.Errorf("as wide as the tagline: row %d, %v", row, ok)
	}
	if _, ok := emptyTaglineRow(n-1, 20, 4); ok {
		t.Error("narrower than the tagline, it should not be shown")
	}
}
