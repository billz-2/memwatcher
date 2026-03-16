package memwatcher

// Частичный export_test.go — только алиасы для heap_monitor_test.go и cleanup_test.go.
// Рефакторинг старых тестов (п.7) пропускается.

// heapMonitor exports

type ExportedHeapMonitor = heapMonitor

var NewHeapMonitor = newHeapMonitor

func (h *heapMonitor) Thresholds() [3]uint64    { return h.thresholds }
func (h *heapMonitor) Limit() uint64             { return h.limit }
func (h *heapMonitor) Pct(inuse uint64) float64  { return h.pct(inuse) }
func (h *heapMonitor) Read() (uint64, int)       { inuse, tier := h.read(); return inuse, int(tier) }

// Watcher cleanup export

func (w *Watcher) Cleanup() { w.cleanup() }
