package memwatcher_test

import (
	"testing"

	"github.com/billz-2/memwatcher"
)

func TestHeapMonitor_Thresholds(t *testing.T) {
	// GOMEMLIMIT = 1000: удобно для проверки вычислений с дефолтными порогами 70/80/90
	h := memwatcher.NewHeapMonitor(1000, 70, 80, 90)

	th := h.Thresholds() // тест-хелпер из export_test.go
	if th[0] != 700 {
		t.Errorf("tier1 threshold = %d, want 700", th[0])
	}
	if th[1] != 800 {
		t.Errorf("tier2 threshold = %d, want 800", th[1])
	}
	if th[2] != 900 {
		t.Errorf("tier3 threshold = %d, want 900", th[2])
	}
}

func TestHeapMonitor_CustomThresholds(t *testing.T) {
	h := memwatcher.NewHeapMonitor(1000, 60, 75, 85)
	th := h.Thresholds()

	if th[0] != 600 {
		t.Errorf("tier1 = %d, want 600", th[0])
	}
	if th[1] != 750 {
		t.Errorf("tier2 = %d, want 750", th[1])
	}
	if th[2] != 850 {
		t.Errorf("tier3 = %d, want 850", th[2])
	}
}

func TestHeapMonitor_ThresholdsOrder(t *testing.T) {
	h := memwatcher.NewHeapMonitor(512*1024*1024, 70, 80, 90)
	th := h.Thresholds()

	if th[0] >= th[1] {
		t.Errorf("tier1 threshold (%d) must be < tier2 threshold (%d)", th[0], th[1])
	}
	if th[1] >= th[2] {
		t.Errorf("tier2 threshold (%d) must be < tier3 threshold (%d)", th[1], th[2])
	}
}

func TestHeapMonitor_Limit(t *testing.T) {
	const limit = int64(256 * 1024 * 1024)
	h := memwatcher.NewHeapMonitor(limit, 70, 80, 90)

	if got := h.Limit(); got != uint64(limit) { // тест-хелпер из export_test.go
		t.Errorf("limit = %d, want %d", got, uint64(limit))
	}
}

func TestHeapMonitor_Pct(t *testing.T) {
	h := memwatcher.NewHeapMonitor(1000, 70, 80, 90)

	cases := []struct {
		inuse uint64
		want  float64
	}{
		{0, 0.0},
		{250, 25.0},
		{500, 50.0},
		{1000, 100.0},
	}

	for _, tc := range cases {
		got := h.Pct(tc.inuse)
		if got != tc.want {
			t.Errorf("Pct(%d) = %f, want %f", tc.inuse, got, tc.want)
		}
	}
}

func TestHeapMonitor_Read_DoesNotPanic(t *testing.T) {
	h := memwatcher.NewHeapMonitor(512*1024*1024, 70, 80, 90)

	inuse, tier := h.Read()

	if tier < memwatcher.HeapTierNormal || tier > memwatcher.HeapTier3 {
		t.Errorf("Read() returned invalid tier %d", tier)
	}
	_ = inuse
}

func TestHeapMonitor_Read_InuseNonNegative(t *testing.T) {
	h := memwatcher.NewHeapMonitor(1024*1024*1024, 70, 80, 90)

	inuse, _ := h.Read()
	if inuse == 0 {
		t.Log("inuse = 0, runtime may report 0 in minimal test environment")
	}
}
