// export_test.go — тест-мост: добавляет методы к публичным типам только для тест-бинарника.
// Не входит в production build. Не нарушает инкапсуляцию в runtime.
package memwatcher

// Thresholds возвращает внутренние пороги [tier1, tier2, tier3] в байтах.
// Используется в heap_monitor_test.go для проверки вычислений.
func (h *HeapMonitor) Thresholds() [3]uint64 { return h.thresholds }

// Limit возвращает GOMEMLIMIT с которым был создан HeapMonitor.
func (h *HeapMonitor) Limit() uint64 { return h.limit }

// Cleanup открывает приватный w.cleanup() для cleanup_test.go.
func (w *Watcher) Cleanup() { w.cleanup() }

// SetBaseURL позволяет тестам подменить baseURL TelegramNotifier на httptest.Server.
func (t *TelegramNotifier) SetBaseURL(u string) { t.baseURL = u }
