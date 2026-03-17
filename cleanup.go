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
	method := "Watcher.cleanup"
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
		// cutoff — граничный момент времени: директории с ModTime до него считаются устаревшими.
		cutoff := time.Now().Add(-w.cfg.DumpTTL)

		// remaining переиспользует ту же память (dumps[:0]) — избегаем лишней аллокации.
		// Записи из dumps либо перекладываются в remaining (молодые), либо удаляются (старые).
		remaining := dumps[:0]
		for _, d := range dumps {
			info, err := d.Info()
			if err != nil {
				// Не можем определить возраст — оставляем директорию, чтобы не удалить случайно.
				remaining = append(remaining, d)
				continue
			}
			// директория была создана позже cutoff - оставляем
			if info.ModTime().After(cutoff) {
				remaining = append(remaining, d)
				continue
			}
			// Директория старше DumpTTL — удаляем.
			if err := w.removeDir(d.Name()); err != nil {
				w.cfg.Log.Error("memwatcher: cleanup: DumpTTL: failed to remove dump",
					zap.String("method", method),
					zap.String("name", dumps[0].Name()),
					zap.Error(err))
			}
		}
		dumps = remaining
	}

	// Фаза 2: MaxDumps-очистка.
	// Используем >= а не > чтобы оставить место для нового дампа который сейчас пишется.
	if w.cfg.MaxDumps > 0 {
		for len(dumps) >= w.cfg.MaxDumps {
			if err := w.removeDir(dumps[0].Name()); err != nil {
				w.cfg.Log.Error("memwatcher: cleanup: MaxDumps: failed to remove dump",
					zap.String("method", method),
					zap.String("name", dumps[0].Name()),
					zap.Error(err))
				// ошибка удаления — не застреваем в бесконечном цикле
				break
			}
			w.cfg.Log.Info("memwatcher: cleanup: removed dump",
				zap.String("method", method),
				zap.String("name", dumps[0].Name()))

			dumps = dumps[1:]
		}
	}
}

// removeDir удаляет директорию дампа и логирует результат.
func (w *Watcher) removeDir(name string) error {
	path := filepath.Join(w.cfg.DumpDir, name)
	return os.RemoveAll(path)
}
