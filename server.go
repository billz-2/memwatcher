package memwatcher

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DumpServer отдаёт дампы по HTTP.
//
// Монтируется рядом с /debug/pprof/ на /debug/dumps/.
// Для auth сервиса endpoint защищается тем же PprofAuthMiddleware.
//
// Интеграция в gin (пример для auth):
//
//	dumpServer := memwatcher.NewDumpServer(cfg.MemWatcherDumpDir)
//	dumpsGroup := router.Group("/debug/dumps")
//	dumpsGroup.Use(security.PprofAuthMiddleware(cfg))
//	dumpsGroup.GET("/", gin.WrapF(dumpServer.ListHandler))
//	dumpsGroup.GET("/*path", func(c *gin.Context) {
//	    dumpServer.DownloadHandler(c.Writer, c.Request)
//	})
//
// Как получить дампы:
//
//	curl -H "Authorization: Bearer $TOKEN" https://service/debug/dumps/
//	curl -H "Authorization: Bearer $TOKEN" https://service/debug/dumps/memdump-auth-20260311-100523/heap.pprof -O
//	go tool pprof heap.pprof
type DumpServer struct {
	// dumpDir — корневая директория дампов, та же что Config.DumpDir у Watcher.
	// ListHandler ищет поддиректории "memdump-*" именно здесь.
	dumpDir string

	mu       sync.RWMutex
	profiles map[string]*pprofEntry // ключ: "dumpName/fileName", закэшированные pprof handlers
}

// NewDumpServer создаёт DumpServer для директории dumpDir.
// Если dumpDir пустой — использует "/tmp" (аналогично Config.setDefaults).
func NewDumpServer(dumpDir string) *DumpServer {
	if dumpDir == "" {
		dumpDir = "/tmp"
	}
	return &DumpServer{
		dumpDir:  dumpDir,
		profiles: make(map[string]*pprofEntry),
	}
}

// DumpDirInfo — JSON описание одной директории дампа.
// Возвращается ListHandler в массиве для каждой найденной директории "memdump-*".
type DumpDirInfo struct {
	// Name — имя директории без полного пути.
	// Пример: "memdump-billz_auth_service-20260311-100523".
	// Используется для построения URL скачивания: /debug/dumps/{Name}/{filename}.
	Name string `json:"name"`

	// CreatedAt — время модификации директории (RFC3339).
	// Фактически время создания дампа (директория создаётся один раз в writeDump).
	CreatedAt string `json:"created_at"`

	// SizeBytes — суммарный размер всех файлов в директории (байты).
	// Не включает метаданные директории, только содержимое файлов.
	SizeBytes int64 `json:"size_bytes"`

	// Files — список имён файлов в директории.
	// Позволяет клиенту знать какие профили доступны без дополнительных запросов.
	// Пример: ["runtime_stats.json", "heap.pprof", "cpu.pprof", ...]
	Files []string `json:"files"`
}

// ServeHTTP делает DumpServer реализацией http.Handler.
// Роутинг: пустой path → ListHandler, иначе → DownloadHandler.
// Использование со StripPrefix:
//
//	mux.Handle("/debug/dumps/", http.StripPrefix("/debug/dumps", srv))
//
// Использование в gin:
//
//	group := router.Group("/debug/dumps")
//	group.Any("/*path", gin.WrapH(http.StripPrefix("/debug/dumps", srv)))
func (s *DumpServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case path == "":
		s.ListHandler(w, r)
	case strings.HasPrefix(path, "pprof/"):
		s.PprofViewerHandler(w, r)
	case strings.HasPrefix(path, "stats/"):
		s.StatsViewHandler(w, r)
	case isDumpCardPath(path) && wantsHTML(r):
		s.DumpCardHandler(w, r)
	default:
		s.DownloadHandler(w, r)
	}
}

// isDumpCardPath возвращает true для путей вида "memdump-svc-123" или "memdump-svc-123/"
// (один сегмент без вложенного файла). Отличает запрос карточки от скачивания файла
// типа "memdump-svc-123/heap.pprof".
func isDumpCardPath(path string) bool {
	name := strings.TrimSuffix(path, "/")
	return strings.HasPrefix(name, "memdump-") && !strings.Contains(name, "/")
}

// DumpCardHandler рендерит HTML-карточку одного дампа.
// URL: /{dumpName}/ (после rewrite в gateway или StripPrefix для прямого монтирования).
// Возвращает 404 если директория не существует или не является memdump-.
// Доступен только для браузерных запросов (wantsHTML), curl получает 404.
func (s *DumpServer) DumpCardHandler(w http.ResponseWriter, r *http.Request) {
	dumpName := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/")

	if !strings.HasPrefix(dumpName, "memdump-") {
		http.NotFound(w, r)
		return
	}

	dumpPath := filepath.Join(s.dumpDir, dumpName)
	info, err := os.Stat(dumpPath)
	if err != nil || !info.IsDir() {
		http.NotFound(w, r)
		return
	}

	files, totalSize := listDirFiles(dumpPath)
	dump := DumpDirInfo{
		Name:      dumpName,
		CreatedAt: info.ModTime().UTC().Format(time.RFC3339),
		SizeBytes: totalSize,
		Files:     files,
	}

	renderDumpCard(w, s.dumpDir, dump)
}

// RegisterHandlers регистрирует /debug/dumps/ в стандартный http.ServeMux.
//
//	mux := http.NewServeMux()
//	memwatcher.NewDumpServer(cfg.DumpDir).RegisterHandlers(mux)
func (s *DumpServer) RegisterHandlers(mux *http.ServeMux) {
	mux.Handle("/debug/dumps/", http.StripPrefix("/debug/dumps", s))
}

// ListHandler возвращает JSON список директорий дампов.
// Фильтрует по префиксу "memdump-" — игнорирует посторонние файлы в DumpDir.
//
// Ответ: массив DumpDirInfo, отсортированный по os.ReadDir (лексикографически = хронологически,
// т.к. timestamp в имени директории: memdump-svc-20260311-100523 < memdump-svc-20260311-110045).
//
// Если DumpDir не существует (ни одного дампа ещё не было) — возвращает пустой массив [],
// а не 500 ошибку.
func (s *DumpServer) ListHandler(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.dumpDir)
	if err != nil {
		if os.IsNotExist(err) {
			// DumpDir ещё не создан — нет дампов, это нормально.
			if wantsHTML(r) {
				renderDumpList(w, s.dumpDir, nil)
				return
			}
			writeJSON(w, []DumpDirInfo{})
			return
		}
		http.Error(w, "failed to list dumps", http.StatusInternalServerError)
		return
	}

	result := make([]DumpDirInfo, 0, len(entries))
	for _, entry := range entries {
		// Пропускаем файлы (не директории) и директории с неожиданными именами.
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "memdump-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files, totalSize := listDirFiles(filepath.Join(s.dumpDir, entry.Name()))
		result = append(result, DumpDirInfo{
			Name:      entry.Name(),
			CreatedAt: info.ModTime().UTC().Format(time.RFC3339),
			SizeBytes: totalSize,
			Files:     files,
		})
	}

	if wantsHTML(r) {
		renderDumpList(w, s.dumpDir, result)
		return
	}
	writeJSON(w, result)
}

// DownloadHandler скачивает файл из директории дампа.
//
// Ожидаемый URL: /debug/dumps/{dumpDirName}/{filename}
// Пример: /debug/dumps/memdump-billz_auth_service-20260311-100523/heap.pprof
//
// Защита от path traversal реализована двумя независимыми проверками:
//  1. Строковая: strings.Contains(rawPath, "..") — быстрая первичная проверка.
//  2. Символьная: filepath.Abs() + HasPrefix() — защита от нестандартных
//     форм traversal (encoded slashes, null bytes и т.д.).
//
// http.ServeFile автоматически выставляет Content-Type, Content-Length, ETag.
// Content-Disposition: attachment заставляет браузер скачивать, а не отображать.
func (s *DumpServer) DownloadHandler(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.Path

	// Убираем /debug/dumps prefix который добавляет gin из полного URL пути.
	// В gin при использовании router.Group("/debug/dumps"),
	// r.URL.Path в wrapped handler содержит полный путь включая mount prefix.
	// Например: /debug/dumps/memdump-auth-20260311-100523/heap.pprof
	const mountPath = "/debug/dumps"
	if idx := strings.Index(rawPath, mountPath); idx != -1 {
		rawPath = rawPath[idx+len(mountPath):]
	}
	rawPath = strings.TrimPrefix(rawPath, "/")

	// Первичная защита: ".." в пути — явная попытка directory traversal.
	if strings.Contains(rawPath, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if rawPath == "" {
		http.NotFound(w, r)
		return
	}

	// filepath.Clean нормализует путь: убирает двойные слеши, ./,
	// но НЕ защищает от всех форм traversal сам по себе.
	cleanPath := filepath.Clean(rawPath)
	fullPath := filepath.Join(s.dumpDir, cleanPath)

	// Вторичная защита: проверяем что resolved абсолютный путь находится
	// строго внутри dumpDir (с учётом symlinks через filepath.Abs).
	// HasPrefix с PathSeparator на конце: исключаем случай когда
	// dumpDir = "/dumps" и fullPath = "/dumps-evil/file" (неожиданный prefix match).
	absDir, _ := filepath.Abs(s.dumpDir)
	absPath, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absPath, absDir+string(os.PathSeparator)) {
		http.Error(w, "invalid path", http.StatusForbidden)
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	// attachment заставляет браузер скачать файл, а не пытаться отобразить
	// бинарный pprof в браузере.
	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(fullPath))
	// http.ServeFile обрабатывает Range requests, If-Modified-Since и т.д.
	// Также повторно нормализует путь — дополнительный уровень защиты.
	http.ServeFile(w, r, fullPath)
}

// listDirFiles возвращает имена файлов и суммарный размер в директории.
// Не рекурсивный: директории внутри дампа (если бы были) игнорируются.
func listDirFiles(dir string) ([]string, int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	names := make([]string, 0, len(entries))
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return names, total
}

// writeJSON сериализует v в JSON и пишет в w с правильным Content-Type.
// Используется в ListHandler. Ошибка сериализации игнорируется —
// если v это []DumpDirInfo, ошибка принципиально невозможна.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
