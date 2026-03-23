package memwatcher

import "context"

// DumpUploader загружает директорию с дампом в удалённое хранилище.
// Каждый сервис реализует свою версию (minioUploaderV6, minioUploaderV7 и т.д.)
// в пакете pkg/profiling.
type DumpUploader interface {
	// Upload загружает все файлы из dumpDirPath в хранилище.
	// dumpDirPath — абсолютный путь к директории вида memdump-{service}-{timestamp}/.
	// Метаданные-файлы (.uploading, .uploaded) пропускаются автоматически.
	Upload(ctx context.Context, dumpDirPath string) error
}

// NoopUploader — заглушка по умолчанию, когда MinIO не настроен.
type NoopUploader struct{}

func (NoopUploader) Upload(_ context.Context, _ string) error { return nil }
