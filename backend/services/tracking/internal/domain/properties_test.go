package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestSnapshotNeverLeaksHiddenCourier(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		m := rapid.SampledFrom(allModels).Draw(rt, "model")
		s := rapid.SampledFrom(allStatuses).Draw(rt, "status")
		if CourierVisible(m, s) {
			rt.Skip("visible")
		}
		// Courier values are distinctive so a substring match cannot be a coincidence.
		clat := rapid.Float64Range(10, 20).Draw(rt, "clat")
		clng := rapid.Float64Range(100, 110).Draw(rt, "clng")
		st := baseState(m, s)
		st.Courier = &Courier{ID: "cou_secretid", ExternalID: "ext-SECRET", DisplayName: "Zaphod-SECRET", Vehicle: "car"}
		hp := LatLng{clat + 0.1234567, clng + 0.7654321} // 7 decimals: no coincidental substring
		st.LastFix = &Fix{T: t0, Pos: hp, HeadingDeg: f64p(77.123456), SpeedMps: f64p(8.7654321)}
		st.Route.ToPickup = []LatLng{hp, st.Pickup}
		now := t0.Add(time.Duration(rapid.IntRange(0, 900).Draw(rt, "age")) * time.Second)

		snap := Snapshot(st, now)
		if snap.Courier != nil {
			rt.Fatal("courier must be nil")
		}
		b, err := json.Marshal(snap)
		if err != nil {
			rt.Fatal(err)
		}
		js := string(b)
		for _, bad := range []string{"SECRET", "cou_secretid", "77.123456", "8.7654321", "heading_deg", "speed_kmh"} {
			if strings.Contains(js, bad) {
				rt.Fatalf("leak %q in %s", bad, js)
			}
		}
		var generic map[string]any
		if err := json.Unmarshal(b, &generic); err != nil {
			rt.Fatal(err)
		}
		if v, ok := generic["courier"]; !ok || v != nil {
			rt.Fatalf("courier must be null: %v", v)
		}
		coords := []string{}
		coords = append(coords, jsonNum(hp.Lat), jsonNum(hp.Lng))
		for _, c := range coords {
			if strings.Contains(js, c) {
				rt.Fatalf("coordinate %s leaked in %s", c, js)
			}
		}
	})
}

func jsonNum(f float64) string { b, _ := json.Marshal(f); return string(b) }

func TestNextVersionMonotonic(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		prev := rapid.Int64Range(0, 1<<60).Draw(rt, "prev")
		ms := rapid.Int64Range(0, 1<<50).Draw(rt, "clock")
		if got := NextVersion(prev, time.UnixMilli(ms)); got <= prev {
			rt.Fatalf("NextVersion(%d, %d)=%d", prev, ms, got)
		}
	})
}

func TestPropertySimplifyTolerance(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(2, 40).Draw(rt, "n")
		pts := make([]LatLng, n)
		for i := range pts {
			pts[i] = LatLng{rapid.Float64Range(52, 52.05).Draw(rt, "lat"), rapid.Float64Range(4, 4.05).Draw(rt, "lng")}
		}
		tol := rapid.Float64Range(1, 200).Draw(rt, "tol")
		out := Simplify(pts, tol)
		if out[0] != pts[0] || out[len(out)-1] != pts[n-1] {
			rt.Fatal("endpoints")
		}
		assertWithin(t, pts, out, tol*1.02+0.5)
	})
}

func TestPropertyOlderFixIgnored(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		st := baseState(ModelDirect, StatusPickedUp)
		off := time.Duration(rapid.IntRange(-600, 0).Draw(rt, "off")) * time.Second
		before := *st.LastFix
		if st.ApplyFix(Fix{T: st.LastFix.T.Add(off), Pos: LatLng{1, 1}}, t0) || *st.LastFix != before {
			rt.Fatal("older/equal fix moved the position")
		}
	})
}
