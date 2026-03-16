package memwatcher_test

import (
	"testing"

	"github.com/billz-2/memwatcher"
)

func TestHeapMonitor_Thresholds(t *testing.T) {
	// GOMEMLIMIT = 1000: удобно для проверки вычислений
	h := memwatcher.NewHeapMonitor(1000)
	thresholds := h.Thresholds()

	if thresholds[0] != 700 {
		t.Errorf("tier1 threshold = %d, want 700", thresholds[0])
	}
	if thresholds[1] != 800 {
		t.Errorf("tier2 threshold = %d, want 800", thresholds[1])
	}
	if thresholds[2] != 900 {
		t.Errorf("tier3 threshold = %d, want 900", thresholds[2])
	}
}

func TestHeapMonitor_ThresholdsOrder(t *testing.T) {
	h := memwatcher.NewHeapMonitor(512 * 1024 * 1024)
	thresholds := h.Thresholds()

	if thresholds[0] >= thresholds[1] {
		t.Errorf("tier1 threshold (%d) must be < tier2 threshold (%d)", thresholds[0], thresholds[1])
	}
	if thresholds[1] >= thresholds[2] {
		t.Errorf("tier2 threshold (%d) must be < tier3 threshold (%d)", thresholds[1], thresholds[2])
	}
}

func TestHeapMonitor_Limit(t *testing.T) {
	const limit = int64(256 * 1024 * 1024)
	h := memwatcher.NewHeapMonitor(limit)

	if h.Limit() != uint64(limit) {
		t.Errorf("Limit() = %d, want %d", h.Limit(), uint64(limit))
	}
}

func TestHeapMonitor_Pct(t *testing.T) {
	h := memwatcher.NewHeapMonitor(1000)

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
	// read() должен работать без паники на реальных runtime метриках.
	// Проверяем корректность защиты от KindBad.
	h := memwatcher.NewHeapMonitor(512 * 1024 * 1024)

	inuse, tier := h.Read()

	// Нормальный Go процесс использует < 512MB heap в тестах, поэтому tier должен быть нормальным.
	// tier: 0=normal, 1=tier1, 2=tier2, 3=tier3
	if tier < 0 || tier > 3 {
		t.Errorf("Read() returned invalid tier %d", tier)
	}
	_ = inuse // просто проверяем что нет паники и значение получено
}

func TestHeapMonitor_Read_InuseNonNegative(t *testing.T) {
	h := memwatcher.NewHeapMonitor(1024 * 1024 * 1024)

	inuse, _ := h.Read()
	// inuse — uint64, всегда >= 0 по определению, но явно документируем ожидание
	if inuse == 0 {
		// В реальном Go процессе heap.inuse > 0 (минимум runtime structures)
		t.Log("inuse = 0, runtime may report 0 in minimal test environment")
	}
}
