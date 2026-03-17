// Package dump записывает диагностические профили в директорию дампа.
//
// Этот пакет — деталь реализации memwatcher. Пользователи библиотеки
// не создают Dumper напрямую; взаимодействие происходит через watcher.WriteDump().
// Пакет находится в internal/, чтобы явно ограничить область видимости.
//
// Принцип "частичный дамп лучше нуля":
// ошибка записи любого файла логируется и не останавливает запись остальных.
// Даже если heap.pprof не записался (нет места на диске),
// runtime_stats.json с ключевой агрегированной информацией уже на диске.
package dump

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger — минимальный интерфейс логгера, который использует Dumper.
// *zap.Logger автоматически удовлетворяет этому интерфейсу.
type Logger interface {
	Info(msg string, fields ...zapcore.Field)
	Error(msg string, fields ...zapcore.Field)
}

// Dumper записывает профили в директорию дампа.
//
// Создаётся внутри watcher.go::writeDump() для каждого дампа:
//
//	d := &dump.Dumper{Dir: dirPath, Log: w.cfg.Log}
//	d.WriteAll(statsJSON, cpuData)
type Dumper struct {
	// Dir — полный путь к директории дампа, например:
	// "/dumps/billz_auth_service/memdump-billz_auth_service-20260311-100523"
	Dir string

	// Log — логгер из Config, используется для записи ошибок и прогресса.
	Log Logger
}

// WriteAll записывает все профили в строго определённом приоритетном порядке.
//
// statsJSON — уже сериализованный JSON snapshot состояния runtime
// (результат json.MarshalIndent(stats.Build(...))).
// Принимает []byte чтобы пакет dump не зависел от пакета stats — разделение ответственности.
//
// Порядок определён по принципу "ценность / размер":
// самые важные и маленькие файлы пишутся первыми.
// Если OOM kill случится в процессе записи — ранние файлы уже будут сохранены.
//
// Приоритет	Файл			Размер		Ценность
// 1		runtime_stats.json	< 1 KB		Агрегированный snapshot: сразу виден масштаб проблемы
// 2		goroutines.pprof	KB–MB		Stack traces горутин: goroutine leak виден здесь
// 3		heap.pprof		1–20 MB		Heap profile: где именно выделена память
// 4		allocs.pprof		1–20 MB		История аллокаций: что выделялось интенсивнее всего
// 5		block.pprof		KB–MB		Блокировки на sync примитивах (нужен SetBlockProfileRate > 0)
// 6		mutex.pprof		KB–MB		Contention на мьютексах (нужен SetMutexProfileFraction > 0)
// 7		cpu.pprof		5–50 MB		CPU активность: что делал сервис пока росла память
//
// cpuData передаётся из cpuprofiler.Profiler.Snapshot() — это уже готовые байты,
// а не pprof.Profile, поэтому обрабатывается через writeFile (не writePprof).
func (d *Dumper) WriteAll(statsJSON []byte, cpuData []byte) {
	// runtime_stats.json — первым, гарантированно маленький.
	if len(statsJSON) > 0 {
		d.writeFile("runtime_stats.json", statsJSON)
	}

	d.writePprof("goroutines.pprof", "goroutine")
	d.writePprof("heap.pprof", "heap")
	d.writePprof("allocs.pprof", "allocs")
	d.writePprof("block.pprof", "block")
	d.writePprof("mutex.pprof", "mutex")

	// cpu.pprof пишем только если профиль был запущен и накопил данные.
	// cpuprofiler.Profiler.Snapshot() возвращает nil если профиль не стартовал
	// (например при первом же дампе до первого тика Tier1).
	if len(cpuData) > 0 {
		d.writeFile("cpu.pprof", cpuData)
	}
}

// writePprof захватывает встроенный Go профиль по имени и записывает в файл.
//
// profileName соответствует именам встроенных профилей pprof:
// "goroutine", "heap", "allocs", "block", "mutex", "threadcreate".
//
// WriteTo(buf, 0) — debug=0 означает бинарный protobuf формат (pprof protocol).
// Именно его понимают: "go tool pprof", Pyroscope, Grafana Phlare.
// debug=1 или debug=2 дали бы текстовый формат — не совместим с большинством инструментов.
//
// block и mutex профили будут пустыми если сервис не вызвал:
//   - runtime.SetBlockProfileRate(rate) для block
//   - runtime.SetMutexProfileFraction(fraction) для mutex
//
// Это не ошибка — пустые файлы не создаются (WriteTo пишет 0 байт → writeFile
// всё равно создаёт файл, но он валидный пустой pprof).
func (d *Dumper) writePprof(filename, profileName string) {
	method := "Dumper.writePprof"

	p := pprof.Lookup(profileName)
	if p == nil {
		// Теоретически не должно случиться для встроенных профилей,
		// но не паникуем — просто пропускаем этот файл.
		return
	}

	var buf bytes.Buffer
	if err := p.WriteTo(&buf, 0); err != nil {
		d.Log.Error("memwatcher: failed to capture pprof profile",
			zap.String("method", method),
			zap.String("profile", profileName),
			zap.Error(err))
		return
	}

	d.writeFile(filename, buf.Bytes())
}

// writeFile записывает байты в файл с fsync.
//
// fsync (f.Sync()) критически важен: без него при OOM kill ядро может не сбросить
// page cache на диск и файл окажется пустым или частичным.
// Каждый файл синхронизируется отдельно — так даже если процесс убьют в середине
// записи следующего файла, уже завершённые файлы гарантированно целы на диске.
//
// Стоимость fsync: ~1-10ms на SSD (PVC обычно network storage — может быть дольше).
// Для 7 файлов суммарно ~7-70ms — приемлемо для диагностики при OOM.
func (d *Dumper) writeFile(filename string, data []byte) {
	method := "Dumper.writeFile"

	path := filepath.Join(d.Dir, filename)

	f, err := os.Create(path)
	if err != nil {
		d.Log.Error("memwatcher: failed to create dump file",
			zap.String("method", method),
			zap.String("path", path),
			zap.Error(err))
		return
	}
	// defer Close() вызовется после Sync() — это нормально,
	// Close() на уже синхронизированном файле не потеряет данные.
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		d.Log.Error("memwatcher: failed to write dump file",
			zap.String("method", method),
			zap.String("path", path),
			zap.Error(err))
		return
	}

	// Принудительный сброс page cache на диск.
	// Без этого данные могут оставаться в памяти ядра и быть потеряны при OOM kill.
	if err := f.Sync(); err != nil {
		d.Log.Error("memwatcher: fsync failed",
			zap.String("method", method),
			zap.String("path", path),
			zap.Error(err))
		return
	}

	d.Log.Info(fmt.Sprintf("memwatcher: wrote %s (%d bytes)", filename, len(data)),
		zap.String("method", method))
}
