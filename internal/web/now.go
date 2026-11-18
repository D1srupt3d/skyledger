package web

import "time"

// statusLevel maps the age in seconds of the newest message to the receiver's
// status; no message at all is silent.
func statusLevel(age *float64) string {
	switch {
	case age == nil || *age >= 900:
		return "silent"
	case *age >= 120:
		return "stale"
	}
	return "live"
}

// localMidnight is the start of now's calendar day in tz (23 or 25 hours long on DST days).
func localMidnight(now time.Time, tz *time.Location) time.Time {
	y, m, d := now.In(tz).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, tz)
}

// bucketTimes lists the Unix start of every step-sized bucket from from through to,
// aligned like SQL time_bucket (its default origin is a multiple of 5 minutes and an hour).
func bucketTimes(from, to time.Time, step time.Duration) []int64 {
	s := int64(step / time.Second)
	var out []int64
	for b := from.Unix() / s * s; b <= to.Unix(); b += s {
		out = append(out, b)
	}
	return out
}
