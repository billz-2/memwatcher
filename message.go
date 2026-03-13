package memwatcher

import (
	"bytes"
	_ "embed"
	"text/template"
)

// Шаблоны встраиваются в бинарник на этапе компиляции через //go:embed.
// Это безопасно — embed просто присваивает содержимое файла строковой переменной,
// никаких вызовов функций в package init не происходит.
//
// Парсинг шаблонов выполняется в конструкторах notifier'ов (NewSlackNotifier,
// NewTelegramNotifier) и возвращает error — никакой паники при init.

//go:embed templates/slack.tmpl
var slackTmplContent string

//go:embed templates/telegram.tmpl
var telegramTmplContent string

// MessageData — общие данные для рендеринга шаблона уведомления.
//
// Формируется из DumpNotification через newMessageData().
// Используется обоими notifier'ами (Slack и Telegram) — единый источник данных,
// разные шаблоны разметки для каждого канала.
//
// Связь с шаблонами:
//
//	DumpNotification (notifier.go)
//	    └─► newMessageData()
//	            ├─► renderMessage(s.tmpl, data)  → SlackNotifier.Notify()
//	            └─► renderMessage(t.tmpl, data)  → TelegramNotifier.Notify()
//
// Шаблоны (содержимое):
//
//	templates/slack.tmpl    — Slack mrkdwn: *bold*, `code`, <URL|text>
//	templates/telegram.tmpl — Telegram HTML: <b>, <code>, <a href>
type MessageData struct {
	// Service — имя сервиса. В Slack обёртывается в `backticks`, в Telegram в <code>.
	Service string
	// TriggerReason — причина дампа. В Slack в `backticks`, в Telegram в <code>.
	TriggerReason string
	// HeapInuseMB — HeapInuse в мегабайтах (уже переведён из байт).
	// Перевод делается здесь, а не в шаблоне, т.к. шаблоны не поддерживают арифметику.
	HeapInuseMB uint64
	// PctOfGoMemLimit — процент использования GOMEMLIMIT (например 83.2).
	// В шаблоне форматируется через printf "%.1f" для одного знака после запятой.
	PctOfGoMemLimit float64
	// DumpDirName — имя директории дампа (без полного пути).
	DumpDirName string
	// PyroscopeURL — ссылка на Pyroscope UI. Пустая строка если Pyroscope не настроен.
	// В шаблонах обёрнута в {{if .PyroscopeURL}} — строка добавляется только если задана.
	PyroscopeURL string
}

// newMessageData преобразует DumpNotification в MessageData для шаблонов.
func newMessageData(n DumpNotification) MessageData {
	return MessageData{
		Service:         n.Service,
		TriggerReason:   n.TriggerReason,
		HeapInuseMB:     n.HeapInuseBytes / 1024 / 1024,
		PctOfGoMemLimit: n.PctOfGoMemLimit,
		DumpDirName:     n.DumpDirName,
		PyroscopeURL:    n.PyroscopeURL,
	}
}

// parseTemplate парсит шаблон из строки и возвращает ошибку если синтаксис невалиден.
// Используется конструкторами NewSlackNotifier и NewTelegramNotifier.
// Никакой паники — только error.
func parseTemplate(name, content string) (*template.Template, error) {
	return template.New(name).Parse(content)
}

// renderMessage рендерит шаблон tmpl с данными data и возвращает готовую строку.
// Общая функция для всех notifier'ов.
func renderMessage(tmpl *template.Template, data MessageData) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
