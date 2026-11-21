package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/text/language"
)

//go:embed ui/translations.json
var translationsJSON []byte

var readTranslations = sync.OnceValue(func() map[string]map[string]string {
	var values map[string]map[string]string
	if err := json.Unmarshal(translationsJSON, &values); err != nil {
		panic(err)
	}
	return values
})

func displayLanguage(preferred []string) string {
	for _, name := range preferred {
		name, _, _ = strings.Cut(name, ".")
		name, _, _ = strings.Cut(name, "@")
		tag, err := language.Parse(strings.ReplaceAll(name, "_", "-"))
		if err != nil {
			continue
		}
		base, _ := tag.Base()
		switch base.String() {
		case "en":
			return "en"
		case "zh":
			script, _ := tag.Script()
			if script.String() == "Hant" {
				return "zh-Hant"
			}
			return "zh-Hans"
		}
	}
	return "en"
}

func translationsFor(lang string) map[string]string {
	values := map[string]string{}
	for key, translations := range readTranslations() {
		if value := translations[lang]; value != "" {
			values[key] = value
		}
	}
	return values
}

func saveAppearance(root, theme string) error {
	if theme != "light" && theme != "dark" && theme != "system" {
		return errors.New("invalid appearance")
	}
	path := stateFile(root, "appearance.json")
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	return writeState(path, theme)
}

func panelPage(root, lang string) string {
	theme := "system"
	if err := readState(stateFile(root, "appearance.json"), &theme); err != nil || (theme != "dark" && theme != "light") {
		theme = "system"
	}
	dict, _ := json.Marshal(translationsFor(lang))
	page := strings.Replace(panelHTML, `<html lang="zh-CN">`, `<html lang="`+lang+`" data-appearance="`+theme+`">`, 1)
	return strings.Replace(page, `<script id="translations" type="application/json">{}</script>`, `<script id="translations" type="application/json">`+string(dict)+`</script>`, 1)
}

func notificationText(lang, message string) string {
	dict := translationsFor(lang)
	if value := dict[message]; value != "" {
		return value
	}
	var amount int64
	if _, err := fmt.Sscanf(message, "已到账 %d 点。", &amount); err == nil && fmt.Sprintf("已到账 %d 点。", amount) == message {
		if value := dict["已到账 {0} 点。"]; value != "" {
			return strings.Replace(value, "{0}", fmt.Sprint(amount), 1)
		}
	}
	return message
}
