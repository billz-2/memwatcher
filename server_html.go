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
}

// ─── Runtime stats ───────────────────────────────────────────────────────────

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
