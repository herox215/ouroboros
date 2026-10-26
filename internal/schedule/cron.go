package schedule

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field numeric cron expression (minute, hour, day of
// month, month, weekday). Lists, inclusive ranges and positive steps are allowed;
// Sunday is 0 or 7. Restricted day-of-month and weekday fields use cron's OR
// rule; a wildcard field uses the other field. Next follows real instants:
// nonexistent DST wall times are skipped and repeated wall times occur twice.
type Cron struct {
	fields [5]map[int]bool
	wild   [5]bool
}

// ParseCron parses syntax only. Store additionally requires possible dates and
// a minimum of 15 minutes between occurrences. Names and macros are unsupported.
func ParseCron(expr string) (*Cron, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, errors.New("cron needs five fields")
	}
	c := &Cron{}
	mins := []int{0, 0, 1, 1, 0}
	maxs := []int{59, 23, 31, 12, 7}
	for i, p := range parts {
		c.fields[i] = map[int]bool{}
		c.wild[i] = strings.HasPrefix(p, "*")
		for _, piece := range strings.Split(p, ",") {
			base, stepText, hasStep := strings.Cut(piece, "/")
			step := 1
			var err error
			if hasStep {
				step, err = strconv.Atoi(stepText)
				if err != nil || step < 1 {
					return nil, fmt.Errorf("invalid cron step %q", piece)
				}
			}
			lo, hi := mins[i], maxs[i]
			if base != "*" {
				a, b, ranged := strings.Cut(base, "-")
				lo, err = strconv.Atoi(a)
				if err != nil {
					return nil, fmt.Errorf("invalid cron field %q", p)
				}
				hi = lo
				if ranged {
					hi, err = strconv.Atoi(b)
					if err != nil {
						return nil, fmt.Errorf("invalid cron range %q", piece)
					}
				} else if hasStep {
					hi = maxs[i]
				}
			}
			if lo < mins[i] || hi > maxs[i] || hi < lo {
				return nil, fmt.Errorf("cron field out of range: %q", p)
			}
			for n := lo; n <= hi; {
				v := n
				if i == 4 && n == 7 {
					v = 0
				}
				c.fields[i][v] = true
				if step > hi-n {
					break
				}
				n += step
			}
		}
	}
	return c, nil
}

func (c *Cron) day(d time.Time) bool {
	if !c.fields[3][int(d.Month())] {
		return false
	}
	dom, dow := c.fields[2][d.Day()], c.fields[4][int(d.Weekday())]
	if c.wild[2] || c.wild[4] {
		return dom && dow
	}
	return dom || dow
}

func (c *Cron) minutes() []int {
	var out []int
	for h := 0; h < 24; h++ {
		if !c.fields[1][h] {
			continue
		}
		for m := 0; m < 60; m++ {
			if c.fields[0][m] {
				out = append(out, h*60+m)
			}
		}
	}
	return out
}

// instants resolves wall times using the offsets on either side of the date.
// Verification rejects normalized nonexistent times and captures both sides of
// a fold, including half-hour DST shifts and historical non-hour offsets.
func instants(d time.Time, minutes []int, loc *time.Location) []time.Time {
	offsets := map[int]bool{}
	for h := -48; h <= 48; h += 6 {
		_, off := d.Add(time.Duration(h) * time.Hour).In(loc).Zone()
		offsets[off] = true
	}
	var out []time.Time
	for _, m := range minutes {
		wall := d.Add(time.Duration(m) * time.Minute)
		for off := range offsets {
			t := wall.Add(-time.Duration(off) * time.Second)
			v := t.In(loc)
			if v.Year() == d.Year() && v.Month() == d.Month() && v.Day() == d.Day() && v.Hour() == m/60 && v.Minute() == m%60 {
				out = append(out, t)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// Next returns the first occurrence strictly after after, in loc. Searching a
// full 400-year Gregorian cycle rejects impossible dates such as 31 February.
func (c *Cron) Next(after time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.Local
	}
	v := after.In(loc)
	d := time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.UTC)
	end := d.AddDate(400, 0, 1)
	minutes := c.minutes()
	for ; d.Before(end); d = d.AddDate(0, 0, 1) {
		if !c.day(d) {
			continue
		}
		for _, t := range instants(d, minutes, loc) {
			if t.After(after) {
				return t, nil
			}
		}
	}
	return time.Time{}, errors.New("cron has no possible occurrence")
}

func (c *Cron) validateInterval() error {
	mins := c.minutes()
	for i := 1; i < len(mins); i++ {
		if mins[i]-mins[i-1] < 15 {
			return errors.New("cron runs must be at least 15 minutes apart")
		}
	}
	// Only adjacent selected dates can make the midnight gap too short. Use a
	// complete Gregorian cycle rather than rejecting sparse monthly schedules.
	if 1440+mins[0]-mins[len(mins)-1] >= 15 {
		return nil
	}
	d := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := false
	for n := 0; n <= 146097; n++ {
		selected := c.day(d)
		if selected && prev {
			return errors.New("cron runs must be at least 15 minutes apart")
		}
		prev = selected
		d = d.AddDate(0, 0, 1)
	}
	return nil
}

// Check actual gaps around offset transitions too: a half-hour spring jump
// can shorten an otherwise legal twenty-minute wall-clock recurrence.
func (c *Cron) validateZone(loc *time.Location, year int) error {
	if loc == time.UTC {
		return nil
	}
	d := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	minutes := c.minutes()
	for n := 0; n < 146097; n++ {
		_, a := d.Add(-24 * time.Hour).In(loc).Zone()
		_, b := d.Add(24 * time.Hour).In(loc).Zone()
		if a != b {
			var ts []time.Time
			for k := -1; k <= 1; k++ {
				day := d.AddDate(0, 0, k)
				if c.day(day) {
					ts = append(ts, instants(day, minutes, loc)...)
				}
			}
			sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
			for i := 1; i < len(ts); i++ {
				if ts[i].Sub(ts[i-1]) < 15*time.Minute {
					return errors.New("cron runs must be at least 15 minutes apart across zone transitions")
				}
			}
		}
		d = d.AddDate(0, 0, 1)
	}
	return nil
}
