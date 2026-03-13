package memwatcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"go.uber.org/zap/zapcore"
)

// testLogger — минимальная реализация Logger для тестов.
// Игнорирует все сообщения. Совместима с интерфейсом Logger (zapcore.Field).
type testLogger struct{}

func (testLogger) Info(_ string, _ ...zapcore.Field)  {}
func (testLogger) Error(_ string, _ ...zapcore.Field) {}

// TestDumperWriteAll_WithoutCPU проверяет что writeAll без cpuData создаёт все
// стандартные файлы (runtime_stats.json + pprof профили), но НЕ cpu.pprof.
func TestDumperWriteAll_WithoutCPU(t *testing.T) {
	dir := t.TempDir()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	stats := buildRuntimeStats("test_svc", "test reason", 80.0, 512<<20, 409<<20, 460<<20, ms)

	d := &dumper{dir: dir, log: testLogger{}}
	d.writeAll(stats, nil) // cpuData = nil → cpu.pprof НЕ создаётся

	// runtime_stats.json должен быть создан и содержать валидный JSON.
	statsPath := filepath.Join(dir, "runtime_stats.json")
	data, err := os.ReadFile(statsPath)
	if err != nil {
		t.Fatalf("runtime_stats.json not created: %v", err)
	}
	var parsed RuntimeStats
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("runtime_stats.json not valid JSON: %v", err)
	}
	if parsed.Service != "test_svc" {
		t.Errorf("Service = %q, want test_svc", parsed.Service)
	}
	if parsed.TriggerReason != "test reason" {
		t.Errorf("TriggerReason = %q, want 'test reason'", parsed.TriggerReason)
	}

	// Стандартные pprof файлы должны присутствовать.
	for _, name := range []string{"goroutines.pprof", "heap.pprof", "allocs.pprof"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %q not created", name)
		}
	}

	// cpu.pprof НЕ должен быть создан при nil cpuData.
	if _, err := os.Stat(filepath.Join(dir, "cpu.pprof")); !os.IsNotExist(err) {
		t.Error("cpu.pprof should NOT exist when cpuData is nil")
	}
}

// TestDumperWriteAll_WithCPU проверяет что непустые cpuData записываются как cpu.pprof.
func TestDumperWriteAll_WithCPU(t *testing.T) {
	dir := t.TempDir()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	stats := buildRuntimeStats("svc", "reason", 90.0, 512<<20, 409<<20, 460<<20, ms)

	// Генерируем реальный CPU профиль через cpuProfiler.
	c := &cpuProfiler{}
	c.ensureRunning()
	// Делаем немного CPU работы чтобы профиль не был совсем пустым.
	sum := 0
	for i := 0; i < 1_000_000; i++ {
		sum += i
	}
	_ = sum
	cpuData := c.snapshot()

	if len(cpuData) == 0 {
		t.Skip("CPU profiling not available in this environment (already running)")
	}

	d := &dumper{dir: dir, log: testLogger{}}
	d.writeAll(stats, cpuData)

	cpuPath := filepath.Join(dir, "cpu.pprof")
	info, err := os.Stat(cpuPath)
	if os.IsNotExist(err) {
		t.Fatal("cpu.pprof should exist when cpuData is provided")
	}
	if info.Size() == 0 {
		t.Error("cpu.pprof should not be empty")
	}
}

// TestDumperWriteFile_Success проверяет что writeFile создаёт файл с ожидаемым содержимым.
func TestDumperWriteFile_Success(t *testing.T) {
	dir := t.TempDir()
	d := &dumper{dir: dir, log: testLogger{}}

	content := []byte("test content 123")
	d.writeFile("test.txt", content)

	got, err := os.ReadFile(filepath.Join(dir, "test.txt"))
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

// TestDumperWriteFile_BadDir проверяет что ошибка записи в несуществующую директорию
// логируется, но не вызывает паники.
func TestDumperWriteFile_BadDir(t *testing.T) {
	d := &dumper{dir: "/nonexistent/path/that/does/not/exist", log: testLogger{}}
	// Не должно быть паники — только лог ошибки.
	d.writeFile("any.txt", []byte("data"))
}

// TestDumperWriteFile_OverwritesExisting проверяет что повторный вызов
// перезаписывает существующий файл.
func TestDumperWriteFile_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	d := &dumper{dir: dir, log: testLogger{}}

	d.writeFile("out.txt", []byte("first"))
	d.writeFile("out.txt", []byte("second"))

	got, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Errorf("content = %q, want 'second'", got)
	}
}
