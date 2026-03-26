package memwatcher

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
)

// wantsHTML возвращает true если клиент — браузер (Accept содержит text/html).
// Используется для content negotiation в ListHandler.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

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
}

type fileItem struct {
	Name    string
	IsPprof bool
}

// dumpListTmpl загружается из templates/dumps.list.html через templatesFS.
// templatesFS объявлен в templator.go: //go:embed templates
// html/template обеспечивает автоматическое экранирование XSS.
var dumpListTmpl = template.Must(
	template.New("dumps.list.html").ParseFS(templatesFS, "templates/dumps.list.html"),
)

// renderDumpList рендерит HTML страницу со списком дампов.
// Список выводится в обратном порядке — newest first.
// Служебные файлы (.uploading, .uploaded) скрываются из вывода.
func renderDumpList(w http.ResponseWriter, dir string, dumps []DumpDirInfo) {
	data := dumpListData{Dir: dir}

	for i := len(dumps) - 1; i >= 0; i-- {
		d := dumps[i]
		item := dumpItem{
			Name:      d.Name,
			CreatedAt: strings.Replace(d.CreatedAt, "T", " ", 1),
			SizeStr:   formatBytes(d.SizeBytes),
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
