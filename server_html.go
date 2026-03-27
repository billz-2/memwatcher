package memwatcher

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// wantsHTML возвращает true если клиент — браузер (Accept содержит text/html).
// Используется для content negotiation в ListHandler.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// ─── Template data structs ───────────────────────────────────────────────────

// dumpListData — данные для шаблона templates/dumps.list.html.
type dumpListData struct {
	Dir   string
	Dumps []dumpItem
}

type dumpItem struct {
	Name      string
	CreatedAt string
	SizeStr   string
	Files     []fileItem
	Stats     *statsDisplay // nil если runtime_stats.json не найден или не парсится
}

type fileItem struct {
	Name    string
	IsPprof bool
	IsStats bool // true для runtime_stats.json — показывает кнопку View → StatsViewHandler
}

// ─── Runtime stats (краткая версия для DumpCard и DumpList) ─────────────────

// runtimeStats — структура для парсинга runtime_stats.json.
// Читается при каждом рендере страницы, не кэшируется.
type runtimeStats struct {
	TriggerReason       string  `json:"trigger_reason"`
	PctOfGomemlimit     float64 `json:"pct_of_gomemlimit"`
	GomemLimitBytes     int64   `json:"gomemlimit_bytes"`
	Threshold80PctBytes int64   `json:"threshold_80pct_bytes"`
	Threshold90PctBytes int64   `json:"threshold_90pct_bytes"`
	HeapAllocBytes      int64   `json:"heap_alloc_bytes"`
	HeapInuseBytes      int64   `json:"heap_inuse_bytes"`
	HeapSysBytes        int64   `json:"heap_sys_bytes"`
	SysBytes            int64   `json:"sys_bytes"`
	StackInuseBytes     int64   `json:"stack_inuse_bytes"`
	NumGoroutines       int     `json:"num_goroutines"`
	NumCPU              int     `json:"num_cpu"`
	GCNum               int     `json:"gc_num"`
	GCCPUFraction       float64 `json:"gc_cpu_fraction"`
	GCPauseLastNs       int64   `json:"gc_pause_last_ns"`
}

// statsDisplay — pre-formatted поля для шаблона.
// Вся логика форматирования в Go, шаблон просто выводит строки.
type statsDisplay struct {
	TriggerReason string // "heap_inuse >= 90% GOMEMLIMIT (92.3%)"
	PctStr        string // "92.3%"
	GomemLimitMB  string // "240 MB"
	TierMB        string // "216 MB" — порог который сработал
	HeapInuseMB   string // "221.3 MB"
	HeapAllocMB   string // "211.6 MB"
	HeapSysMB     string // "273.6 MB"
	SysMB         string // "290.8 MB"
	StackMB       string // "6.2 MB"
	Goroutines    int
	CPUs          int
	GCNum         int
	GCCPUPct      string // "3.77%"
	GCPauseLastUs string // "78.8 µs"
}

// buildStatsDisplay конвертирует runtimeStats в statsDisplay с форматированием.
// Tier определяется из trigger_reason: содержит "90%" → threshold_90pct_bytes, иначе threshold_80pct_bytes.
func buildStatsDisplay(rs runtimeStats) *statsDisplay {
	tierBytes := rs.Threshold80PctBytes
	if strings.Contains(rs.TriggerReason, "90%") {
		tierBytes = rs.Threshold90PctBytes
	}
	return &statsDisplay{
		TriggerReason: rs.TriggerReason,
		PctStr:        fmt.Sprintf("%.1f%%", rs.PctOfGomemlimit),
		GomemLimitMB:  fmt.Sprintf("%.0f MB", float64(rs.GomemLimitBytes)/(1<<20)),
		TierMB:        fmt.Sprintf("%.0f MB", float64(tierBytes)/(1<<20)),
		HeapInuseMB:   fmt.Sprintf("%.1f MB", float64(rs.HeapInuseBytes)/(1<<20)),
		HeapAllocMB:   fmt.Sprintf("%.1f MB", float64(rs.HeapAllocBytes)/(1<<20)),
		HeapSysMB:     fmt.Sprintf("%.1f MB", float64(rs.HeapSysBytes)/(1<<20)),
		SysMB:         fmt.Sprintf("%.1f MB", float64(rs.SysBytes)/(1<<20)),
		StackMB:       fmt.Sprintf("%.1f MB", float64(rs.StackInuseBytes)/(1<<20)),
		Goroutines:    rs.NumGoroutines,
		CPUs:          rs.NumCPU,
		GCNum:         rs.GCNum,
		GCCPUPct:      fmt.Sprintf("%.2f%%", rs.GCCPUFraction*100),
		GCPauseLastUs: fmt.Sprintf("%.1f µs", float64(rs.GCPauseLastNs)/1000),
	}
}

// readRuntimeStats читает и парсит runtime_stats.json из директории дампа.
// Возвращает nil если файл не найден или не парсится — это нормально для старых дампов.
func readRuntimeStats(dumpDirPath string) *statsDisplay {
	raw, err := os.ReadFile(filepath.Join(dumpDirPath, "runtime_stats.json"))
	if err != nil {
		return nil
	}
	var rs runtimeStats
	if err := json.Unmarshal(raw, &rs); err != nil {
		return nil
	}
	return buildStatsDisplay(rs)
}

// ─── Runtime stats full (для StatsViewHandler) ───────────────────────────────

// runtimeStatsFull — полный набор полей из runtime_stats.json.
// Используется в StatsViewHandler — содержит все поля RuntimeStats.
type runtimeStatsFull struct {
	Timestamp     string  `json:"timestamp"`
	Service       string  `json:"service"`
	TriggerReason string  `json:"trigger_reason"`
	PctOfGomemlimit float64 `json:"pct_of_gomemlimit"`

	GomemLimitBytes     int64 `json:"gomemlimit_bytes"`
	Threshold80PctBytes int64 `json:"threshold_80pct_bytes"`
	Threshold90PctBytes int64 `json:"threshold_90pct_bytes"`

	HeapAllocBytes    int64 `json:"heap_alloc_bytes"`
	HeapInuseBytes    int64 `json:"heap_inuse_bytes"`
	HeapIdleBytes     int64 `json:"heap_idle_bytes"`
	HeapSysBytes      int64 `json:"heap_sys_bytes"`
	HeapReleasedBytes int64 `json:"heap_released_bytes"`
	HeapObjectsCount  int64 `json:"heap_objects_count"`

	TotalAllocBytes  int64 `json:"total_alloc_bytes"`
	TotalMallocs     int64 `json:"total_mallocs"`
	TotalFrees       int64 `json:"total_frees"`
	LiveObjectsCount int64 `json:"live_objects_count"`

	StackInuseBytes  int64 `json:"stack_inuse_bytes"`
	MSpanInuseBytes  int64 `json:"mspan_inuse_bytes"`
	MCacheInuseBytes int64 `json:"mcache_inuse_bytes"`
	GCSysBytes       int64 `json:"gc_sys_bytes"`
	OtherSysBytes    int64 `json:"other_sys_bytes"`
	SysBytes         int64 `json:"sys_bytes"`

	NumGoroutines int   `json:"num_goroutines"`
	NumCPU        int   `json:"num_cpu"`
	NumCgoCalls   int64 `json:"num_cgo_calls"`

	GCNum             int     `json:"gc_num"`
	GCNumForced       int     `json:"gc_num_forced"`
	GCCPUFraction     float64 `json:"gc_cpu_fraction"`
	GCPauseTotalNs    int64   `json:"gc_pause_total_ns"`
	GCPauseLastNs     int64   `json:"gc_pause_last_ns"`
	GCPauseLastAt     string  `json:"gc_pause_last_at"`
	GCNextTargetBytes int64   `json:"gc_next_target_bytes"`
	GCPauseRecentNs   []int64 `json:"gc_pause_recent_ns"`
}

// statsViewData — pre-formatted данные для шаблона templates/stats.view.html.
// Вся логика форматирования в Go, шаблон просто выводит строки и числа.
type statsViewData struct {
	DumpName  string
	Timestamp string

	// Trigger
	TriggerReason string
	PctStr        string  // "83.2%"
	PctFloat      float64 // для bar width
	GomemLimitStr string  // "512 MB"
	Tier2Str      string  // "409 MB (80%)"
	Tier3Str      string  // "460 MB (90%)"
	HeapInuseStr  string  // "426 MB (83.2%)"

	// Heap bars (0..100)
	HeapInusePct float64
	HeapSysPct   float64
	SysPct       float64

	// Heap
	HeapAllocStr    string
	HeapIdleStr     string
	HeapSysStr      string
	HeapReleasedStr string
	HeapObjectsStr  string // "2 847 431" с разделителями

	// System
	SysStr      string
	StackStr    string
	MSpanStr    string
	MCacheStr   string
	GCSysStr    string
	OtherSysStr string

	// Allocations
	LiveObjectsStr  string
	TotalMallocsStr string
	TotalFreesStr   string
	TotalAllocStr   string  // "58.4 GB"
	FreePct         float64 // frees/mallocs * 100

	// Runtime
	Goroutines int
	CPUs       int
	CgoCalls   int64

	// GC Next
	GCNextStr     string
	GCNextDistStr string  // GOMEMLIMIT - NextTarget
	GCNextPct     float64 // heap_alloc / next_target * 100

	// GC Summary
	GCNum           int
	GCNumForced     int
	GCCPUPctStr     string
	GCCPUPct        float64
	GCPauseTotalStr string
	GCPauseLastStr  string
	GCPauseLastAt   string

	// Sparkline — µs значения для JS
	PauseRecentUs []float64
}

func buildStatsViewData(dumpName string, rs runtimeStatsFull) statsViewData {
	mb := func(b int64) string {
		if b < 0 {
			b = 0
		}
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	}
	gbOrMb := func(b int64) string {
		if b < 0 {
			b = 0
		}
		if b >= 1<<30 {
			return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
		}
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	}
	fmtNs := func(ns int64) string {
		switch {
		case ns >= 1_000_000_000:
			return fmt.Sprintf("%.2f s", float64(ns)/1e9)
		case ns >= 1_000_000:
			return fmt.Sprintf("%.2f ms", float64(ns)/1e6)
		default:
			return fmt.Sprintf("%.1f µs", float64(ns)/1e3)
		}
	}
	clamp := func(v float64) float64 {
		if v < 0 {
			return 0
		}
		if v > 100 {
			return 100
		}
		return v
	}
	pct := func(part, total int64) float64 {
		if total == 0 {
			return 0
		}
		return clamp(float64(part) / float64(total) * 100)
	}
	fmtInt := func(n int64) string {
		// простое форматирование с пробелами через 3 цифры
		s := fmt.Sprintf("%d", n)
		if len(s) <= 3 {
			return s
		}
		out := make([]byte, 0, len(s)+len(s)/3)
		mod := len(s) % 3
		for i, c := range s {
			if i > 0 && (i-mod)%3 == 0 {
				out = append(out, ' ')
			}
			out = append(out, byte(c))
		}
		return string(out)
	}

	pauseUs := make([]float64, 0, len(rs.GCPauseRecentNs))
	for _, ns := range rs.GCPauseRecentNs {
		pauseUs = append(pauseUs, float64(ns)/1000)
	}

	dist := rs.GomemLimitBytes - rs.GCNextTargetBytes
	if dist < 0 {
		dist = 0
	}

	return statsViewData{
		DumpName:  dumpName,
		Timestamp: rs.Timestamp,

		TriggerReason: rs.TriggerReason,
		PctStr:        fmt.Sprintf("%.1f%%", rs.PctOfGomemlimit),
		PctFloat:      clamp(rs.PctOfGomemlimit),
		GomemLimitStr: mb(rs.GomemLimitBytes),
		Tier2Str:      fmt.Sprintf("%s (80%%)", mb(rs.Threshold80PctBytes)),
		Tier3Str:      fmt.Sprintf("%s (90%%)", mb(rs.Threshold90PctBytes)),
		HeapInuseStr:  fmt.Sprintf("%s (%.1f%%)", mb(rs.HeapInuseBytes), rs.PctOfGomemlimit),

		HeapInusePct: pct(rs.HeapInuseBytes, rs.GomemLimitBytes),
		HeapSysPct:   pct(rs.HeapSysBytes, rs.GomemLimitBytes),
		SysPct:       pct(rs.SysBytes, rs.GomemLimitBytes),

		HeapAllocStr:    mb(rs.HeapAllocBytes),
		HeapIdleStr:     mb(rs.HeapIdleBytes),
		HeapSysStr:      mb(rs.HeapSysBytes),
		HeapReleasedStr: mb(rs.HeapReleasedBytes),
		HeapObjectsStr:  fmtInt(rs.HeapObjectsCount),

		SysStr:      mb(rs.SysBytes),
		StackStr:    mb(rs.StackInuseBytes),
		MSpanStr:    mb(rs.MSpanInuseBytes),
		MCacheStr:   mb(rs.MCacheInuseBytes),
		GCSysStr:    mb(rs.GCSysBytes),
		OtherSysStr: mb(rs.OtherSysBytes),

		LiveObjectsStr:  fmtInt(rs.LiveObjectsCount),
		TotalMallocsStr: fmtInt(rs.TotalMallocs),
		TotalFreesStr:   fmtInt(rs.TotalFrees),
		TotalAllocStr:   gbOrMb(rs.TotalAllocBytes),
		FreePct:         pct(rs.TotalFrees, rs.TotalMallocs),

		Goroutines: rs.NumGoroutines,
		CPUs:       rs.NumCPU,
		CgoCalls:   rs.NumCgoCalls,

		GCNextStr:     mb(rs.GCNextTargetBytes),
		GCNextDistStr: mb(dist),
		GCNextPct:     pct(rs.HeapAllocBytes, rs.GCNextTargetBytes),

		GCNum:           rs.GCNum,
		GCNumForced:     rs.GCNumForced,
		GCCPUPctStr:     fmt.Sprintf("%.1f%%", rs.GCCPUFraction*100),
		GCCPUPct:        clamp(rs.GCCPUFraction * 100),
		GCPauseTotalStr: fmtNs(rs.GCPauseTotalNs),
		GCPauseLastStr:  fmtNs(rs.GCPauseLastNs),
		GCPauseLastAt:   rs.GCPauseLastAt,

		PauseRecentUs: pauseUs,
	}
}

// StatsViewHandler рендерит полный дашборд всех метрик из runtime_stats.json.
// URL: /stats/{dumpName}/ (после StripPrefix или gateway rewrite).
// Отдаёт 404 если дамп или runtime_stats.json не существует.
func (s *DumpServer) StatsViewHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/stats/")
	dumpName := strings.Trim(rest, "/")

	if !strings.HasPrefix(dumpName, "memdump-") {
		http.NotFound(w, r)
		return
	}

	dumpPath := filepath.Join(s.dumpDir, dumpName)
	if info, err := os.Stat(dumpPath); err != nil || !info.IsDir() {
		http.NotFound(w, r)
		return
	}

	raw, err := os.ReadFile(filepath.Join(dumpPath, "runtime_stats.json"))
	if err != nil {
		http.Error(w, "runtime_stats.json not found", http.StatusNotFound)
		return
	}
	var rs runtimeStatsFull
	if err := json.Unmarshal(raw, &rs); err != nil {
		http.Error(w, "failed to parse runtime_stats.json", http.StatusInternalServerError)
		return
	}

	data := buildStatsViewData(dumpName, rs)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = statsViewTmpl.Execute(w, data)
}

// ─── Templates ───────────────────────────────────────────────────────────────

// dumpListTmpl загружается из templates/dumps.list.html через templatesFS.
// templatesFS объявлен в templator.go: //go:embed templates
// html/template обеспечивает автоматическое экранирование XSS.
var dumpListTmpl = template.Must(
	template.New("dumps.list.html").ParseFS(templatesFS, "templates/dumps.list.html"),
)

// dumpCardTmpl загружается из templates/dump.card.html.
var dumpCardTmpl = template.Must(
	template.New("dump.card.html").ParseFS(templatesFS, "templates/dump.card.html"),
)

// statsViewTmpl загружается из templates/stats.view.html.
var statsViewTmpl = template.Must(
	template.New("stats.view.html").ParseFS(templatesFS, "templates/stats.view.html"),
)

// dumpCardData — данные для шаблона templates/dump.card.html.
type dumpCardData struct {
	DumpName  string
	CreatedAt string
	SizeStr   string
	Files     []fileItem
	Stats     *statsDisplay // nil если runtime_stats.json не найден
}

// renderDumpList рендерит HTML страницу со списком дампов.
// Список выводится в обратном порядке — newest first.
// Служебные файлы (.uploading, .uploaded) скрываются из вывода.
// runtime_stats.json читается при каждом рендере, без кэша.
func renderDumpList(w http.ResponseWriter, dir string, dumps []DumpDirInfo) {
	data := dumpListData{Dir: dir}

	for i := len(dumps) - 1; i >= 0; i-- {
		d := dumps[i]
		item := dumpItem{
			Name:      d.Name,
			CreatedAt: strings.Replace(d.CreatedAt, "T", " ", 1),
			SizeStr:   formatBytes(d.SizeBytes),
			Stats:     readRuntimeStats(filepath.Join(dir, d.Name)),
		}
		for _, f := range d.Files {
			if f == ".uploaded" || f == ".uploading" {
				continue
			}
			item.Files = append(item.Files, fileItem{
				Name:    f,
				IsPprof: strings.HasSuffix(f, ".pprof"),
				IsStats: f == "runtime_stats.json",
			})
		}
		data.Dumps = append(data.Dumps, item)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dumpListTmpl.Execute(w, data)
}

// renderDumpCard рендерит full-page HTML карточку одного дампа.
// Читает runtime_stats.json при каждом вызове, без кэша.
func renderDumpCard(w http.ResponseWriter, dir string, d DumpDirInfo) {
	data := dumpCardData{
		DumpName:  d.Name,
		CreatedAt: strings.Replace(d.CreatedAt, "T", " ", 1),
		SizeStr:   formatBytes(d.SizeBytes),
		Stats:     readRuntimeStats(filepath.Join(dir, d.Name)),
	}
	for _, f := range d.Files {
		if f == ".uploaded" || f == ".uploading" {
			continue
		}
		data.Files = append(data.Files, fileItem{
			Name:    f,
			IsPprof: strings.HasSuffix(f, ".pprof"),
			IsStats: f == "runtime_stats.json",
		})
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dumpCardTmpl.Execute(w, data)
}

func formatBytes(b int64) string {
	switch {
	case b >= 1 << 20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1 << 10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

