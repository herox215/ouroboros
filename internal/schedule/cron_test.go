package schedule

import (
	"testing"
	"time"
)

func TestCronSyntaxAndCalendar(t *testing.T) {
	for _, expr := range []string{"", "* * * *", "60 * * * *", "0 24 * * *", "0 0 0 * *", "0 0 * 13 *", "0 0 * * 8", "*/0 * * * *", "*/-1 * * * *", "1-0 * * * *", "0,,1 * * * *", "x * * * *", "0 0 * * MON"} {
		if _, e := ParseCron(expr); e == nil {
			t.Errorf("accepted %q", expr)
		}
	}
	for _, tt := range []struct{ expr, after, want string }{
		{"*/15 * * * *", "2026-01-01T00:00:00Z", "2026-01-01T00:15:00Z"},
		{"0 9 * * 1-5", "2026-01-02T09:00:00Z", "2026-01-05T09:00:00Z"},
		{"0 9 */2 * 1", "2026-01-01T00:00:00Z", "2026-01-05T09:00:00Z"},
		{"0 9 * * 7", "2026-01-02T09:00:00Z", "2026-01-04T09:00:00Z"},
		{"0 9 13 * 1", "2026-01-12T09:00:00Z", "2026-01-13T09:00:00Z"},
		{"0 0 29 2 *", "2026-01-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"5,35 8-10/2 * * *", "2026-01-01T08:35:00Z", "2026-01-01T10:05:00Z"},
	} {
		t.Run(tt.expr, func(t *testing.T) {
			c, e := ParseCron(tt.expr)
			if e != nil {
				t.Fatal(e)
			}
			after, _ := time.Parse(time.RFC3339, tt.after)
			got, e := c.Next(after, time.UTC)
			if e != nil || got.Format(time.RFC3339) != tt.want {
				t.Fatalf("%s %v, want %s", got, e, tt.want)
			}
		})
	}
	c, _ := ParseCron("0 0 31 2 *")
	if _, e := c.Next(epoch, time.UTC); e == nil {
		t.Fatal("impossible date accepted")
	}
}

func TestDST(t *testing.T) {
	loc, e := time.LoadLocation("America/New_York")
	if e != nil {
		t.Fatal(e)
	}
	for _, tt := range []struct{ expr, after, want string }{
		{"30 2 * * *", "2026-03-08T06:00:00Z", "2026-03-09T06:30:00Z"},
		{"30 1 * * *", "2026-11-01T05:00:00Z", "2026-11-01T05:30:00Z"},
		{"30 1 * * *", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"},
		{"0 9 * * *", "2026-03-07T14:00:00Z", "2026-03-08T13:00:00Z"},
	} {
		c, _ := ParseCron(tt.expr)
		after, _ := time.Parse(time.RFC3339, tt.after)
		got, e := c.Next(after, loc)
		if e != nil || got.Format(time.RFC3339) != tt.want {
			t.Errorf("%s after %s: %s %v, want %s", tt.expr, tt.after, got, e, tt.want)
		}
	}
	// Samoa went over the date line after 29 December 2011: with the offset
	// of the 31st, 10:00 of the 29th would be a time of the 28th.
	apia, e := time.LoadLocation("Pacific/Apia")
	if e != nil {
		t.Fatal(e)
	}
	c, _ := ParseCron("0 10 29 * *")
	got, e := c.Next(time.Date(2011, 12, 28, 0, 0, 0, 0, time.UTC), apia)
	if want := time.Date(2011, 12, 29, 20, 0, 0, 0, time.UTC); e != nil || !got.Equal(want) {
		t.Errorf("the 29th in Samoa: %s %v, want %s", got, e, want)
	}
}

func TestMinimumInterval(t *testing.T) {
	for _, tt := range []struct {
		expr  string
		valid bool
	}{
		{"*/15 * * * *", true}, {"*/14 * * * *", false}, {"0,10 9 * * *", false},
		{"0,55 0,23 * * *", false}, {"0,55 0,23 1 * *", true},
		{"0 9 * * *", true}, {"0 */1 * * *", true}, {"10,50 0,23 * * *", true},
	} {
		c, e := ParseCron(tt.expr)
		if e != nil {
			t.Fatal(e)
		}
		if valid := c.validateInterval() == nil; valid != tt.valid {
			t.Errorf("interval %q: valid=%v", tt.expr, valid)
		}
	}
	loc, e := time.LoadLocation("Australia/Lord_Howe")
	if e != nil {
		t.Fatal(e)
	}
	c, _ := ParseCron("10,30,50 * * * *")
	if e = c.validateZone(loc, 2026); e == nil {
		t.Fatal("accepted 10-minute gap across half-hour DST shift")
	}
	c, _ = ParseCron("*/15 * * * *")
	if e = c.validateZone(loc, 2026); e != nil {
		t.Fatal(e)
	}
	// Samoa went over the date line after 29 December 2011: its 23:50 was
	// 09:50 UTC, and the midnight of the 31st came ten minutes later.
	apia, e := time.LoadLocation("Pacific/Apia")
	if e != nil {
		t.Fatal(e)
	}
	c, _ = ParseCron("0,50 0,23 29,31 * *")
	if e = c.validateInterval(); e != nil {
		t.Fatal(e)
	}
	if e = c.validateZone(apia, 2011); e == nil {
		t.Fatal("accepted a 10-minute gap across the date line")
	}
}
