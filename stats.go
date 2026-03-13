package memwatcher

import (
	"runtime"
	"time"
)

// RuntimeStats — полный snapshot состояния runtime Go в момент срабатывания порога.
//
// Записывается первым файлом в директорию дампа ("runtime_stats.json")
// потому что весит < 1 KB и содержит самую ценную агрегированную информацию —
// даже если остальные большие файлы (heap.pprof, cpu.pprof) не успеют записаться
// при OOM kill, этот файл уже будет на диске.
//
// Как читать: открыть runtime_stats.json, посмотреть HeapInuseBytes,
// LiveObjectsCount, NumGoroutines — это даёт первоначальную картину утечки.
// Затем анализировать heap.pprof через "go tool pprof heap.pprof".
type RuntimeStats struct {
	// Timestamp — время создания дампа в RFC3339Nano (UTC).
	Timestamp string `json:"timestamp"`
	// Service — имя сервиса из Config.ServiceName.
	Service string `json:"service"`
	// TriggerReason — причина дампа, например "heap_inuse >= 80% GOMEMLIMIT (83.2%)".
	TriggerReason string `json:"trigger_reason"`
	// PctOfGoMemLimit — процент HeapInuse от GOMEMLIMIT на момент дампа.
	PctOfGoMemLimit float64 `json:"pct_of_gomemlimit"`

	// GoMemLimitBytes — текущий GOMEMLIMIT в байтах (из debug.SetMemoryLimit(-1)).
	// Все пороги вычисляются от этого значения.
	GoMemLimitBytes uint64 `json:"gomemlimit_bytes"`
	// Threshold80PctBytes — абсолютное значение порога 80% (Tier2).
	Threshold80PctBytes uint64 `json:"threshold_80pct_bytes"`
	// Threshold90PctBytes — абсолютное значение порога 90% (Tier3).
	Threshold90PctBytes uint64 `json:"threshold_90pct_bytes"`

	// HeapAllocBytes — объём живых heap объектов прямо сейчас (ms.HeapAlloc).
	// Ключевой показатель: если растёт со временем — утечка.
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	// HeapInuseBytes — объём зарезервированных spans (включает фрагментацию).
	// Именно этот показатель сравниваем с GOMEMLIMIT в watcher.go.
	HeapInuseBytes uint64 `json:"heap_inuse_bytes"`
	// HeapIdleBytes — spans возвращённые GC но не отданные ОС.
	// Большое HeapIdle при малом HeapAlloc = фрагментация, а не утечка.
	HeapIdleBytes uint64 `json:"heap_idle_bytes"`
	// HeapSysBytes — всего получено от ОС под heap (HeapInuse + HeapIdle).
	HeapSysBytes uint64 `json:"heap_sys_bytes"`
	// HeapReleasedBytes — байты возвращённые ОС через madvise.
	HeapReleasedBytes uint64 `json:"heap_released_bytes"`
	// HeapObjectsCount — количество живых объектов на heap (ms.HeapObjects).
	HeapObjectsCount uint64 `json:"heap_objects_count"`

	// TotalAllocBytes — суммарно выделено за всё время жизни процесса.
	// Сравнивать со снапшотами во времени: быстрый рост = много аллокаций.
	TotalAllocBytes uint64 `json:"total_alloc_bytes"`
	// TotalMallocs — количество malloc вызовов за всё время.
	TotalMallocs uint64 `json:"total_mallocs"`
	// TotalFrees — количество free вызовов за всё время.
	TotalFrees uint64 `json:"total_frees"`
	// LiveObjectsCount — количество живых объектов: Mallocs - Frees.
	// Если растёт — объекты не освобождаются → утечка.
	LiveObjectsCount uint64 `json:"live_objects_count"`

	// StackInuseBytes — память занятая stack'ами горутин.
	// Большое значение при большом NumGoroutines → горутин много и они глубокие.
	StackInuseBytes uint64 `json:"stack_inuse_bytes"`
	// MSpanInuseBytes — metadata для heap spans (служебная память GC).
	MSpanInuseBytes uint64 `json:"mspan_inuse_bytes"`
	// MCacheInuseBytes — per-P кэш аллокатора (обычно < 1 MB).
	MCacheInuseBytes uint64 `json:"mcache_inuse_bytes"`
	// GCSysBytes — память занятая метаданными GC.
	GCSysBytes uint64 `json:"gc_sys_bytes"`
	// OtherSysBytes — прочая служебная память runtime.
	OtherSysBytes uint64 `json:"other_sys_bytes"`
	// SysBytes — итого получено от ОС (sum of all *Sys fields).
	// Это и есть RSS процесса с точки зрения Go runtime.
	SysBytes uint64 `json:"sys_bytes"`

	// NumGoroutines — количество живых горутин в момент дампа.
	// Коррелирует с StackInuseBytes. Рост → goroutine leak.
	NumGoroutines int `json:"num_goroutines"`
	// NumCPU — количество логических CPU (GOMAXPROCS ограничивает использование).
	NumCPU int `json:"num_cpu"`
	// NumCgoCalls — количество вызовов cgo за всё время.
	NumCgoCalls int64 `json:"num_cgo_calls"`

	// GCNum — количество завершённых циклов GC.
	GCNum uint32 `json:"gc_num"`
	// GCNumForced — количество принудительных GC (runtime.GC() вызовов).
	GCNumForced uint32 `json:"gc_num_forced"`
	// GCCPUFraction — доля CPU затраченная на GC (0.0 - 1.0).
	// > 0.1 означает что GC занимает > 10% CPU → heap pressure высокий.
	GCCPUFraction float64 `json:"gc_cpu_fraction"`
	// GCPauseTotalNs — суммарное время STW пауз за всё время жизни (ns).
	GCPauseTotalNs uint64 `json:"gc_pause_total_ns"`
	// GCPauseLastNs — длительность последней STW паузы (ns).
	GCPauseLastNs uint64 `json:"gc_pause_last_ns"`
	// GCPauseLastAt — timestamp последней STW паузы (RFC3339Nano).
	GCPauseLastAt string `json:"gc_pause_last_at"`
	// GCNextTargetBytes — размер heap при котором GC запустится в следующий раз.
	// Если очень близко к GoMemLimitBytes — GC будет работать практически непрерывно.
	GCNextTargetBytes uint64 `json:"gc_next_target_bytes"`
	// GCPauseRecentNs — длительности последних 10 STW пауз (ns), от новых к старым.
	// Помогает увидеть тренд: растут паузы или нет.
	GCPauseRecentNs []uint64 `json:"gc_pause_recent_ns"`
}

// buildRuntimeStats формирует RuntimeStats из уже захваченного runtime.MemStats.
//
// Принимает ms по значению (копию), т.к. MemStats захвачен в watcher.go
// до вызова writeDump — это гарантирует, что stats отражает момент срабатывания
// порога, а не состояние после начала записи дампа (GC мог пройти за это время).
//
// Параметры goMemLimit, threshold80, threshold90 передаются явно (а не вычисляются
// здесь заново) чтобы гарантировать использование тех же значений что в watcher.go.
func buildRuntimeStats(
	service, reason string,
	pct float64,
	goMemLimit, threshold80, threshold90 uint64,
	ms runtime.MemStats,
) RuntimeStats {
	// Собираем последние 10 GC пауз из кольцевого буфера PauseNs.
	// PauseNs — массив из 256 элементов. Последняя пауза хранится по индексу
	// (NumGC+255)%256, предыдущая по (NumGC+254)%256, и т.д.
	// Итерируем пока i < NumGC (не выходим за реальные паузы).
	recentPauses := make([]uint64, 0, 10)
	for i := 0; i < 10 && i < int(ms.NumGC); i++ {
		idx := (int(ms.NumGC) + 255 - i) % 256
		if ns := ms.PauseNs[idx]; ns > 0 {
			recentPauses = append(recentPauses, ns)
		}
	}

	// PauseEnd — кольцевой буфер из 256 unix timestamp'ов (наносекунды с эпохи)
	// окончания каждой STW паузы. Берём последний по тому же индексу что и PauseNs.
	var lastPauseAt string
	if ms.NumGC > 0 {
		lastEndNs := ms.PauseEnd[(ms.NumGC+255)%256]
		if lastEndNs > 0 {
			lastPauseAt = time.Unix(0, int64(lastEndNs)).UTC().Format(time.RFC3339Nano)
		}
	}

	return RuntimeStats{
		Timestamp:           time.Now().UTC().Format(time.RFC3339Nano),
		Service:             service,
		TriggerReason:       reason,
		PctOfGoMemLimit:     pct,
		GoMemLimitBytes:     goMemLimit,
		Threshold80PctBytes: threshold80,
		Threshold90PctBytes: threshold90,

		HeapAllocBytes:    ms.HeapAlloc,
		HeapInuseBytes:    ms.HeapInuse,
		HeapIdleBytes:     ms.HeapIdle,
		HeapSysBytes:      ms.HeapSys,
		HeapReleasedBytes: ms.HeapReleased,
		HeapObjectsCount:  ms.HeapObjects,

		TotalAllocBytes:  ms.TotalAlloc,
		TotalMallocs:     ms.Mallocs,
		TotalFrees:       ms.Frees,
		LiveObjectsCount: ms.Mallocs - ms.Frees, // живые объекты = созданные - освобождённые

		StackInuseBytes:  ms.StackInuse,
		MSpanInuseBytes:  ms.MSpanInuse,
		MCacheInuseBytes: ms.MCacheInuse,
		GCSysBytes:       ms.GCSys,
		OtherSysBytes:    ms.OtherSys,
		SysBytes:         ms.Sys,

		NumGoroutines: runtime.NumGoroutine(), // реальное количество горутин прямо сейчас
		NumCPU:        runtime.NumCPU(),
		NumCgoCalls:   runtime.NumCgoCall(),

		GCNum:             ms.NumGC,
		GCNumForced:       ms.NumForcedGC,
		GCCPUFraction:     ms.GCCPUFraction,
		GCPauseTotalNs:    ms.PauseTotalNs,
		GCPauseLastNs:     ms.PauseNs[(ms.NumGC+255)%256],
		GCPauseLastAt:     lastPauseAt,
		GCNextTargetBytes: ms.NextGC,
		GCPauseRecentNs:   recentPauses,
	}
}
