package memwatcher

import (
	"html/template"
	"net/http"
	"os"
	"strings"
)

// ServicesConfig — конфигурация агрегированного UI для gateway.
type ServicesConfig struct {
	// AllDumpsDir — корневой каталог с поддиректориями сервисов.
	// Пример: "/dumps/dev" → содержит "billz_auth_service/", "billz_user_service/" и т.д.
	AllDumpsDir string

	// ForceDumpBasePath — базовый URL для POST запросов force dump.
	// JS делает: fetch(ForceDumpBasePath + "/" + svcName, { method: "POST" })
	// Пример: "/v3/debug/force-dump"
	ForceDumpBasePath string

	// DumpsBasePath — базовый URL для ссылок на список дампов сервиса.
	// Пример: "/v3/debug/dumps"
	DumpsBasePath string
}

// servicesHandler реализует http.Handler для агрегированного UI gateway.
type servicesHandler struct {
	cfg ServicesConfig
}

// NewServicesHandler создаёт http.Handler для страницы списка сервисов с кнопками Force Dump.
// Предназначен для gateway, которому нужен агрегированный UI поверх нескольких DumpServer'ов.
// Шаблон: templates/services.list.html (встроен через embed.FS).
func NewServicesHandler(cfg ServicesConfig) http.Handler {
	return &servicesHandler{cfg: cfg}
}

// serviceListItem — данные одного сервиса для шаблона services.list.html.
type serviceListItem struct {
	// Name — имя сервиса (имя поддиректории AllDumpsDir).
	Name string
	// DumpCount — количество memdump-* директорий в поддиректории сервиса.
	DumpCount int
	// DumpsURL — полный URL для перехода на страницу дампов сервиса.
	DumpsURL string
	// DumpURL — URL для POST запроса force dump (используется в JS fetch).
	DumpURL string
}

// servicesListTmpl загружается из templates/services.list.html через templatesFS.
var servicesListTmpl = template.Must(
	template.New("services.list.html").ParseFS(templatesFS, "templates/services.list.html"),
)

func (h *servicesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(h.cfg.AllDumpsDir)
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, "failed to list services", http.StatusInternalServerError)
		return
	}

	items := make([]serviceListItem, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		items = append(items, serviceListItem{
			Name:      e.Name(),
			DumpCount: countDumps(h.cfg.AllDumpsDir, e.Name()),
			DumpsURL:  h.cfg.DumpsBasePath + "/" + e.Name() + "/",
			DumpURL:   h.cfg.ForceDumpBasePath + "/" + e.Name(),
		})
	}

	if wantsHTML(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = servicesListTmpl.Execute(w, items)
		return
	}
	writeJSON(w, items)
}

// countDumps возвращает количество memdump-* директорий в поддиректории сервиса.
func countDumps(root, svc string) int {
	entries, err := os.ReadDir(root + "/" + svc)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "memdump-") {
			count++
		}
	}
	return count
}
