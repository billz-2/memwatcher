package memwatcher

import (
	"runtime"
	"testing"
	"time"
)

// TestBuildRuntimeStats_BasicFields проверяет что buildRuntimeStats корректно
// маппит все входные параметры в поля RuntimeStats.
func TestBuildRuntimeStats_BasicFields(t *testing.T) {
	ms := runtime.MemStats{
		HeapAlloc:    100 * 1024 * 1024,
		HeapInuse:    120 * 1024 * 1024,
		HeapIdle:     20 * 1024 * 1024,
		HeapSys:      140 * 1024 * 1024,
		HeapReleased: 5 * 1024 * 1024,
		HeapObjects:  50_000,
		TotalAlloc:   500 * 1024 * 1024,
		Mallocs:      1_000_000,
		Frees:        800_000,
		StackInuse:   2 * 1024 * 1024,
		Sys:          200 * 1024 * 1024,
		NumGC:        0, // нет GC — упрощает проверку PauseNs
	}

	const (
		service    = "billz_auth_service"
		reason     = "heap_inuse >= 80% GOMEMLIMIT (83.2%)"
		pct        = 83.2
		memLimit   = 512 * 1024 * 1024
		thresh80   = 409 * 1024 * 1024
		thresh90   = 460 * 1024 * 1024
	)

	stats := buildRuntimeStats(service, reason, pct, memLimit, thresh80, thresh90, ms)

	// Проверяем прямое маппирование параметров.
	if stats.Service != service {
		t.Errorf("Service = %q, want %q", stats.Service, service)
	}
	if stats.TriggerReason != reason {
		t.Errorf("TriggerReason = %q, want %q", stats.TriggerReason, reason)
	}
	if stats.PctOfGoMemLimit != pct {
		t.Errorf("PctOfGoMemLimit = %v, want %v", stats.PctOfGoMemLimit, pct)
	}
	if stats.GoMemLimitBytes != memLimit {
		t.Errorf("GoMemLimitBytes = %d, want %d", stats.GoMemLimitBytes, memLimit)
	}
	if stats.Threshold80PctBytes != thresh80 {
		t.Errorf("Threshold80PctBytes = %d", stats.Threshold80PctBytes)
	}
	if stats.Threshold90PctBytes != thresh90 {
		t.Errorf("Threshold90PctBytes = %d", stats.Threshold90PctBytes)
	}

	// Проверяем маппирование runtime.MemStats полей.
	if stats.HeapAllocBytes != ms.HeapAlloc {
		t.Errorf("HeapAllocBytes = %d, want %d", stats.HeapAllocBytes, ms.HeapAlloc)
	}
	if stats.HeapInuseBytes != ms.HeapInuse {
		t.Errorf("HeapInuseBytes = %d, want %d", stats.HeapInuseBytes, ms.HeapInuse)
	}
	if stats.HeapObjectsCount != ms.HeapObjects {
		t.Errorf("HeapObjectsCount = %d, want %d", stats.HeapObjectsCount, ms.HeapObjects)
	}
	if stats.TotalAllocBytes != ms.TotalAlloc {
		t.Errorf("TotalAllocBytes = %d", stats.TotalAllocBytes)
	}
	if stats.SysBytes != ms.Sys {
		t.Errorf("SysBytes = %d, want %d", stats.SysBytes, ms.Sys)
	}
}

// TestBuildRuntimeStats_LiveObjectsCount проверяет формулу LiveObjectsCount = Mallocs - Frees.
func TestBuildRuntimeStats_LiveObjectsCount(t *testing.T) {
	cases := []struct {
		mallocs uint64
		frees   uint64
		want    uint64
	}{
		{1000, 800, 200},
		{0, 0, 0},
		{500, 499, 1},
		{1_000_000, 0, 1_000_000},
	}

	for _, tc := range cases {
		ms := runtime.MemStats{
			Mallocs: tc.mallocs,
			Frees:   tc.frees,
		}
		stats := buildRuntimeStats("svc", "reason", 80, 512<<20, 409<<20, 460<<20, ms)
		if stats.LiveObjectsCount != tc.want {
			t.Errorf("Mallocs=%d Frees=%d: LiveObjectsCount = %d, want %d",
				tc.mallocs, tc.frees, stats.LiveObjectsCount, tc.want)
		}
	}
}

// TestBuildRuntimeStats_Timestamp проверяет что Timestamp — валидный RFC3339Nano.
func TestBuildRuntimeStats_Timestamp(t *testing.T) {
	before := time.Now().UTC()
	stats := buildRuntimeStats("svc", "r", 80, 0, 0, 0, runtime.MemStats{})
	after := time.Now().UTC()

	ts, err := time.Parse(time.RFC3339Nano, stats.Timestamp)
	if err != nil {
		t.Fatalf("Timestamp %q is not valid RFC3339Nano: %v", stats.Timestamp, err)
	}
	if ts.Before(before) || ts.After(after) {
		t.Errorf("Timestamp %v not in [%v, %v]", ts, before, after)
	}
}

// TestBuildRuntimeStats_NumGoroutinesAndCPU проверяет что NumGoroutines и NumCPU > 0
// (тест всегда запускается в живом Go процессе с как минимум 1 горутиной).
func TestBuildRuntimeStats_NumGoroutinesAndCPU(t *testing.T) {
	stats := buildRuntimeStats("svc", "r", 80, 0, 0, 0, runtime.MemStats{})

	if stats.NumGoroutines <= 0 {
		t.Errorf("NumGoroutines = %d, want > 0", stats.NumGoroutines)
	}
	if stats.NumCPU <= 0 {
		t.Errorf("NumCPU = %d, want > 0", stats.NumCPU)
	}
}

// TestBuildRuntimeStats_RecentGCPauses проверяет что GCPauseRecentNs содержит
// не более 10 элементов и только ненулевые значения.
func TestBuildRuntimeStats_RecentGCPauses(t *testing.T) {
	// Принудительно запускаем несколько GC циклов чтобы заполнить PauseNs.
	for i := 0; i < 5; i++ {
		runtime.GC()
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	stats := buildRuntimeStats("svc", "r", 80, 512<<20, 409<<20, 460<<20, ms)

	if len(stats.GCPauseRecentNs) > 10 {
		t.Errorf("GCPauseRecentNs has %d elements, want <= 10", len(stats.GCPauseRecentNs))
	}
	for i, ns := range stats.GCPauseRecentNs {
		if ns == 0 {
			t.Errorf("GCPauseRecentNs[%d] = 0, should only contain non-zero pauses", i)
		}
	}
	// При NumGC >= 5 должно быть хотя бы несколько пауз.
	if ms.NumGC >= 5 && len(stats.GCPauseRecentNs) == 0 {
		t.Error("GCPauseRecentNs should not be empty after GC cycles")
	}
}

// TestBuildRuntimeStats_NoGCPauses проверяет что при NumGC == 0
// GCPauseRecentNs пустой и GCPauseLastAt пустой.
func TestBuildRuntimeStats_NoGCPauses(t *testing.T) {
	ms := runtime.MemStats{NumGC: 0}
	stats := buildRuntimeStats("svc", "r", 80, 0, 0, 0, ms)

	if len(stats.GCPauseRecentNs) != 0 {
		t.Errorf("expected empty GCPauseRecentNs at NumGC=0, got %v", stats.GCPauseRecentNs)
	}
	if stats.GCPauseLastAt != "" {
		t.Errorf("GCPauseLastAt should be empty at NumGC=0, got %q", stats.GCPauseLastAt)
	}
}
