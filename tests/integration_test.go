package tests

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/billz-2/memwatcher"
	"github.com/prometheus/client_golang/prometheus"
)

// fakeNotifier — реализация Notifier для тестов.
// Записывает полученные уведомления в буферизованный канал.
type fakeNotifier struct {
	received chan memwatcher.DumpNotification
	delay    time.Duration // симуляция медленного нотификатора
}

func newFakeNotifier(delay time.Duration) *fakeNotifier {
	return &fakeNotifier{
		received: make(chan memwatcher.DumpNotification, 1),
		delay:    delay,
	}
}

func (f *fakeNotifier) Notify(ctx context.Context, n memwatcher.DumpNotification) error {
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(f.delay):
		}
	}
	select {
	case f.received <- n:
	default:
	}
	return nil
}

// minCfg возвращает минимальную валидную Config для интеграционных тестов.
func minCfg(t *testing.T, dumpDir string) memwatcher.Config {
	t.Helper()
	return memwatcher.Config{
		ServiceName:   "test_svc",
		DumpDir:       dumpDir,
		PollInterval:  time.Second,
		CooldownTier2: time.Minute,
		CooldownTier3: 30 * time.Second,
		Registerer:    prometheus.NewRegistry(),
	}
}

// heapForTest создаёт HeapMonitor с текущим GOMEMLIMIT.
// GOMEMLIMIT должен быть установлен в реальное значение (не math.MaxInt64).
func heapForTest() *memwatcher.HeapMonitor {
	return memwatcher.NewHeapMonitor(debug.SetMemoryLimit(-1))
}

// ---- Группа 1: Watcher lifecycle ----

// TestWatcher_Stop_TerminatesRun проверяет что Stop() завершает Run() за разумное время.
// Покрывает путь останова через stopCh (SIGTERM → Stop()).
func TestWatcher_Stop_TerminatesRun(t *testing.T) {
	const limit = 256 << 20
	debug.SetMemoryLimit(limit)
	defer debug.SetMemoryLimit(math.MaxInt64)

	w, err := memwatcher.New(minCfg(t, t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan struct{})
	go func() {
		w.Run(context.Background())
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	w.Stop()

	select {
	case <-done:
		// OK — Run() завершился
	case <-time.After(time.Second):
		t.Error("Run() did not stop after Stop() within 1s")
	}
}

// TestWatcher_Stop_WritesShutdownDump проверяет что Stop() при heap >= Tier2
// записывает финальный дамп перед завершением Run().
func TestWatcher_Stop_WritesShutdownDump(t *testing.T) {
	// GOMEMLIMIT = 1 байт → heap гарантированно выше 80% → tier >= HeapTier2
	debug.SetMemoryLimit(1)
	defer debug.SetMemoryLimit(math.MaxInt64)

	dir := t.TempDir()
	w, err := memwatcher.New(minCfg(t, dir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan struct{})
	go func() {
		w.Run(context.Background())
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	w.Stop()

	// Ждём завершения Run() с запасом на запись дампа (pprof + fsync)
	select {
	case <-done:
		// OK
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not exit after Stop()")
	}

	// Финальный дамп должен быть записан
	entries, _ := os.ReadDir(dir)
	var dumpDirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "memdump-test_svc-") {
			dumpDirs = append(dumpDirs, e)
		}
	}
	if len(dumpDirs) == 0 {
		t.Error("shutdown dump was not written after Stop() with heap >= Tier2")
	}
}

// TestWatcher_CtxCancel_TerminatesRun проверяет что отмена ctx завершает Run().
// Покрывает путь останова через signal.NotifyContext (основной production path).
// ctx.Done() — принудительный выход БЕЗ дампа (graceful timeout исчерпан).
func TestWatcher_CtxCancel_TerminatesRun(t *testing.T) {
	const limit = 256 << 20
	debug.SetMemoryLimit(limit)
	defer debug.SetMemoryLimit(math.MaxInt64)

	w, err := memwatcher.New(minCfg(t, t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// OK
	case <-time.After(time.Second):
		t.Error("Run() did not stop after ctx cancel within 1s")
	}
}

// TestWatcher_NoGomemlimit_ExitsImmediately проверяет что без GOMEMLIMIT
// Run() выходит немедленно без зависания в тик-цикле.
func TestWatcher_NoGomemlimit_ExitsImmediately(t *testing.T) {
	prev := debug.SetMemoryLimit(math.MaxInt64)
	defer debug.SetMemoryLimit(prev)

	w, err := memwatcher.New(minCfg(t, t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan struct{})
	go func() {
		w.Run(context.Background())
		close(done)
	}()

	select {
	case <-done:
		// OK — вышел сразу
	case <-time.After(200 * time.Millisecond):
		t.Error("Run() hung when GOMEMLIMIT is not set (math.MaxInt64)")
	}
}

// ---- Группа 2: WriteDump ----

// TestWatcher_WriteDump_CreatesExpectedFiles проверяет что WriteDump создаёт
// директорию memdump-{svc}-{ts}/ с обязательными файлами внутри.
func TestWatcher_WriteDump_CreatesExpectedFiles(t *testing.T) {
	const limit = 256 << 20
	debug.SetMemoryLimit(limit)
	defer debug.SetMemoryLimit(math.MaxInt64)

	dir := t.TempDir()
	w, err := memwatcher.New(minCfg(t, dir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	heap := heapForTest()
	if err := w.WriteDump("2", "test reason", heap); err != nil {
		t.Fatalf("WriteDump: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var dumpDirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "memdump-test_svc-") {
			dumpDirs = append(dumpDirs, e)
		}
	}
	if len(dumpDirs) != 1 {
		t.Fatalf("expected 1 dump dir, got %d", len(dumpDirs))
	}

	dumpPath := filepath.Join(dir, dumpDirs[0].Name())

	required := []string{"runtime_stats.json", "goroutines.pprof", "heap.pprof", "allocs.pprof"}
	for _, name := range required {
		path := filepath.Join(dumpPath, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("required file %q not found in dump dir", name)
		}
	}

	// runtime_stats.json должен быть валидным JSON
	data, _ := os.ReadFile(filepath.Join(dumpPath, "runtime_stats.json"))
	var stats map[string]any
	if err := json.Unmarshal(data, &stats); err != nil {
		t.Errorf("runtime_stats.json is not valid JSON: %v", err)
	}
}

// TestWatcher_WriteDump_CleanupBeforeWrite проверяет что cleanup вызывается ДО
// создания нового дампа: при MaxDumps=2 и 3 существующих → после WriteDump остаётся 2.
func TestWatcher_WriteDump_CleanupBeforeWrite(t *testing.T) {
	const limit = 256 << 20
	debug.SetMemoryLimit(limit)
	defer debug.SetMemoryLimit(math.MaxInt64)

	dir := t.TempDir()

	existingDirs := []string{
		"memdump-test_svc-20260301-100000",
		"memdump-test_svc-20260302-100000",
		"memdump-test_svc-20260303-100000",
	}
	for _, d := range existingDirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	cfg := minCfg(t, dir)
	cfg.MaxDumps = 2
	w, err := memwatcher.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	heap := heapForTest()
	if err := w.WriteDump("2", "reason", heap); err != nil {
		t.Fatalf("WriteDump: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	var dumpDirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "memdump-") {
			dumpDirs = append(dumpDirs, e)
		}
	}

	// cleanup удалил лишние ДО записи → осталось ровно MaxDumps=2
	if len(dumpDirs) != 2 {
		t.Errorf("expected 2 dump dirs after WriteDump(MaxDumps=2), got %d", len(dumpDirs))
	}

	// Самая старая (20260301) должна быть удалена
	for _, d := range dumpDirs {
		if d.Name() == "memdump-test_svc-20260301-100000" {
			t.Error("oldest dump should have been deleted by cleanup")
		}
	}
}

// TestWatcher_WriteDump_NotifiesAllNotifiers проверяет что все нотификаторы
// из []Notifier получают DumpNotification с корректными полями.
func TestWatcher_WriteDump_NotifiesAllNotifiers(t *testing.T) {
	const limit = 256 << 20
	debug.SetMemoryLimit(limit)
	defer debug.SetMemoryLimit(math.MaxInt64)

	n1 := newFakeNotifier(0)
	n2 := newFakeNotifier(0)

	cfg := minCfg(t, t.TempDir())
	cfg.Notifiers = []memwatcher.Notifier{n1, n2}
	cfg.NotifyTimeout = 500 * time.Millisecond

	w, err := memwatcher.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	heap := heapForTest()
	if err := w.WriteDump("2", "test reason", heap); err != nil {
		t.Fatalf("WriteDump: %v", err)
	}

	// Нотификаторы вызываются асинхронно — ждём
	deadline := time.After(500 * time.Millisecond)
	for i, n := range []*fakeNotifier{n1, n2} {
		select {
		case notif := <-n.received:
			if notif.Service != "test_svc" {
				t.Errorf("notifier[%d]: Service = %q, want test_svc", i, notif.Service)
			}
			if notif.TriggerReason != "test reason" {
				t.Errorf("notifier[%d]: TriggerReason = %q, want 'test reason'", i, notif.TriggerReason)
			}
			if notif.DumpDirName == "" {
				t.Errorf("notifier[%d]: DumpDirName is empty", i)
			}
		case <-deadline:
			t.Errorf("notifier[%d] was not called within timeout", i)
		}
	}
}

// TestWatcher_WriteDump_NotifierTimeout проверяет что медленный нотификатор
// не блокирует возврат из WriteDump — уведомление происходит асинхронно.
// Нотификации используют context.Background() + NotifyTimeout — не прерываются.
func TestWatcher_WriteDump_NotifierTimeout(t *testing.T) {
	const limit = 256 << 20
	debug.SetMemoryLimit(limit)
	defer debug.SetMemoryLimit(math.MaxInt64)

	slow := newFakeNotifier(10 * time.Second)

	cfg := minCfg(t, t.TempDir())
	cfg.Notifiers = []memwatcher.Notifier{slow}
	cfg.NotifyTimeout = 50 * time.Millisecond

	w, err := memwatcher.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	heap := heapForTest()
	start := time.Now()
	if err := w.WriteDump("2", "reason", heap); err != nil {
		t.Fatalf("WriteDump: %v", err)
	}
	elapsed := time.Since(start)

	// WriteDump должен вернуться быстро — нотификация async.
	// 500ms с запасом на реальный dump (pprof + fsync).
	if elapsed > 500*time.Millisecond {
		t.Errorf("WriteDump took %v — slow notifier should not block (async)", elapsed)
	}
}

// ---- Группа 3: DumpServer HTTP ----

// TestDumpServer_ServeHTTP_Routing проверяет что ServeHTTP маршрутизирует:
// пустой path → ListHandler (200 + JSON), непустой → DownloadHandler.
func TestDumpServer_ServeHTTP_Routing(t *testing.T) {
	dir := t.TempDir()
	srv := memwatcher.NewDumpServer(dir)

	t.Run("empty path → list", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var result []any
		if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
			t.Fatalf("response is not JSON array: %v", err)
		}
	})

	t.Run("non-empty path → download (404 — файла нет)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/memdump-svc-123/heap.pprof", nil)
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		// 404 ожидаем т.к. файла нет, но это значит DownloadHandler был вызван
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 (DownloadHandler called, file not found)", rr.Code)
		}
	})
}

// TestDumpServer_RegisterHandlers проверяет что RegisterHandlers регистрирует
// обработчик на /debug/dumps/ в стандартном http.ServeMux.
func TestDumpServer_RegisterHandlers(t *testing.T) {
	dir := t.TempDir()
	srv := memwatcher.NewDumpServer(dir)

	mux := http.NewServeMux()
	srv.RegisterHandlers(mux)

	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + "/debug/dumps/")
	if err != nil {
		t.Fatalf("GET /debug/dumps/: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// TestIntegration_WriteDump_ThenServe — end-to-end тест:
// файлы созданные WriteDump видны через DumpServer.ListHandler.
// Связывает Watcher (пишет) и DumpServer (читает) через общий DumpDir.
func TestIntegration_WriteDump_ThenServe(t *testing.T) {
	const limit = 256 << 20
	debug.SetMemoryLimit(limit)
	defer debug.SetMemoryLimit(math.MaxInt64)

	dir := t.TempDir()

	// Watcher создаёт дамп
	w, err := memwatcher.New(minCfg(t, dir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	heap := heapForTest()
	if err := w.WriteDump("2", "integration test", heap); err != nil {
		t.Fatalf("WriteDump: %v", err)
	}

	// DumpServer читает тот же dir
	dumpSrv := memwatcher.NewDumpServer(dir)
	req := httptest.NewRequest(http.MethodGet, "/debug/dumps/", nil)
	rr := httptest.NewRecorder()
	dumpSrv.ListHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("ListHandler status = %d", rr.Code)
	}

	var dumps []struct {
		Name      string   `json:"name"`
		SizeBytes int64    `json:"size_bytes"`
		Files     []string `json:"files"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&dumps); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dumps) != 1 {
		t.Fatalf("expected 1 dump in list, got %d", len(dumps))
	}

	if !strings.HasPrefix(dumps[0].Name, "memdump-test_svc-") {
		t.Errorf("unexpected dump name: %s", dumps[0].Name)
	}
	if dumps[0].SizeBytes == 0 {
		t.Error("SizeBytes should be > 0 after WriteDump")
	}

	filesSet := make(map[string]bool)
	for _, f := range dumps[0].Files {
		filesSet[f] = true
	}
	for _, expected := range []string{"runtime_stats.json", "heap.pprof"} {
		if !filesSet[expected] {
			t.Errorf("expected file %q not in dump listing: %v", expected, dumps[0].Files)
		}
	}
}
