package cpuprofiler

import (
	"sync"
	"testing"
)

// TestCPUProfiler_SnapshotWithoutStart проверяет что Snapshot() при незапущенном
// профиле возвращает nil без паники.
func TestCPUProfiler_SnapshotWithoutStart(t *testing.T) {
	p := &Profiler{}
	data := p.Snapshot()
	if data != nil {
		t.Errorf("Snapshot() without start should return nil, got %d bytes", len(data))
	}
	if p.running {
		t.Error("running should be false after Snapshot without start")
	}
}

// TestCPUProfiler_StopWithoutStart проверяет что Stop() при незапущенном профиле
// не паникует и не меняет состояние.
func TestCPUProfiler_StopWithoutStart(t *testing.T) {
	p := &Profiler{}
	p.Stop() // не должно быть паники
	if p.running {
		t.Error("running should remain false after Stop without start")
	}
}

// TestCPUProfiler_EnsureRunning_Idempotent проверяет что повторный вызов
// EnsureRunning() не вызывает ошибку "cpu profiling already in use".
// Состояние running должно оставаться true после обоих вызовов.
func TestCPUProfiler_EnsureRunning_Idempotent(t *testing.T) {
	p := &Profiler{}
	p.EnsureRunning()
	if !p.running {
		t.Skip("CPU profiling not available in this test environment (another profile may be running)")
	}
	initialRunning := p.running

	p.EnsureRunning() // повторный вызов — должен быть идемпотентным
	if p.running != initialRunning {
		t.Errorf("running changed after second EnsureRunning: %v → %v", initialRunning, p.running)
	}

	// Останавливаем чтобы не мешать следующим тестам.
	p.Stop()
}

// TestCPUProfiler_StartStop проверяет базовый цикл EnsureRunning → Stop.
// После Stop() running должен быть false.
func TestCPUProfiler_StartStop(t *testing.T) {
	p := &Profiler{}
	p.EnsureRunning()
	if !p.running {
		t.Skip("CPU profiling not available")
	}
	p.Stop()
	if p.running {
		t.Error("running should be false after Stop()")
	}
}

// TestCPUProfiler_StartSnapshot проверяет полный цикл EnsureRunning → Snapshot:
//   - Snapshot() возвращает непустые байты (реальный pprof данные)
//   - после Snapshot() profiler остановлен (running = false)
func TestCPUProfiler_StartSnapshot(t *testing.T) {
	p := &Profiler{}
	p.EnsureRunning()
	if !p.running {
		t.Skip("CPU profiling not available")
	}

	// Немного работы чтобы профиль не был совсем пустым.
	sum := 0
	for i := 0; i < 500_000; i++ {
		sum += i
	}
	_ = sum

	data := p.Snapshot()
	if p.running {
		t.Error("running should be false after Snapshot()")
	}
	if len(data) == 0 {
		t.Error("Snapshot() should return non-empty CPU profile data")
	}
}

// TestCPUProfiler_MultipleStartSnapshotCycles проверяет что можно запустить
// несколько циклов последовательно.
func TestCPUProfiler_MultipleStartSnapshotCycles(t *testing.T) {
	p := &Profiler{}

	for i := 0; i < 3; i++ {
		p.EnsureRunning()
		if !p.running {
			t.Skipf("CPU profiling not available at cycle %d", i)
		}
		data := p.Snapshot()
		if p.running {
			t.Errorf("cycle %d: still running after Snapshot", i)
		}
		if len(data) == 0 {
			t.Errorf("cycle %d: empty Snapshot", i)
		}
	}
}

// TestCPUProfiler_Race проверяет потокобезопасность методов Profiler
// при конкурентных вызовах. Запускается с go test -race чтобы обнаружить data races.
// Тест проверяет отсутствие deadlock'ов и паник — не корректность данных профиля.
func TestCPUProfiler_Race(t *testing.T) {
	p := &Profiler{}
	var wg sync.WaitGroup

	const goroutines = 20
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			switch id % 3 {
			case 0:
				p.EnsureRunning()
			case 1:
				p.Stop()
			case 2:
				_ = p.Snapshot()
			}
		}(i)
	}

	wg.Wait()
	// Убеждаемся что профиль остановлен после теста.
	p.Stop()
}
