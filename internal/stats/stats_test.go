package stats

import (
	"runtime"
	"testing"
	"time"
)

// TestBuild_BasicFields проверяет что Build корректно
// маппит все входные параметры в поля RuntimeStats.
func TestBuild_BasicFields(t *testing.T) {
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
		service  = "billz_auth_service"
		reason   = "heap_inuse >= 80% GOMEMLIMIT (83.2%)"
		pct      = 83.2
		memLimit = 512 * 1024 * 1024
		thresh80 = 409 * 1024 * 1024
		thresh90 = 460 * 1024 * 1024
	)

	s := Build(service, reason, pct, memLimit, thresh80, thresh90, ms)

	if s.Service != service {
		t.Errorf("Service = %q, want %q", s.Service, service)
	}
	if s.TriggerReason != reason {
		t.Errorf("TriggerReason = %q, want %q", s.TriggerReason, reason)
	}
	if s.PctOfGoMemLimit != pct {
		t.Errorf("PctOfGoMemLimit = %v, want %v", s.PctOfGoMemLimit, pct)
	}
	if s.GoMemLimitBytes != memLimit {
		t.Errorf("GoMemLimitBytes = %d, want %d", s.GoMemLimitBytes, memLimit)
	}
	if s.Threshold80PctBytes != thresh80 {
		t.Errorf("Threshold80PctBytes = %d", s.Threshold80PctBytes)
	}
	if s.Threshold90PctBytes != thresh90 {
		t.Errorf("Threshold90PctBytes = %d", s.Threshold90PctBytes)
	}
	if s.HeapAllocBytes != ms.HeapAlloc {
		t.Errorf("HeapAllocBytes = %d, want %d", s.HeapAllocBytes, ms.HeapAlloc)
	}
	if s.HeapInuseBytes != ms.HeapInuse {
		t.Errorf("HeapInuseBytes = %d, want %d", s.HeapInuseBytes, ms.HeapInuse)
	}
	if s.HeapObjectsCount != ms.HeapObjects {
		t.Errorf("HeapObjectsCount = %d, want %d", s.HeapObjectsCount, ms.HeapObjects)
	}
	if s.TotalAllocBytes != ms.TotalAlloc {
		t.Errorf("TotalAllocBytes = %d", s.TotalAllocBytes)
	}
	if s.SysBytes != ms.Sys {
		t.Errorf("SysBytes = %d, want %d", s.SysBytes, ms.Sys)
	}
}

// TestBuild_LiveObjectsCount проверяет формулу LiveObjectsCount = Mallocs - Frees.
func TestBuild_LiveObjectsCount(t *testing.T) {
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
		s := Build("svc", "reason", 80, 512<<20, 409<<20, 460<<20, ms)
		if s.LiveObjectsCount != tc.want {
			t.Errorf("Mallocs=%d Frees=%d: LiveObjectsCount = %d, want %d",
				tc.mallocs, tc.frees, s.LiveObjectsCount, tc.want)
		}
	}
}

// TestBuild_Timestamp проверяет что Timestamp — валидный RFC3339Nano.
func TestBuild_Timestamp(t *testing.T) {
	before := time.Now().UTC()
	s := Build("svc", "r", 80, 0, 0, 0, runtime.MemStats{})
	after := time.Now().UTC()

	ts, err := time.Parse(time.RFC3339Nano, s.Timestamp)
	if err != nil {
		t.Fatalf("Timestamp %q is not valid RFC3339Nano: %v", s.Timestamp, err)
	}
	if ts.Before(before) || ts.After(after) {
		t.Errorf("Timestamp %v not in [%v, %v]", ts, before, after)
	}
}

// TestBuild_NumGoroutinesAndCPU проверяет что NumGoroutines и NumCPU > 0
// (тест всегда запускается в живом Go процессе с как минимум 1 горутиной).
func TestBuild_NumGoroutinesAndCPU(t *testing.T) {
	s := Build("svc", "r", 80, 0, 0, 0, runtime.MemStats{})

	if s.NumGoroutines <= 0 {
		t.Errorf("NumGoroutines = %d, want > 0", s.NumGoroutines)
	}
	if s.NumCPU <= 0 {
		t.Errorf("NumCPU = %d, want > 0", s.NumCPU)
	}
}

// TestBuild_RecentGCPauses проверяет что GCPauseRecentNs содержит
// не более 10 элементов и только ненулевые значения.
func TestBuild_RecentGCPauses(t *testing.T) {
	// Принудительно запускаем несколько GC циклов чтобы заполнить PauseNs.
	for i := 0; i < 5; i++ {
		runtime.GC()
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := Build("svc", "r", 80, 512<<20, 409<<20, 460<<20, ms)

	if len(s.GCPauseRecentNs) > 10 {
		t.Errorf("GCPauseRecentNs has %d elements, want <= 10", len(s.GCPauseRecentNs))
	}
	for i, ns := range s.GCPauseRecentNs {
		if ns == 0 {
			t.Errorf("GCPauseRecentNs[%d] = 0, should only contain non-zero pauses", i)
		}
	}
	if ms.NumGC >= 5 && len(s.GCPauseRecentNs) == 0 {
		t.Error("GCPauseRecentNs should not be empty after GC cycles")
	}
}

// TestBuild_NoGCPauses проверяет что при NumGC == 0
// GCPauseRecentNs пустой и GCPauseLastAt пустой.
func TestBuild_NoGCPauses(t *testing.T) {
	ms := runtime.MemStats{NumGC: 0}
	s := Build("svc", "r", 80, 0, 0, 0, ms)

	if len(s.GCPauseRecentNs) != 0 {
		t.Errorf("expected empty GCPauseRecentNs at NumGC=0, got %v", s.GCPauseRecentNs)
	}
	if s.GCPauseLastAt != "" {
		t.Errorf("GCPauseLastAt should be empty at NumGC=0, got %q", s.GCPauseLastAt)
	}
}
