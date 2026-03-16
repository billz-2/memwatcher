package memwatcher

import (
	"sync"
	"testing"
)

// TestCPUProfiler_SnapshotWithoutStart проверяет что snapshot() при незапущенном
// профиле возвращает nil без паники.
func TestCPUProfiler_SnapshotWithoutStart(t *testing.T) {
	c := &profiler{}
	data := c.snapshot()
	if data != nil {
		t.Errorf("snapshot() without start should return nil, got %d bytes", len(data))
	}
	if c.running {
		t.Error("running should be false after snapshot without start")
	}
}

// TestCPUProfiler_StopWithoutStart проверяет что stop() при незапущенном профиле
// не паникует и не меняет состояние.
func TestCPUProfiler_StopWithoutStart(t *testing.T) {
	c := &profiler{}
	c.stop() // не должно быть паники
	if c.running {
		t.Error("running should remain false after stop without start")
	}
}

// TestCPUProfiler_EnsureRunning_Idempotent проверяет что повторный вызов
// ensureRunning() не вызывает ошибку "cpu profiling already in use".
// Состояние running должно оставаться true после обоих вызовов.
func TestCPUProfiler_EnsureRunning_Idempotent(t *testing.T) {
	c := &profiler{}
	c.ensureRunning()
	if !c.running {
		t.Skip("CPU profiling not available in this test environment (another profile may be running)")
	}
	initialRunning := c.running

	c.ensureRunning() // повторный вызов — должен быть идемпотентным
	if c.running != initialRunning {
		t.Errorf("running changed after second ensureRunning: %v → %v", initialRunning, c.running)
	}

	// Останавливаем чтобы не мешать следующим тестам.
	c.stop()
}

// TestCPUProfiler_StartStop проверяет базовый цикл ensureRunning → stop.
// После stop() running должен быть false.
func TestCPUProfiler_StartStop(t *testing.T) {
	c := &profiler{}
	c.ensureRunning()
	if !c.running {
		t.Skip("CPU profiling not available")
	}
	c.stop()
	if c.running {
		t.Error("running should be false after stop()")
	}
}

// TestCPUProfiler_StartSnapshot проверяет полный цикл ensureRunning → snapshot:
//   - snapshot() возвращает непустые байты (реальный pprof данные)
//   - после snapshot() profiler остановлен (running = false)
func TestCPUProfiler_StartSnapshot(t *testing.T) {
	c := &profiler{}
	c.ensureRunning()
	if !c.running {
		t.Skip("CPU profiling not available")
	}

	// Немного работы чтобы профиль не был совсем пустым.
	sum := 0
	for i := 0; i < 500_000; i++ {
		sum += i
	}
	_ = sum

	data := c.snapshot()
	if c.running {
		t.Error("running should be false after snapshot()")
	}
	if len(data) == 0 {
		t.Error("snapshot() should return non-empty CPU profile data")
	}
}

// TestCPUProfiler_MultipleStartSnapshotCycles проверяет что можно запустить
// несколько циклов последовательно.
func TestCPUProfiler_MultipleStartSnapshotCycles(t *testing.T) {
	c := &profiler{}

	for i := 0; i < 3; i++ {
		c.ensureRunning()
		if !c.running {
			t.Skipf("CPU profiling not available at cycle %d", i)
		}
		data := c.snapshot()
		if c.running {
			t.Errorf("cycle %d: still running after snapshot", i)
		}
		if len(data) == 0 {
			t.Errorf("cycle %d: empty snapshot", i)
		}
	}
}

// TestCPUProfiler_Race проверяет потокобезопасность методов profiler
// при конкурентных вызовах. Запускается с go test -race чтобы обнаружить data races.
// Тест проверяет отсутствие deadlock'ов и паник — не корректность данных профиля.
func TestCPUProfiler_Race(t *testing.T) {
	c := &profiler{}
	var wg sync.WaitGroup

	const goroutines = 20
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			switch id % 3 {
			case 0:
				c.ensureRunning()
			case 1:
				c.stop()
			case 2:
				_ = c.snapshot()
			}
		}(i)
	}

	wg.Wait()
	// Убеждаемся что профиль остановлен после теста.
	c.stop()
}
