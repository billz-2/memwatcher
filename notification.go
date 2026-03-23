package memwatcher

import "time"

// OOMNotification — данные для шаблонов TemplateKeyOOM.
// Все вычисления выполнены до создания: HeapInuseMB уже в MB, Timestamp уже UTC.
type OOMNotification struct {
	Service         string
	TriggerReason   string
	HeapInuseMB     uint64    // ms.HeapInuse / 1024 / 1024
	PctOfGoMemLimit float64   // ms.HeapInuse / goMemLimit * 100
	DumpDirName     string    // "memdump-{ServiceName}-{timestamp}"
	DumpURL         string    // прямая ссылка на heap.pprof через gateway; пустая если DumpBaseURL не настроен
	PyroscopeURL    string    // пустая строка если PyroscopeBaseURL не настроен
	Timestamp       time.Time // UTC
}

// ConfigWarningNotification — данные для шаблонов TemplateKeyConfigWarning.
// Заполняется в Config.validateAndHeal() при обнаружении невалидных порогов.
type ConfigWarningNotification struct {
	Service       string
	InvalidFields []string       // ["Tier2Pct=90 >= Tier3Pct=85"]
	ResetValues   map[string]int // {"Tier1Pct": 70, "Tier2Pct": 80, "Tier3Pct": 90}
	Timestamp     time.Time
}
