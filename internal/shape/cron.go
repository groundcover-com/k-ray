package shape

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A small reader for the five-field schedule of a CronJob. It answers two
// questions the tools need: is the schedule valid and can it ever fire (a day
// that does not exist in the month never does), and when does it fire next.
// It is not a scheduler and it does not know time zones.

type cronField struct {
	set  map[int]bool
	star bool // the field starts with "*" (or "?"): it does not restrict
}

// Cron is a parsed schedule.
type Cron struct {
	min, hour, dom, month, dow cronField
	every                      time.Duration // "@every 1h30m": a fixed interval, no fields
}

var cronMacros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dayNames   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
)

// ParseCron reads a five-field schedule or an @macro.
func ParseCron(spec string) (*Cron, error) {
	s := strings.TrimSpace(spec)
	for _, p := range []string{"CRON_TZ=", "TZ="} {
		if strings.HasPrefix(s, p) {
			_, rest, _ := strings.Cut(s, " ")
			s = strings.TrimSpace(rest)
		}
	}
	if m, ok := cronMacros[strings.ToLower(s)]; ok {
		s = m
	}
	if rest, ok := strings.CutPrefix(strings.ToLower(s), "@every"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil || d < time.Second {
			return nil, fmt.Errorf("@every needs a duration of at least 1s, such as 1h30m")
		}
		return &Cron{every: d}, nil
	}
	f := strings.Fields(s)
	if len(f) != 5 {
		return nil, fmt.Errorf("it has %d fields, not 5 (minute hour day-of-month month day-of-week)", len(f))
	}
	var c Cron
	var err error
	parse := func(dst *cronField, text, name string, lo, hi int, names map[string]int) {
		if err != nil {
			return
		}
		*dst, err = parseCronField(text, lo, hi, names)
		if err != nil {
			err = fmt.Errorf("%s field %q: %w", name, text, err)
		}
	}
	parse(&c.min, f[0], "minute", 0, 59, nil)
	parse(&c.hour, f[1], "hour", 0, 23, nil)
	parse(&c.dom, f[2], "day-of-month", 1, 31, nil)
	parse(&c.month, f[3], "month", 1, 12, monthNames)
	parse(&c.dow, f[4], "day-of-week", 0, 7, dayNames)
	if err != nil {
		return nil, err
	}
	if c.dow.set[7] { // 7 is Sunday too
		c.dow.set[0] = true
		delete(c.dow.set, 7)
	}
	return &c, nil
}

func parseCronField(text string, lo, hi int, names map[string]int) (cronField, error) {
	out := cronField{set: map[int]bool{}, star: strings.HasPrefix(text, "*") || strings.HasPrefix(text, "?")}
	num := func(s string) (int, error) {
		if n, ok := names[strings.ToLower(s)]; ok {
			return n, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", s)
		}
		if n < lo || n > hi {
			return 0, fmt.Errorf("%d is outside %d-%d", n, lo, hi)
		}
		return n, nil
	}
	for _, part := range strings.Split(text, ",") {
		rng, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return out, fmt.Errorf("the step %q is not a positive number", stepText)
			}
			step = n
		}
		a, b := lo, hi
		switch {
		case rng == "*" || rng == "?":
		case strings.Contains(rng, "-"):
			x, y, _ := strings.Cut(rng, "-")
			var err error
			if a, err = num(x); err != nil {
				return out, err
			}
			if b, err = num(y); err != nil {
				return out, err
			}
			if a > b {
				return out, fmt.Errorf("the range %q runs backwards", rng)
			}
		default:
			var err error
			if a, err = num(rng); err != nil {
				return out, err
			}
			if !hasStep {
				b = a
			}
		}
		for v := a; v <= b; v += step {
			out.set[v] = true
		}
	}
	return out, nil
}

// daysIn is the most days a month can have. February has 29: it is possible
// in a leap year.
var daysIn = [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// CanFire reports whether the schedule names a day that exists. A schedule
// such as "0 0 31 2 *" is valid and never fires.
func (c *Cron) CanFire() bool {
	if c.every > 0 {
		return true
	}
	if !c.dom.star && c.dow.star { // only the day of the month limits the date
		for m := range c.month.set {
			for d := range c.dom.set {
				if d <= daysIn[m] {
					return true
				}
			}
		}
		return false
	}
	return len(c.month.set) > 0
}

func (c *Cron) dayMatches(t time.Time) bool {
	if !c.month.set[int(t.Month())] {
		return false
	}
	domOK, dowOK := c.dom.set[t.Day()], c.dow.set[int(t.Weekday())]
	if c.dom.star || c.dow.star {
		return domOK && dowOK
	}
	return domOK || dowOK
}

// Next is the first time after t at which the schedule fires, in the zone of
// t. It returns false when there is none within about eight years.
func (c *Cron) Next(t time.Time) (time.Time, bool) {
	if c.every > 0 {
		return t.Add(c.every), true
	}
	t = t.Truncate(time.Minute).Add(time.Minute)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	for i := 0; i < 366*8; i++ {
		d := day.AddDate(0, 0, i)
		if !c.dayMatches(d) {
			continue
		}
		for h := 0; h < 24; h++ {
			if !c.hour.set[h] {
				continue
			}
			for m := 0; m < 60; m++ {
				if !c.min.set[m] {
					continue
				}
				at := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, t.Location())
				if !at.Before(t) {
					return at, true
				}
			}
		}
	}
	return time.Time{}, false
}
