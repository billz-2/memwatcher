// Package cpuprofiler управляет жизненным циклом CPU профиля в рамках мониторинга памяти.
//
// Этот пакет — деталь реализации memwatcher. Он не является частью публичного API
// и находится в internal/, чтобы явно ограничить область видимости.
//
// Механика взаимодействия с watcher.go:
//
//  1. При HeapInuse ≥ Tier1 — watcher вызывает EnsureRunning().
//     Профиль начинает накапливать данные о CPU активности.
//     Цель: к моменту срабатывания Tier2 уже будет ~(Tier2-Tier1) * polling_interval
//     секунд записанного CPU профиля — видно что делал сервис пока память росла.
//
//  2. При HeapInuse ≥ Tier2 или Tier3 — watcher вызывает Snapshot()
//     внутри writeDump(), получает накопленные байты и записывает cpu.pprof.
//     После Snapshot() профиль остановлен, но EnsureRunning() немедленно
//     запустит его снова в следующем тике если порог ещё не пройден.
//
//  3. При HeapInuse < Tier1 — watcher вызывает Stop(): профиль сбрасывается,
//     polling замедляется до PollInterval. Не тратим CPU на профилирование
//     в нормальном режиме работы сервиса.
//
// Потокобезопасность: EnsureRunning/Stop/Snapshot могут вызываться
// из разных горутин (основной цикл Run + async writeDump), защищены mu.
package cpuprofiler

import (
	"bytes"
	"runtime/pprof"
	"sync"
)

// Profiler управляет состоянием CPU профиля через runtime/pprof.
type Profiler struct {
	mu sync.Mutex

	// buf накапливает данные профиля пока профилирование активно.
	// pprof.StartCPUProfile() пишет в него напрямую.
	// Сбрасывается при Stop() и перед каждым новым StartCPUProfile().
	buf bytes.Buffer

	// running == true означает что pprof.StartCPUProfile() был вызван
	// и ещё не был остановлен pprof.StopCPUProfile().
	// Нужен чтобы EnsureRunning() не вызывал StartCPUProfile дважды
	// (повторный вызов вернёт ошибку "cpu profiling already in use").
	running bool
}

// EnsureRunning запускает CPU профиль если ещё не запущен.
// Идемпотентный: безопасно вызывать на каждом тике пока HeapInuse ≥ Tier1.
func (p *Profiler) EnsureRunning() {
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

// Stop останавливает профиль и очищает накопленные данные.
// Вызывается когда HeapInuse падает ниже Tier1 — сервис в норме,
// профилирование нецелесообразно (overhead ~2-5% CPU).
func (p *Profiler) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return
	}

	pprof.StopCPUProfile()
	p.buf.Reset()
	p.running = false
}

// Snapshot останавливает профиль и возвращает накопленные данные для записи в файл.
//
// Вызывается из dump.Dumper.WriteAll() через watcher.go::writeDump().
// После возврата профиль остановлен (running = false).
// Следующий тик watcher.go вызовет EnsureRunning() если порог ещё не пройден.
//
// Возвращает nil если профиль не был запущен (например если writeDump
// вызывается первый раз до первого тика Tier1 — маловероятно, но возможно).
// dump.Dumper проверяет len(cpuData) > 0 и не создаёт cpu.pprof если nil.
func (p *Profiler) Snapshot() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil
	}

	// StopCPUProfile() записывает финальные данные в buf и закрывает профиль.
	pprof.StopCPUProfile()
	p.running = false

	// Копируем данные из буфера: buf.Bytes() возвращает slice на внутреннюю
	// память buf, которую сброс при следующем EnsureRunning() перезапишет.
	// Копия гарантирует что Dumper получает стабильный slice.
	data := make([]byte, p.buf.Len())
	copy(data, p.buf.Bytes())
	p.buf.Reset()
	return data
}
