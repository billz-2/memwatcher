package memwatcher

import (
	"runtime/metrics"
)

const (
	heapObjectsMetric = "/memory/classes/heap/objects:bytes"
	heapUnusedMetric  = "/memory/classes/heap/unused:bytes"

	tier1Pct = 0.70
	tier2Pct = 0.80
	tier3Pct = 0.90
)

// HeapTier описывает текущий уровень заполнения heap относительно GOMEMLIMIT.
type HeapTier int

const (
	HeapTierNormal HeapTier = iota // < 70%
	HeapTier1                      // ≥ 70%: ускорить polling, запустить profiler
	HeapTier2                      // ≥ 80%: первый дамп (cooldown Tier2)
	HeapTier3                      // ≥ 90%: повторный дамп (cooldown Tier3)
)

// HeapMonitor читает HeapInuse без STW и определяет текущий tier.
//
// HeapInuse = objects:bytes + unused:bytes (два счётчика runtime/metrics).
// Оба читаются атомарно — без STW паузы, безопасно вызывать на каждом тике.
// STW происходит только в WriteDump через runtime.ReadMemStats().
//
// sample переиспользуется между вызовами Read() — одна аллокация на весь жизненный цикл.
//
// Может использоваться независимо от Watcher — например, для custom логики
// реакции на давление памяти:
//
//	heap := memwatcher.NewHeapMonitor(debug.SetMemoryLimit(-1))
//	inuse, tier := heap.Read()
//	if tier >= memwatcher.HeapTier2 {
//	    // custom действие
//	}
type HeapMonitor struct {
	sample     []metrics.Sample
	limit      uint64
	thresholds [3]uint64 // [tier1, tier2, tier3] в байтах
}

// NewHeapMonitor создаёт HeapMonitor, вычисляя абсолютные пороги из goMemLimit.
func NewHeapMonitor(goMemLimit int64) *HeapMonitor {
	limit := uint64(goMemLimit)
	return &HeapMonitor{
		sample: []metrics.Sample{
			{Name: heapObjectsMetric},
			{Name: heapUnusedMetric},
		},
		limit: limit,
		thresholds: [3]uint64{
			uint64(float64(goMemLimit) * tier1Pct),
			uint64(float64(goMemLimit) * tier2Pct),
			uint64(float64(goMemLimit) * tier3Pct),
		},
	}
}

// Read читает текущий HeapInuse и возвращает его значение и tier. Без STW.
//
// Защита от KindBad: если метрика не распознана рантаймом (обновление Go
// с переименованием метрик) — возвращаем 0 / HeapTierNormal, не паникуем.
// Тик просто пропускается, следующий тик повторит попытку.
func (h *HeapMonitor) Read() (inuse uint64, tier HeapTier) {
	metrics.Read(h.sample)
	if h.sample[0].Value.Kind() != metrics.KindUint64 ||
		h.sample[1].Value.Kind() != metrics.KindUint64 {
		return 0, HeapTierNormal
	}
	inuse = h.sample[0].Value.Uint64() + h.sample[1].Value.Uint64()
	switch {
	case inuse >= h.thresholds[2]:
		return inuse, HeapTier3
	case inuse >= h.thresholds[1]:
		return inuse, HeapTier2
	case inuse >= h.thresholds[0]:
		return inuse, HeapTier1
	default:
		return inuse, HeapTierNormal
	}
}

// Pct вычисляет процент заполнения GOMEMLIMIT для данного inuse.
func (h *HeapMonitor) Pct(inuse uint64) float64 {
	return float64(inuse) / float64(h.limit) * 100
}
