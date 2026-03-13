package memwatcher

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// setupDumpDir создаёт временную директорию с тестовыми дампами.
// Возвращает путь к корневой дампа-директории.
// Структура:
//
//	<tmp>/
//	  memdump-svc-20260312-100523/
//	    runtime_stats.json   (8 bytes)
//	    heap.pprof           (5 bytes)
//	  memdump-svc-20260312-110000/
//	    runtime_stats.json   (8 bytes)
//	  not_a_dump/            (должна игнорироваться ListHandler)
//	  plain_file.txt         (должна игнорироваться ListHandler)
func setupDumpDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Первая директория дампа.
	d1 := filepath.Join(dir, "memdump-svc-20260312-100523")
	if err := os.Mkdir(d1, 0o755); err != nil {
		t.Fatalf("mkdir d1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d1, "runtime_stats.json"), []byte(`{"v":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d1, "heap.pprof"), []byte("ppro"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Вторая директория дампа.
	d2 := filepath.Join(dir, "memdump-svc-20260312-110000")
	if err := os.Mkdir(d2, 0o755); err != nil {
		t.Fatalf("mkdir d2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d2, "runtime_stats.json"), []byte(`{"v":2}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Директория без нужного префикса — должна игнорироваться.
	if err := os.Mkdir(filepath.Join(dir, "not_a_dump"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Обычный файл в корне — должен игнорироваться.
	if err := os.WriteFile(filepath.Join(dir, "plain_file.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	return dir
}

// ---- ListHandler ----

// TestListHandler_NonExistentDir проверяет что несуществующая директория дампов
// возвращает пустой JSON массив [], а не 500.
func TestListHandler_NonExistentDir(t *testing.T) {
	srv := NewDumpServer("/nonexistent/path/that/does/not/exist")
	req := httptest.NewRequest(http.MethodGet, "/debug/dumps/", nil)
	rr := httptest.NewRecorder()

	srv.ListHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	var result []DumpDirInfo
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty array, got %v", result)
	}
}

// TestListHandler_WithDumps проверяет что ListHandler возвращает только директории
// с префиксом "memdump-" и корректно заполняет поля.
func TestListHandler_WithDumps(t *testing.T) {
	dir := setupDumpDir(t)
	srv := NewDumpServer(dir)
	req := httptest.NewRequest(http.MethodGet, "/debug/dumps/", nil)
	rr := httptest.NewRecorder()

	srv.ListHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var dumps []DumpDirInfo
	if err := json.NewDecoder(rr.Body).Decode(&dumps); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Должно быть 2 директории (not_a_dump и plain_file.txt фильтруются).
	if len(dumps) != 2 {
		t.Fatalf("expected 2 dumps, got %d: %v", len(dumps), dumps)
	}

	// Первый дамп содержит 2 файла.
	if len(dumps[0].Files) != 2 {
		t.Errorf("dump[0].Files = %v, want 2 files", dumps[0].Files)
	}
	if dumps[0].SizeBytes == 0 {
		t.Error("dump[0].SizeBytes should be > 0")
	}
	if dumps[0].CreatedAt == "" {
		t.Error("dump[0].CreatedAt should not be empty")
	}
	if dumps[0].Name != "memdump-svc-20260312-100523" {
		t.Errorf("dump[0].Name = %q", dumps[0].Name)
	}
}

// ---- DownloadHandler ----

// makeDownloadRequest создаёт запрос с правильным путём для DownloadHandler.
// DownloadHandler стрипает префикс "/debug/dumps" из пути.
func makeDownloadRequest(path string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/debug/dumps/"+path, nil)
}

// TestDownloadHandler_Success проверяет что существующий файл скачивается корректно.
func TestDownloadHandler_Success(t *testing.T) {
	dir := t.TempDir()
	dumpDir := filepath.Join(dir, "memdump-svc-123")
	if err := os.Mkdir(dumpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("heap profile data")
	if err := os.WriteFile(filepath.Join(dumpDir, "heap.pprof"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := NewDumpServer(dir)
	req := makeDownloadRequest("memdump-svc-123/heap.pprof")
	rr := httptest.NewRecorder()

	srv.DownloadHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if cd := rr.Header().Get("Content-Disposition"); cd == "" {
		t.Error("Content-Disposition header missing")
	}
}

// TestDownloadHandler_DotDot проверяет что path traversal через ".." блокируется.
func TestDownloadHandler_DotDot(t *testing.T) {
	dir := t.TempDir()
	srv := NewDumpServer(dir)

	cases := []struct {
		name string
		path string
	}{
		{"parent traversal", "../etc/passwd"},
		{"nested traversal", "memdump-svc/../../../etc/passwd"},
		{"double dot", "memdump-svc/../../secret"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := makeDownloadRequest(tc.path)
			rr := httptest.NewRecorder()
			srv.DownloadHandler(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("path %q: status = %d, want 400 (BadRequest)", tc.path, rr.Code)
			}
		})
	}
}

// TestDownloadHandler_NotFound проверяет что несуществующий файл возвращает 404.
func TestDownloadHandler_NotFound(t *testing.T) {
	dir := t.TempDir()
	srv := NewDumpServer(dir)
	req := makeDownloadRequest("memdump-svc-123/nonexistent.pprof")
	rr := httptest.NewRecorder()

	srv.DownloadHandler(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestDownloadHandler_EmptyPath проверяет что пустой путь возвращает 404.
func TestDownloadHandler_EmptyPath(t *testing.T) {
	dir := t.TempDir()
	srv := NewDumpServer(dir)
	req := httptest.NewRequest(http.MethodGet, "/debug/dumps/", nil)
	rr := httptest.NewRecorder()

	srv.DownloadHandler(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestDownloadHandler_DirectoryNotFile проверяет что запрос к директории возвращает 404.
func TestDownloadHandler_DirectoryNotFile(t *testing.T) {
	dir := t.TempDir()
	dumpDir := filepath.Join(dir, "memdump-svc-123")
	if err := os.Mkdir(dumpDir, 0o755); err != nil {
		t.Fatal(err)
	}

	srv := NewDumpServer(dir)
	// Запрос к директории, а не файлу.
	req := makeDownloadRequest("memdump-svc-123")
	rr := httptest.NewRecorder()

	srv.DownloadHandler(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("directory request: status = %d, want 404", rr.Code)
	}
}

// TestNewDumpServer_EmptyDir проверяет что пустая dumpDir заменяется на /tmp.
func TestNewDumpServer_EmptyDir(t *testing.T) {
	srv := NewDumpServer("")
	if srv.dumpDir != "/tmp" {
		t.Errorf("dumpDir = %q, want /tmp", srv.dumpDir)
	}
}
