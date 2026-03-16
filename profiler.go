package memwatcher

import (
	"bytes"
	"runtime/pprof"
	"sync"
)

// profiler управляет жизненным циклом CPU профиля в рамках мониторинга памяти.
//
// Механика взаимодействия с watcher.go:
//
//  1. При HeapInuse ≥ 70% (Tier1) — watcher вызывает ensureRunning().
//     Профиль начинает накапливать данные о CPU активности.
//     Цель: к моменту срабатывания Tier2 (80%) уже будет ~(80-70)% * polling_interval
//     секунд записанного CPU профиля — видно что делал сервис пока память росла.
//
//  2. При HeapInuse ≥ 80% или ≥ 90% (Tier2/3) — watcher вызывает snapshot()
//     внутри writeDump(), получает накопленные байты и записывает cpu.pprof.
//     После snapshot() профиль остановлен, но ensureRunning() немедленно
//     запустит его снова в следующем тике если порог ещё не пройден.
//
//  3. При HeapInuse < 70% — watcher вызывает stop(): профиль сбрасывается,
//     polling замедляется до PollInterval. Не тратим CPU на профилирование
//     в нормальном режиме работы сервиса.
//
// Потокобезопасность: ensureRunning/stop/snapshot могут вызываться
// из разных горутин (основной цикл Run + async writeDump), защищены mu.
type profiler struct {
	mu sync.Mutex

	// buf накапливает данные профиля пока профилирование активно.
	// pprof.StartCPUProfile() пишет в него напрямую.
	// Сбрасывается при stop() и перед каждым новым StartCPUProfile().
	buf bytes.Buffer

	// running == true означает что pprof.StartCPUProfile() был вызван
	// и ещё не был остановлен pprof.StopCPUProfile().
	// Нужен чтобы ensureRunning() не вызывал StartCPUProfile дважды
	// (повторный вызов вернёт ошибку "cpu profiling already in use").
	running bool
}

// ensureRunning запускает CPU профиль если ещё не запущен.
// Идемпотентный: безопасно вызывать на каждом тике пока HeapInuse ≥ 70%.
func (p *profiler) ensureRunning() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return
	}

	// Сбрасываем буфер перед новым профилем.
	// buf.Reset() не освобождает память, только сбрасывает указатель записи —
	// переиспользуем уже выделенный буфер без аллокации.
	p.buf.Reset()

	// pprof.StartCPUProfile() запускает сбор stack traces с частотой 100 Hz.
	// Может вернуть ошибку если профилирование уже запущено где-то ещё
	// (например параллельный тест или другой инструмент).
	if err := pprof.StartCPUProfile(&p.buf); err != nil {
		return // не паникуем — профиль просто не будет собран
	}
	p.running = true
}

// stop останавливает профиль и очищает накопленные данные.
// Вызывается когда HeapInuse падает ниже 70% — сервис в норме,
// профилирование нецелесообразно (overhead ~2-5% CPU).
func (p *profiler) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return
	}

	pprof.StopCPUProfile()
	p.buf.Reset()
	p.running = false
}

// snapshot останавливает профиль и возвращает накопленные данные для записи в файл.
//
// Вызывается из dump.go::writeAll() → watcher.go::writeDump().
// После возврата профиль остановлен (running = false).
// Следующий тик watcher.go вызовет ensureRunning() если порог ещё не пройден.
//
// Возвращает nil если профиль не был запущен (например если writeDump
// вызывается первый раз до первого тика Tier1 — маловероятно, но возможно).
// dump.go проверяет len(cpuData) > 0 и не создаёт cpu.pprof если nil.
func (p *profiler) snapshot() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil
	}

	// StopCPUProfile() записывает финальные данные в buf и закрывает профиль.
	pprof.StopCPUProfile()
	p.running = false

	// Копируем данные из буфера: buf.Bytes() возвращает slice на внутреннюю
	// память buf, которую сброс при следующем ensureRunning() перезапишет.
	// Копия гарантирует что dump.go получает стабильный slice.
	data := make([]byte, p.buf.Len())
	copy(data, p.buf.Bytes())
	p.buf.Reset()
	return data
}
