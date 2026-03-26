package memwatcher

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/pprof/driver"
	"github.com/google/pprof/profile"
)

// pprofEntry — закэшированный pprof HTTP handler для одного файла.
// Инвалидируется по mtime: если файл изменился — пересобираем.
type pprofEntry struct {
	mtime   time.Time
	handler http.Handler
}

// PprofViewerHandler обслуживает встроенный pprof web viewer.
//
// Ожидаемый URL (r.URL.Path после StripPrefix "/debug/dumps" или после rewrite в gateway):
//
//	/pprof/{dumpName}/{fileName}/[subpath]
//
// Примеры:
//
//	/pprof/memdump-svc-123/heap.pprof/          → pprof index
//	/pprof/memdump-svc-123/heap.pprof/flamegraph → flamegraph
//	/pprof/memdump-svc-123/heap.pprof/top        → top allocations
//	/pprof/memdump-svc-123/heap.pprof/webui/     → static JS/CSS assets
func (s *DumpServer) PprofViewerHandler(w http.ResponseWriter, r *http.Request) {
	// r.URL.Path = "/pprof/{dumpName}/{fileName}[/sub...]"
	rest := strings.TrimPrefix(r.URL.Path, "/pprof/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}

	dumpName := parts[0]
	fileName := parts[1]

	// Redirect без trailing slash: /pprof/{dump}/{file} → /pprof/{dump}/{file}/
	// Trailing slash нужен чтобы relative ссылки pprof UI корректно резолвились.
	if len(parts) == 2 {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
		return
	}

	subPath := "/" + parts[2] // "/" + "" = "/" для корня; "/" + "flamegraph" = "/flamegraph"

	filePath := filepath.Join(s.dumpDir, dumpName, fileName)
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	h, err := s.getPprofHandler(dumpName, fileName, filePath, info.ModTime())
	if err != nil {
		http.Error(w, "failed to build pprof viewer: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Rewrite URL: даём pprof handler только sub-путь без prefix.
	// pprof web UI использует relative fetch-запросы (fetch('top?...'), fetch('flamegraph?...'))
	// которые браузер резолвит относительно текущего URL — всё работает под любым prefix.
	r2 := r.Clone(r.Context())
	r2.URL.Path = subPath
	h.ServeHTTP(w, r2)
}

// getPprofHandler возвращает закэшированный handler или строит новый.
// Кэш инвалидируется по mtime файла.
func (s *DumpServer) getPprofHandler(dumpName, fileName, filePath string, mtime time.Time) (http.Handler, error) {
	key := dumpName + "/" + fileName

	s.mu.RLock()
	entry, ok := s.profiles[key]
	s.mu.RUnlock()

	if ok && entry.mtime.Equal(mtime) {
		return entry.handler, nil
	}

	h, err := buildPprofHandler(filePath)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.profiles[key] = &pprofEntry{mtime: mtime, handler: h}
	s.mu.Unlock()

	return h, nil
}

// buildPprofHandler создаёт http.Handler для одного pprof файла.
//
// Использует github.com/google/pprof/driver.PProf с кастомными
// Fetch, FlagSet, UI и HTTPServer callback.
//
// driver.PProf вызывает HTTPServer синхронно — регистрирует handlers в mux
// и возвращает nil без запуска реального сервера.
//
// Зарегистрированные routes: /, /top, /flamegraph, /peek, /source, /webui/*
func buildPprofHandler(filePath string) (http.Handler, error) {
	mux := http.NewServeMux()

	err := driver.PProf(&driver.Options{
		Flagset: newPprofFlagset(filePath),
		Fetch:   &pprofFetcher{path: filePath},
		UI:      new(pprofSilentUI),
		HTTPServer: func(args *driver.HTTPServerArgs) error {
			for pattern, h := range args.Handlers {
				mux.Handle(pattern, h)
			}
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build pprof handler for %s: %w", filepath.Base(filePath), err)
	}

	return mux, nil
}

// ─── pprofFetcher ───────────────────────────────────────────────────────────

type pprofFetcher struct{ path string }

func (f *pprofFetcher) Fetch(_ string, _, _ time.Duration) (*profile.Profile, string, error) {
	file, err := os.Open(f.path)
	if err != nil {
		return nil, "", fmt.Errorf("open pprof file: %w", err)
	}
	defer file.Close()

	p, err := profile.Parse(file)
	if err != nil {
		return nil, "", fmt.Errorf("parse pprof file: %w", err)
	}
	return p, "", nil
}

// ─── pprofFlagset ───────────────────────────────────────────────────────────
//
// Минимальная реализация driver.FlagSet:
//   - Parse() → [filePath] — источник профиля для Fetch
//   - "http"       → "localhost:0" — не-пустая строка включает HTTP-mode в driver
//   - "no_browser" → true — не открывать браузер
//   - всё остальное → дефолтные значения pprof driver'а

type pprofFlagset struct {
	source  string
	bools   map[string]*bool
	ints    map[string]*int
	floats  map[string]*float64
	strings map[string]*string
	slices  map[string]*[]*string
}

func newPprofFlagset(source string) *pprofFlagset {
	return &pprofFlagset{
		source:  source,
		bools:   make(map[string]*bool),
		ints:    make(map[string]*int),
		floats:  make(map[string]*float64),
		strings: make(map[string]*string),
		slices:  make(map[string]*[]*string),
	}
}

func (f *pprofFlagset) Bool(name string, def bool, _ string) *bool {
	v := def
	if name == "no_browser" {
		v = true
	}
	f.bools[name] = &v
	return f.bools[name]
}

func (f *pprofFlagset) Int(name string, def int, _ string) *int {
	v := def
	f.ints[name] = &v
	return f.ints[name]
}

func (f *pprofFlagset) Float64(name string, def float64, _ string) *float64 {
	v := def
	f.floats[name] = &v
	return f.floats[name]
}

func (f *pprofFlagset) String(name, def, _ string) *string {
	v := def
	if name == "http" {
		v = "localhost:0" // не-пустая строка → включает HTTP-mode; реальный порт не используется
	}
	f.strings[name] = &v
	return f.strings[name]
}

func (f *pprofFlagset) StringList(name, def, _ string) *[]*string {
	var sl []*string
	if def != "" {
		d := def
		sl = []*string{&d}
	}
	f.slices[name] = &sl
	return f.slices[name]
}

func (f *pprofFlagset) ExtraUsage() string      { return "" }
func (f *pprofFlagset) AddExtraUsage(_ string)  {}
func (f *pprofFlagset) Parse(_ func()) []string { return []string{f.source} }

// ─── pprofSilentUI ──────────────────────────────────────────────────────────

type pprofSilentUI struct{}

func (*pprofSilentUI) ReadLine(_ string) (string, error)    { return "", io.EOF }
func (*pprofSilentUI) Print(_ ...interface{})                {}
func (*pprofSilentUI) PrintErr(_ ...interface{})             {}
func (*pprofSilentUI) IsTerminal() bool                      { return false }
func (*pprofSilentUI) WantBrowser() bool                     { return false }
func (*pprofSilentUI) SetAutoComplete(_ func(string) string) {}
