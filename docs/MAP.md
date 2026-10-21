# MAP — карта проекта (сгенерирована map.mjs; обновляй после структурных изменений)

## Stack
- Go
- Commands: check=`node scripts/project/verify.mjs`; test=`go test ./...`; test:harness=`node --test scripts/project/check.test.mjs`; test:ui=`node --test internal/webui/app.test.mjs internal/webui/view-model.test.mjs`; build=`go build -trimpath -o dist/figmobil.exe ./cmd/figmobil`

## ГДЕ ИСКАТЬ (прочитать перед созданием нового)
- **Компоненты (переиспользовать, не дублировать)**: не найдено — уточни точечным поиском, если задача касается UI
- **Storybook / stories (каталог UI)**: не найдено — может быть неприменимо к этому проекту
- **Токены/темы**: не найдено — уточни при работе над UI
- **Тесты** (10): internal\compare\budget_test.go, internal\compare\compare_test.go, internal\compare\path_test.go, internal\compare\path_windows_test.go, internal\server\integration_test.go, internal\server\server_test.go, internal\transferlimit\gate_test.go, internal\webui\app.test.mjs, internal\webui\view-model.test.mjs, scripts\project\check.test.mjs
- **Backend-каталоги (эвристика)** (2): cmd, internal
- **Подпроекты по манифестам**: не найдено — вложенные манифесты не найдены

## Правила проекта (читай, не нарушай)
- AGENTS.md
- Линтеры не найдены
- Docs: docs\ARCHITECTURE.md, docs\DECISIONS.md

## Дерево (глубина 3)
```
.agents/
  skills/
    frontend-postgres/
    memory-loop/
    project-memory/
    repo-recon/
cmd/
  figmobil/
docs/
internal/
  compare/
  server/
  transferlimit/
  webui/
scripts/
  harness/
  project/
spec/
AGENTS.md
go.mod
package.json
README.md
```

Это ограниченный эвристический обзор, не полный индекс и не доказательство отсутствия файла.
Навигация: карта → точечный rg → чтение нужных файлов. Репо целиком не дампить.