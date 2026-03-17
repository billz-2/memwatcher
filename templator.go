package memwatcher

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

const (
	// TemplateKeyOOM — ключ для шаблонов heap dump нотификации.
	// Файлы: templates/oom.slack.tmpl, templates/oom.telegram.tmpl
	TemplateKeyOOM = "oom"

	// TemplateKeyConfigWarning — ключ для шаблонов предупреждения о невалидных tier порогах.
	// Файлы: templates/config_warning.slack.tmpl, templates/config_warning.telegram.tmpl
	TemplateKeyConfigWarning = "config_warning"
)

// Единый embed.FS — все шаблоны директории templates/.
// Добавление нового шаблона: создать файл, объявить константу ключа.
// Никаких изменений в Go-коде для регистрации шаблона не нужно.

//go:embed templates
var templatesFS embed.FS

// Templator рендерит данные в строку по ключу шаблона.
//
// Ключ определяет файл шаблона: key + channel suffix.
//
//	SlackTemplator:    Get("oom", data) → lookup "oom.slack.tmpl"
//	TelegramTemplator: Get("oom", data) → lookup "oom.telegram.tmpl"
//
// Добавить новый тип нотификации: создать шаблоны + константу ключа.
// Интерфейс не меняется.
type Templator interface {
	Get(key string, data any) (string, error)
}

type templator struct {
	tmpl   *template.Template // все шаблоны канала, pre-parsed из embed.FS
	suffix string             // ".slack.tmpl" или ".telegram.tmpl"
}

// Get рендерит шаблон с именем key+suffix и данными data.
// Возвращает ошибку если шаблон не найден или выполнение упало.
func (t *templator) Get(key string, data any) (string, error) {
	name := key + t.suffix
	tmpl := t.tmpl.Lookup(name)
	if tmpl == nil {
		return "", fmt.Errorf("template %q not found (suffix=%q)", name, t.suffix)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template %q: %w", name, err)
	}
	return buf.String(), nil
}

func newTemplator(fs embed.FS, pattern, suffix string) (Templator, error) {
	tmpl, err := template.New("").ParseFS(fs, pattern)
	if err != nil {
		return nil, fmt.Errorf("parse templates %q: %w", pattern, err)
	}
	return &templator{tmpl: tmpl, suffix: suffix}, nil
}

// NewSlackTemplator создаёт Templator для Slack: загружает templates/*.slack.tmpl.
// Парсинг выполняется один раз — при создании. Ошибки синтаксиса шаблонов
// обнаруживаются здесь, а не в runtime при первой отправке.
func NewSlackTemplator() (Templator, error) {
	return newTemplator(templatesFS, "templates/*.slack.tmpl", ".slack.tmpl")
}

// NewTelegramTemplator создаёт Templator для Telegram: загружает templates/*.telegram.tmpl.
func NewTelegramTemplator() (Templator, error) {
	return newTemplator(templatesFS, "templates/*.telegram.tmpl", ".telegram.tmpl")
}
