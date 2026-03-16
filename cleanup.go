package memwatcher

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// cleanup удаляет устаревшие или лишние директории дампов.
// Вызывается внутри writeDump() ДО создания нового дампа.
//
// Порядок применения:
//  1. DumpTTL: удаляем директории старше cfg.DumpTTL (по ModTime).
//  2. MaxDumps: если после TTL-очистки осталось >= cfg.MaxDumps — удаляем самые старые.
//
// ReadDir уже возвращает записи в лексикографическом порядке,
// который для имён memdump-svc-{timestamp} совпадает с хронологическим —
// самые старые идут первыми.
func (w *Watcher) cleanup() {
	if w.cfg.MaxDumps == 0 && w.cfg.DumpTTL == 0 {
		return
	}

	entries, err := os.ReadDir(w.cfg.DumpDir)
	if err != nil {
		// DumpDir ещё не создан (первый дамп) — это норма, ничего удалять не нужно.
		return
	}

	dumps := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "memdump-") {
			dumps = append(dumps, e)
		}
	}

	// Фаза 1: TTL-очистка.
	if w.cfg.DumpTTL > 0 {
		cutoff := time.Now().Add(-w.cfg.DumpTTL)
		remaining := dumps[:0]
		for _, d := range dumps {
			info, err := d.Info()
			if err != nil {
				remaining = append(remaining, d)
				continue
			}
			if info.ModTime().Before(cutoff) {
				w.removeDir(d.Name())
			} else {
				remaining = append(remaining, d)
			}
		}
		dumps = remaining
	}

	// Фаза 2: MaxDumps-очистка.
	// Используем >= а не > чтобы оставить место для нового дампа который сейчас пишется.
	if w.cfg.MaxDumps > 0 {
		for len(dumps) >= w.cfg.MaxDumps {
			if !w.removeDir(dumps[0].Name()) {
				break // ошибка удаления — не застреваем в бесконечном цикле
			}
			dumps = dumps[1:]
		}
	}
}

// removeDir удаляет директорию дампа и логирует результат.
// Возвращает true если удаление успешно.
func (w *Watcher) removeDir(name string) bool {
	path := filepath.Join(w.cfg.DumpDir, name)
	if err := os.RemoveAll(path); err != nil {
		w.cfg.Log.Error("memwatcher: cleanup: failed to remove dump",
			zap.String("name", name),
			zap.Error(err),
		)
		return false
	}
	w.cfg.Log.Info("memwatcher: cleanup: removed dump",
		zap.String("name", name),
	)
	return true
}
