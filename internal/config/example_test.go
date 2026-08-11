package config

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Проверки самого `.env.example`. Файл — не документация, а входные данные:
// docker compose читает его копию через `env_file` и отдаёт значения процессу.
// Ошибка оформления доезжает до боя молча, потому что компилятор такой файл не
// видит, а сервис поднимается как ни в чём не бывало.
//
// Так уже вышло: `CALLBACK_URL=   # пусто = коллбэки выключены` приехал в
// контейнер вместе с комментарием, коллбэки оказались включены на адрес
// «# шаблон с плейсхолдером {taskKey}», и каждая готовая задача уходила в три
// неудачных POST'а с бэкоффом. Нашлось только живым прогоном.

const examplePath = "../../.env.example"

// envLine — строка вида KEY=VALUE (KEY заглавными, с подчёркиваниями).
var envLine = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)=(.*)$`)

func exampleLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(examplePath)
	require.NoError(t, err, ".env.example обязан лежать в репозитории: его копируют в .env")
	return strings.Split(string(raw), "\n")
}

// TestExampleHasNoInlineComments — комментарий обязан стоять НАД переменной.
//
// Компоуз отрезает хвостовой комментарий только у непустого значения: у
// пустого он сначала съедает пробелы, и значением становится сам комментарий.
// Полагаться на это различие нельзя, поэтому запрет общий — никаких `#` в
// строке с присваиванием.
func TestExampleHasNoInlineComments(t *testing.T) {
	for i, line := range exampleLines(t) {
		m := envLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		assert.NotContains(t, m[2], "#",
			"строка %d (%s): комментарий в строке присваивания. Перенеси его на строку выше — "+
				"docker compose отрежет его не всегда, и значением станет текст комментария",
			i+1, m[1])
	}
}

// TestExampleValuesAreTrimmed — ни ведущих, ни хвостовых пробелов в значении.
//
// Пробел не виден глазом, но доезжает до процесса: `RESOLVE_TIMEOUT=25s ` не
// разберётся как длительность, и конфиг молча возьмёт значение по умолчанию.
func TestExampleValuesAreTrimmed(t *testing.T) {
	for i, line := range exampleLines(t) {
		m := envLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		assert.Equal(t, strings.TrimSpace(m[2]), m[2],
			"строка %d (%s): в значении лишние пробелы", i+1, m[1])
	}
}

// TestExampleCoversEveryEnvKey — в примере есть КАЖДАЯ переменная, которую
// читает Load, и нет ни одной лишней.
//
// Пропущенная переменная — это настройка, о которой эксплуатация не узнает:
// так `OSRM_URL` не доехал до локального `.env`, и чистка тихо работала
// вхолостую. Лишняя — обещание ручки, которой нет: конвейер уже не читает
// пороги снятых фильтров, а строки в файле их всё ещё предлагали крутить.
func TestExampleCoversEveryEnvKey(t *testing.T) {
	inFile := map[string]bool{}
	for _, line := range exampleLines(t) {
		if m := envLine.FindStringSubmatch(line); m != nil {
			inFile[m[1]] = true
		}
	}

	// Ключи, которые читает Load. Берём из исходника, а не списком руками:
	// список пришлось бы помнить, а исходник меняется вместе с кодом.
	src, err := os.ReadFile("config.go")
	require.NoError(t, err)
	inCode := map[string]bool{}
	for _, m := range regexp.MustCompile(`env\w*\("([A-Z][A-Z0-9_]*)"`).FindAllStringSubmatch(string(src), -1) {
		inCode[m[1]] = true
	}
	require.NotEmpty(t, inCode, "не нашли в config.go ни одного вызова env*(...) — сломался разбор")

	for key := range inCode {
		assert.True(t, inFile[key], "%s читается кодом, но в .env.example его нет", key)
	}
	for key := range inFile {
		assert.True(t, inCode[key], "%s есть в .env.example, но код его не читает", key)
	}
}
