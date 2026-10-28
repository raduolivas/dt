package domain

import (
	"encoding/json"
	"testing"
	"time"
)

// TestHiddenCourierFixDoesNotChangeViewerSnapshot: a fix for a courier the
// customer may not see changes the partner view but not one byte of the
// viewer snapshot (no updated_at bump that would leak the fix cadence).
func TestHiddenCourierFixDoesNotChangeViewerSnapshot(t *testing.T) {
	st := baseState(ModelDirect, StatusCourierAssigned) // hidden in Direct before pickup
	st.ViewerVersion = 7
	before, _ := json.Marshal(Snapshot(st, t0.Add(time.Minute)))
	pvBefore, _ := json.Marshal(PartnerView(st, t0.Add(time.Minute)))
	if !st.ApplyFix(Fix{T: t0.Add(30 * time.Second), Pos: LatLng{52.372, 4.899}}, t0.Add(time.Minute)) {
		t.Fatal("fix not applied")
	}
	if !st.UpdatedAt.Equal(t0) {
		t.Fatalf("UpdatedAt moved to %v on a hidden-courier fix", st.UpdatedAt)
	}
	after, _ := json.Marshal(Snapshot(st, t0.Add(time.Minute)))
	if string(before) != string(after) {
		t.Fatalf("viewer snapshot changed:\n%s\n%s", before, after)
	}
	pvAfter, _ := json.Marshal(PartnerView(st, t0.Add(time.Minute)))
	if string(pvBefore) == string(pvAfter) {
		t.Fatal("partner view should show the new position")
	}

	vis := baseState(ModelEndToEnd, StatusCourierAssigned)
	vis.ApplyFix(Fix{T: t0.Add(30 * time.Second), Pos: LatLng{52.372, 4.899}}, t0.Add(time.Minute))
	if !vis.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("visible courier fix: UpdatedAt=%v", vis.UpdatedAt)
	}
}

func TestSnapshotCarriesViewerVersion(t *testing.T) {
	st := baseState(ModelEndToEnd, StatusPickedUp)
	st.Version, st.ViewerVersion = 9, 4
	if v := Snapshot(st, t0).Version; v != 4 {
		t.Fatalf("snapshot version=%d want ViewerVersion 4", v)
	}
	if v := PartnerView(st, t0).Version; v != 9 {
		t.Fatalf("partner view version=%d want 9", v)
	}
}

// TestImpliedSpeedIgnoresSubTwoSecondJitter: GPS jitter between fixes less
// than 2 s apart implies absurd speeds; only the 1 km jump rule applies.
func TestImpliedSpeedIgnoresSubTwoSecondJitter(t *testing.T) {
	prev := &Fix{T: t0, Pos: LatLng{52.37, 4.89}}
	// ~33 m in 0.5 s = 240 km/h implied: kept.
	jitter := Fix{T: t0.Add(500 * time.Millisecond), Pos: LatLng{52.3703, 4.89}}
	if v, err := CheckFix(prev, jitter, jitter.T); err != nil || v.Action != Accept {
		t.Fatalf("jitter: %+v %v", v, err)
	}
	// 1.1 km in 1 s: still a jump.
	jump := Fix{T: t0.Add(time.Second), Pos: LatLng{52.38, 4.89}}
	if v, _ := CheckFix(prev, jump, jump.T); v.Action != Drop || v.Reason != "jump" {
		t.Fatalf("jump: %+v", v)
	}
	// ~556 m in 5 s = 400 km/h: dropped for speed.
	fast := Fix{T: t0.Add(5 * time.Second), Pos: LatLng{52.375, 4.89}}
	if v, _ := CheckFix(prev, fast, fast.T); v.Action != Drop || v.Reason != "speed" {
		t.Fatalf("fast: %+v", v)
	}
	// exactly 2 s apart: checked.
	two := Fix{T: t0.Add(2 * time.Second), Pos: LatLng{52.3735, 4.89}} // ~390 m = 700 km/h
	if v, _ := CheckFix(prev, two, two.T); v.Action != Drop || v.Reason != "speed" {
		t.Fatalf("2 s: %+v", v)
	}
}
