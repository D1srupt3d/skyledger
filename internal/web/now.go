package web

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/D1srupt3d/skyledger/internal/geo"
	"github.com/D1srupt3d/skyledger/internal/queries"
)

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
	out := []int64{} // [] in JSON, never null
	for b := from.Unix() / s * s; b <= to.Unix(); b += s {
		out = append(out, b)
	}
	return out
}

// Real columns scan as float32, so JSON shows 15.3, not 15.300000190734863.

type aircraft struct {
	Hex          string       `json:"hex"`
	Callsign     *string      `json:"callsign"`
	Registration *string      `json:"registration"`
	Type         *string      `json:"type"`
	Operator     *string      `json:"operator"`
	Military     bool         `json:"military"`
	Alt          *int         `json:"alt"`
	OnGround     bool         `json:"on_ground"`
	GS           *float32     `json:"gs"`
	Track        *float32     `json:"track"`
	DistanceNM   *float32     `json:"distance_nm"`
	Bearing      *float32     `json:"bearing"`
	Squawk       *string      `json:"squawk"`
	Emergency    *string      `json:"emergency"`
	Seen         time.Time    `json:"seen"`
	Trail        [][2]float32 `json:"trail"` // [bearing, distance_nm], oldest first
}

type notable struct {
	Kind       string     `json:"kind"` // emergency, military or lowest
	Hex        string     `json:"hex"`
	Callsign   *string    `json:"callsign"`
	Type       *string    `json:"type"`
	Alt        *int       `json:"alt"`
	DistanceNM *float32   `json:"distance_nm"`
	Bearing    *float32   `json:"bearing"`
	Squawk     *string    `json:"squawk"`
	Emergency  *string    `json:"emergency"`
	LastHeard  *time.Time `json:"last_heard"` // military only: the latest flight over an hour ago
}

func (a *aircraft) notable(kind string) notable {
	return notable{Kind: kind, Hex: a.Hex, Callsign: a.Callsign, Type: a.Type, Alt: a.Alt,
		DistanceNM: a.DistanceNM, Bearing: a.Bearing, Squawk: a.Squawk, Emergency: a.Emergency}
}

type live struct {
	Status struct {
		Level string   `json:"level"`
		AgeS  *float64 `json:"age_s"`
	} `json:"status"`
	Counts struct {
		WithPosition    int64  `json:"with_position"`
		WithoutPosition *int64 `json:"without_position"`
	} `json:"counts"`
	Aircraft []*aircraft `json:"aircraft"`
	Notable  []notable   `json:"notable"`
}

// panelRow runs a one-row dashboard panel query over from..to into dest.
func (a *api) panelRow(ctx context.Context, name string, from, to time.Time, dest ...any) error {
	sql, args, err := queries.Expand(queries.Panel[name], from, to, "")
	if err != nil {
		return err
	}
	return a.db.QueryRow(ctx, sql, args...).Scan(dest...)
}

func (a *api) live(ctx context.Context) (any, error) {
	now := time.Now()
	var out live
	if err := a.panelRow(ctx, "last_write", now, now, &out.Status.AgeS); err != nil {
		return nil, err
	}
	out.Status.Level = statusLevel(out.Status.AgeS)
	if err := a.panelRow(ctx, "now", now, now, &out.Counts.WithPosition); err != nil {
		return nil, err
	}
	err := a.db.QueryRow(ctx, queries.WithoutPosition, now.Add(-2*time.Minute)).Scan(&out.Counts.WithoutPosition)
	if err != nil {
		return nil, err
	}

	rows, err := a.db.Query(ctx, queries.LiveAircraft, now.Add(-time.Minute))
	if err != nil {
		return nil, err
	}
	out.Aircraft, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (*aircraft, error) {
		ac := &aircraft{Trail: [][2]float32{}}
		err := r.Scan(&ac.Hex, &ac.Callsign, &ac.Registration, &ac.Type, &ac.Operator, &ac.Military,
			&ac.Alt, &ac.OnGround, &ac.GS, &ac.Track, &ac.DistanceNM, &ac.Bearing, &ac.Squawk, &ac.Emergency, &ac.Seen)
		ac.Seen = ac.Seen.UTC()
		return ac, err
	})
	if err != nil {
		return nil, err
	}
	byHex := map[string]*aircraft{}
	for _, ac := range out.Aircraft {
		byHex[ac.Hex] = ac
	}

	rows, err = a.db.Query(ctx, queries.LiveTrails, now.Add(-5*time.Minute))
	if err != nil {
		return nil, err
	}
	var hex string
	var pt [2]float32
	if _, err := pgx.ForEachRow(rows, []any{&hex, &pt[0], &pt[1]}, func() error {
		if ac := byHex[hex]; ac != nil {
			ac.Trail = append(ac.Trail, pt)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	out.Notable = []notable{}
	listed := map[string]bool{}
	for _, ac := range out.Aircraft {
		if ac.Emergency != nil || (ac.Squawk != nil && (*ac.Squawk == "7500" || *ac.Squawk == "7600" || *ac.Squawk == "7700")) {
			out.Notable = append(out.Notable, ac.notable("emergency"))
			listed[ac.Hex] = true
		}
	}
	var military []string
	for _, ac := range out.Aircraft {
		if ac.Military {
			military = append(military, ac.Hex)
		}
	}
	heard := map[string]time.Time{}
	var ready bool // flights is empty and unreadable until history's first refresh: last_heard stays null
	if len(military) > 0 {
		if err := a.db.QueryRow(ctx, queries.FlightsReady).Scan(&ready); err != nil {
			return nil, err
		}
	}
	if ready {
		rows, err := a.db.Query(ctx, queries.LastHeard, military, now.Add(-time.Hour))
		if err != nil {
			return nil, err
		}
		var at time.Time
		if _, err := pgx.ForEachRow(rows, []any{&hex, &at}, func() error {
			heard[hex] = at.UTC()
			return nil
		}); err != nil {
			return nil, err
		}
	}
	for _, h := range military {
		n := byHex[h].notable("military")
		if at, ok := heard[h]; ok {
			n.LastHeard = &at
		}
		out.Notable = append(out.Notable, n)
		listed[h] = true
	}
	var lowest *aircraft
	for _, ac := range out.Aircraft {
		if !ac.OnGround && ac.Alt != nil && (lowest == nil || *ac.Alt < *lowest.Alt) {
			lowest = ac
		}
	}
	if lowest != nil && !listed[lowest.Hex] {
		out.Notable = append(out.Notable, lowest.notable("lowest"))
	}
	return out, nil
}

type far struct {
	NM  float32 `json:"nm"`
	Deg int     `json:"deg"`
}

type farEver struct {
	far
	Time time.Time `json:"time"`
}

// outline reads an outline panel's rows into one distance per degree and the furthest point.
func (a *api) outline(ctx context.Context, name string, from, to time.Time) ([]*float32, *farEver, error) {
	sql, args, err := queries.Expand(queries.Panel[name], from, to, "")
	if err != nil {
		return nil, nil, err
	}
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, err
	}
	dist := make([]*float32, 360)
	var best *farEver
	var deg int
	var nm float32
	var at time.Time
	_, err = pgx.ForEachRow(rows, []any{&deg, nil, nil, &nm, nil, nil, &at}, func() error {
		if deg < 0 || deg >= 360 {
			return nil // the row repeated at 360 to close the loop
		}
		d := nm
		dist[deg] = &d
		if best == nil || nm > best.NM {
			best = &farEver{far{nm, deg}, at.UTC()}
		}
		return nil
	})
	return dist, best, err
}

type outlines struct {
	AllTime       []*float32 `json:"all_time"`
	Today         []*float32 `json:"today"`
	FurthestToday *far       `json:"furthest_today"`
	FurthestEver  *farEver   `json:"furthest_ever"`
}

func (a *api) outlines(ctx context.Context) (any, error) {
	now := time.Now()
	var out outlines
	var err error
	if out.AllTime, out.FurthestEver, err = a.outline(ctx, "outline_all", now, now); err != nil {
		return nil, err
	}
	// ponytail: computed over today's live positions every 60 s; precompute it in history if a Pi 4 takes over 500 ms.
	today, best, err := a.outline(ctx, "outline_range", localMidnight(now, a.tz), now)
	if err != nil {
		return nil, err
	}
	out.Today = today
	if best != nil {
		out.FurthestToday = &best.far
		if out.FurthestEver == nil || best.NM > out.FurthestEver.NM { // live data the nightly outline hasn't seen
			out.FurthestEver = best
		}
	}
	return out, nil
}

type tracked struct {
	StepS    int      `json:"step_s"`
	T        []int64  `json:"t"`
	Aircraft []*int64 `json:"aircraft"`
}

type altitude struct {
	StepS    int      `json:"step_s"`
	T        []int64  `json:"t"`
	Ground   []*int64 `json:"ground"`
	Below10k []*int64 `json:"below_10k"`
	Mid      []*int64 `json:"10k_30k"`
	High     []*int64 `json:"30k_up"`
}

type traffic struct {
	Tracked   tracked  `json:"tracked"`
	Altitude  altitude `json:"altitude"`
	Today     int64    `json:"aircraft_today"`
	Yesterday *int64   `json:"aircraft_yesterday"`
}

// series runs a grouped panel over from..to in step buckets: one column of
// counts per series, null where a bucket has no rows (Expand doesn't fill them).
func (a *api) series(ctx context.Context, name string, from, to time.Time, step time.Duration, bucket string, n int) ([]int64, [][]*int64, error) {
	t := bucketTimes(from, to, step)
	cols := make([][]*int64, n)
	for i := range cols {
		cols[i] = make([]*int64, len(t))
	}
	sql, args, err := queries.Expand(queries.Panel[name], from, to, bucket)
	if err != nil {
		return nil, nil, err
	}
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, err
	}
	index := make(map[int64]int, len(t))
	for i, b := range t {
		index[b] = i
	}
	var at time.Time
	vals := make([]int64, n)
	dest := []any{&at}
	for i := range vals {
		dest = append(dest, &vals[i])
	}
	_, err = pgx.ForEachRow(rows, dest, func() error {
		if i, ok := index[at.Unix()]; ok {
			for c, v := range vals {
				cols[c][i] = &v
			}
		}
		return nil
	})
	return t, cols, err
}

func (a *api) traffic(ctx context.Context) (any, error) {
	now := time.Now()
	from := now.Add(-24 * time.Hour)
	var out traffic
	t, cols, err := a.series(ctx, "tracked", from, now, 5*time.Minute, "5 minutes", 1)
	if err != nil {
		return nil, err
	}
	out.Tracked = tracked{300, t, cols[0]}
	if t, cols, err = a.series(ctx, "altitude", from, now, time.Hour, "1 hour", 4); err != nil {
		return nil, err
	}
	out.Altitude = altitude{3600, t, cols[0], cols[1], cols[2], cols[3]}
	midnight := localMidnight(now, a.tz)
	if err := a.panelRow(ctx, "aircraft_range", midnight, now, &out.Today); err != nil {
		return nil, err
	}
	yesterday := midnight.AddDate(0, 0, -1).Format(time.DateOnly)
	if err := a.db.QueryRow(ctx, queries.AircraftOnDay, yesterday).Scan(&out.Yesterday); err != nil {
		return nil, err
	}
	return out, nil
}

// mapData reads the receiver location each call (cheap) and runs geo.Build
// (seconds on a Pi) only when it differs from the one the kept map was built for.
func (a *api) mapData(ctx context.Context) (any, error) {
	var lat, lon *float64
	if err := a.db.QueryRow(ctx, queries.ReceiverLocation).Scan(&lat, &lon); err != nil {
		return nil, err
	}
	if lat == nil || lon == nil {
		return geo.Map{Land: [][][2]float64{}, Borders: [][][2]float64{}, Cities: []geo.City{}}, nil
	}
	a.mapMu.Lock()
	defer a.mapMu.Unlock()
	if at := [2]float64{*lat, *lon}; a.mapped == nil || at != a.mapAt {
		m, err := geo.Build(*lat, *lon)
		if err != nil {
			return nil, err
		}
		a.mapped, a.mapAt = &m, at
	}
	return a.mapped, nil
}
