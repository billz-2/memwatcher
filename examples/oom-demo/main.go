package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/billz-2/memwatcher"
)

// leakStore удерживает аллоцированные чанки в памяти.
// GC не может их собрать пока эта переменная жива.
var (
	mu          sync.Mutex
	leakedChunks [][]byte
)

// StatusResponse — ответ /status в JSON.
type StatusResponse struct {
	HeapInuseBytes  uint64  `json:"heap_inuse_bytes"`
	HeapInuseMB     uint64  `json:"heap_inuse_mb"`
	GoMemLimitBytes int64   `json:"gomemlimit_bytes"`
	PctOfGoMemLimit float64 `json:"pct_of_gomemlimit"`
	NumGoroutines   int     `json:"num_goroutines"`
	TotalLeakedMB   int     `json:"total_leaked_mb"`
	Hint            string  `json:"hint"`
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	memLimit := debug.SetMemoryLimit(-1) // -1 читает без изменения
	pct := 0.0
	if memLimit > 0 {
		pct = float64(ms.HeapInuse) / float64(memLimit) * 100
	}

	mu.Lock()
	totalLeaked := 0
	for _, chunk := range leakedChunks {
		totalLeaked += len(chunk) / 1024 / 1024
	}
	mu.Unlock()

	hint := "normal"
	switch {
	case pct >= 90:
		hint = "CRITICAL: tier3 dump expected"
	case pct >= 80:
		hint = "WARNING: tier2 dump expected"
	case pct >= 70:
		hint = "CAUTION: tier1 - cpu profiling started"
	}

	resp := StatusResponse{
		HeapInuseBytes:  ms.HeapInuse,
		HeapInuseMB:     ms.HeapInuse / 1024 / 1024,
		GoMemLimitBytes: memLimit,
		PctOfGoMemLimit: pct,
		NumGoroutines:   runtime.NumGoroutine(),
		TotalLeakedMB:   totalLeaked,
		Hint:            hint,
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(resp)
}

// leakHandler аллоцирует N MB и удерживает их в pamяти.
// Параметр: ?mb=N (default: 10)
func leakHandler(w http.ResponseWriter, r *http.Request) {
	mb, err := strconv.Atoi(r.URL.Query().Get("mb"))
	if err != nil || mb <= 0 {
		mb = 10
	}

	chunk := make([]byte, mb*1024*1024)
	// Записываем данные чтобы GC не посчитал их "dead" и не собрал.
	for i := range chunk {
		chunk[i] = byte(i % 256)
	}

	mu.Lock()
	leakedChunks = append(leakedChunks, chunk)
	total := 0
	for _, c := range leakedChunks {
		total += len(c) / 1024 / 1024
	}
	mu.Unlock()

	fmt.Fprintf(w, "leaked %d MB (total leaked: %d MB)\n", mb, total)
	log.Printf("leak: +%d MB, total leaked: %d MB", mb, total)
}

// resetHandler освобождает все удерживаемые аллокации и форсирует GC.
func resetHandler(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	count := len(leakedChunks)
	leakedChunks = nil
	mu.Unlock()

	runtime.GC()
	fmt.Fprintf(w, "reset: freed %d chunks, GC forced\n", count)
	log.Printf("reset: freed %d chunks", count)
}

// buildNotifier создаёт notifier из переменных окружения.
// Если ни одна из переменных не задана — возвращает NoopNotifier.
func buildNotifier() memwatcher.Notifier {
	var notifiers []memwatcher.Notifier

	if url := os.Getenv("SLACK_WEBHOOK_URL"); url != "" {
		n, err := memwatcher.NewSlackNotifier(url)
		if err != nil {
			log.Printf("slack notifier init error: %v", err)
		} else {
			notifiers = append(notifiers, n)
			log.Println("notifier: Slack enabled")
		}
	}

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	if token != "" && chatID != "" {
		n, err := memwatcher.NewTelegramNotifier(token, chatID)
		if err != nil {
			log.Printf("telegram notifier init error: %v", err)
		} else {
			notifiers = append(notifiers, n)
			log.Println("notifier: Telegram enabled")
		}
	}

	if len(notifiers) == 0 {
		log.Println("notifier: none configured (set SLACK_WEBHOOK_URL or TELEGRAM_BOT_TOKEN+TELEGRAM_CHAT_ID)")
	}

	return memwatcher.NewMultiNotifier(notifiers...)
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	serviceName := getEnv("SERVICE_NAME", "oom-demo")
	port := getEnv("PORT", "8080")
	dumpDir := getEnv("DUMP_DIR", "/tmp/dumps")

	log.Printf("starting %s on :%s (dump dir: %s)", serviceName, port, dumpDir)

	if limit := debug.SetMemoryLimit(-1); limit > 0 {
		log.Printf("GOMEMLIMIT: %d MB", limit/1024/1024)
	} else {
		log.Println("GOMEMLIMIT: not set — memwatcher will use math.MaxInt64 as limit (thresholds won't trigger meaningfully)")
	}

	notifier := buildNotifier()

	// POLL_INTERVAL — базовый интервал polling в demo.
	// Default: 500ms (агрессивнее чем продакшн 5s) чтобы корректно детектировать
	// все тиры при быстрой симуляции утечки через make oom (1s между аллокациями).
	// В продакшн сервисах используй дефолт (5s) — там утечки медленные.
	pollInterval := 500 * time.Millisecond
	if v := os.Getenv("POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			pollInterval = d
		}
	}

	cfg := memwatcher.Config{
		ServiceName:  serviceName,
		DumpDir:      dumpDir,
		Notifier:     notifier,
		PollInterval: pollInterval,
	}

	watcher, err := memwatcher.New(cfg)
	if err != nil {
		log.Fatalf("memwatcher.New: %v", err)
	}

	go watcher.Run(context.Background())
	log.Println("memwatcher started")

	dumpSrv := memwatcher.NewDumpServer(dumpDir)

	mux := http.NewServeMux()
	mux.HandleFunc("/status", statusHandler)
	mux.HandleFunc("/leak", leakHandler)
	mux.HandleFunc("/reset", resetHandler)
	mux.HandleFunc("/debug/dumps/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/debug/dumps/" || r.URL.Path == "/debug/dumps" {
			dumpSrv.ListHandler(w, r)
		} else {
			dumpSrv.DownloadHandler(w, r)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "oom-demo endpoints:")
		fmt.Fprintln(w, "  GET  /status              — heap stats")
		fmt.Fprintln(w, "  POST /leak?mb=N           — allocate N MB (default: 10)")
		fmt.Fprintln(w, "  POST /reset               — free all leaked memory")
		fmt.Fprintln(w, "  GET  /debug/dumps/        — list dump directories")
		fmt.Fprintln(w, "  GET  /debug/dumps/{dir}/{file} — download dump file")
	})

	log.Fatal(http.ListenAndServe(":"+port, mux))
}
