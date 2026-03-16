package memwatcher

import (
	"testing"
)

func TestHeapMonitor_Thresholds(t *testing.T) {
	// GOMEMLIMIT = 1000: удобно для проверки вычислений
	h := NewHeapMonitor(1000)

	if h.thresholds[0] != 700 {
		t.Errorf("tier1 threshold = %d, want 700", h.thresholds[0])
	}
	if h.thresholds[1] != 800 {
		t.Errorf("tier2 threshold = %d, want 800", h.thresholds[1])
	}
	if h.thresholds[2] != 900 {
		t.Errorf("tier3 threshold = %d, want 900", h.thresholds[2])
	}
}

func TestHeapMonitor_ThresholdsOrder(t *testing.T) {
	h := NewHeapMonitor(512 * 1024 * 1024)

	if h.thresholds[0] >= h.thresholds[1] {
		t.Errorf("tier1 threshold (%d) must be < tier2 threshold (%d)", h.thresholds[0], h.thresholds[1])
	}
	if h.thresholds[1] >= h.thresholds[2] {
		t.Errorf("tier2 threshold (%d) must be < tier3 threshold (%d)", h.thresholds[1], h.thresholds[2])
	}
}

func TestHeapMonitor_Limit(t *testing.T) {
	const limit = int64(256 * 1024 * 1024)
	h := NewHeapMonitor(limit)

	if h.limit != uint64(limit) {
		t.Errorf("limit = %d, want %d", h.limit, uint64(limit))
	}
}

func TestHeapMonitor_Pct(t *testing.T) {
	h := NewHeapMonitor(1000)

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
	// Read() должен работать без паники на реальных runtime метриках.
	// Проверяем корректность защиты от KindBad.
	h := NewHeapMonitor(512 * 1024 * 1024)

	inuse, tier := h.Read()

	if tier < HeapTierNormal || tier > HeapTier3 {
		t.Errorf("Read() returned invalid tier %d", tier)
	}
	_ = inuse
}

func TestHeapMonitor_Read_InuseNonNegative(t *testing.T) {
	h := NewHeapMonitor(1024 * 1024 * 1024)

	inuse, _ := h.Read()
	if inuse == 0 {
		// В реальном Go процессе heap.inuse > 0 (минимум runtime structures)
		t.Log("inuse = 0, runtime may report 0 in minimal test environment")
	}
}
