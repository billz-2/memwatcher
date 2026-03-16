package memwatcher

import (
	"runtime/metrics"
)

// Константы метрик и порогов.
const (
	// heapObjectsMetric + heapUnusedMetric в сумме дают HeapInuse из runtime.MemStats.
	//
	// runtime/metrics не имеет прямого аналога ms.HeapInuse.
	// HeapInuse = spans в активном использовании = live objects + fragmentation внутри spans.
	// В runtime/metrics это разбито на две отдельные метрики:
	//   objects:bytes — память занятая живыми объектами      (≈ ms.HeapAlloc)
	//   unused:bytes  — выделенные spans без объектов (фрагментация внутри heap)
	// objects + unused = HeapInuse (spans не возвращённые ОС, но не idle)
	//
	// Обе метрики читаются без STW — атомарные счётчики runtime.
	heapObjectsMetric = "/memory/classes/heap/objects:bytes"
	heapUnusedMetric  = "/memory/classes/heap/unused:bytes"

	// tier1Pct — порог Tier1: ускоряем polling и запускаем CPU профиль.
	tier1Pct = 0.70

	// tier2Pct — порог Tier2: первый дамп всех профилей.
	tier2Pct = 0.80

	// tier3Pct — порог Tier3: повторный дамп с более коротким cooldown.
	tier3Pct = 0.90
)

// heapTier описывает текущий уровень заполнения heap относительно GOMEMLIMIT.
type heapTier int

const (
	heapTierNormal heapTier = iota // < 70%: нормальный режим
	heapTier1                      // ≥ 70%: ускорить polling, запустить profiler
	heapTier2                      // ≥ 80%: первый дамп (cooldown Tier2)
	heapTier3                      // ≥ 90%: повторный дамп (cooldown Tier3)
)

// heapMonitor читает HeapInuse без STW и определяет текущий tier.
//
// HeapInuse = objects:bytes + unused:bytes (два счётчика runtime/metrics).
// Оба читаются атомарно — без STW паузы, безопасно вызывать на каждом тике.
// STW происходит только в writeDump через runtime.ReadMemStats().
//
// sample переиспользуется между вызовами read() — одна аллокация на весь жизненный цикл.
type heapMonitor struct {
	sample     []metrics.Sample
	limit      uint64    // GOMEMLIMIT в байтах
	thresholds [3]uint64 // [tier1, tier2, tier3] в байтах
}

// newHeapMonitor создаёт heapMonitor, вычисляя абсолютные пороги из goMemLimit.
func newHeapMonitor(goMemLimit int64) *heapMonitor {
	limit := uint64(goMemLimit)
	return &heapMonitor{
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

// read читает текущий HeapInuse и возвращает его значение и tier.
//
// Защита от KindBad: если метрика не распознана рантаймом (обновление Go
// с переименованием метрик) — возвращаем 0 / heapTierNormal, не паникуем.
// Тик просто пропускается, следующий тик повторит попытку.
func (h *heapMonitor) read() (inuse uint64, tier heapTier) {
	metrics.Read(h.sample)
	if h.sample[0].Value.Kind() != metrics.KindUint64 ||
		h.sample[1].Value.Kind() != metrics.KindUint64 {
		return 0, heapTierNormal
	}
	inuse = h.sample[0].Value.Uint64() + h.sample[1].Value.Uint64()
	switch {
	case inuse >= h.thresholds[2]:
		return inuse, heapTier3
	case inuse >= h.thresholds[1]:
		return inuse, heapTier2
	case inuse >= h.thresholds[0]:
		return inuse, heapTier1
	default:
		return inuse, heapTierNormal
	}
}

// pct вычисляет процент заполнения GOMEMLIMIT для данного inuse.
func (h *heapMonitor) pct(inuse uint64) float64 {
	return float64(inuse) / float64(h.limit) * 100
}
