# Установленная обвязка

Профиль: **core, стадия планирования**, 2026-10-20. Установка выполнена после явного запроса пользователя. Файлы источников не изменялись.

## Происхождение и состав

Базовый комплект: `D:/User/Documents/codex-harness-demo`; применён `scripts/setup.mjs <target> --profile core --apply` после чистого dry-run. Побайтовые SHA-256 файлов и managed-блока AGENTS записаны в [.harness/installed.json](../.harness/installed.json).

Установлены map, doctor, eval, project-memory, verifier; документация процесса; четыре локальных entrypoint skills: repo-recon, memory-loop, frontend-postgres, project-memory. Последний навык хранит checkpoint отдельно от файлов проекта. `frontend-postgres` не устанавливает PostgreSQL и не означает выбранный стек. `eval` не запускался на сравнительных данных: экономия токенов не измерялась.

Дополнения `scripts/project/check.mjs` и тесты проверяют текущий реестр и границы фазы. Запуск без npm install:

```powershell
node scripts/harness/map.mjs --out docs/MAP.md
node scripts/project/check.mjs
node --test scripts/project/check.test.mjs
node scripts/harness/doctor.mjs
```

Локальный check — защита от случайного рассогласования процесса. Он не контролирует организационные политики и не доказывает фактическое согласие человека. Наличие файлов не является проверкой работающего приложения.

## Использование «ФРОНТОВОЙ»

Изучен `D:/User/Documents/ФРОНТОВАЯ`, версия 1.3.0. Агент запустил её `node scripts/verify.mjs --json`: код 0, 26 обязательных файлов, 7 skills, 14 Markdown, `problems: []`.

Её установщик поверх core не запускался: он копирует общий map/doctor и обновляет инструкции от своего маркера до конца файла. Полезные practices — визуальный контракт, browser capture, triage и baseline после принятия UI. Набор подключается выборочно в P3, с предварительным сравнением версий скриптов.

Обнаруженное ограничение: README «ФРОНТОВОЙ» трактует matchRatio как диагностику, а `scripts/run-loop.mjs` использует обязательный порог, по умолчанию 0.98. Такой порог не перенесён в figmobil. Визуальное совпадение не подтверждает корректность импорта и криптографии.

## Готовые skills и инструменты

Просмотрен локальный [каталог](SKILLS-CATALOG.md). React/composition skills откладываются до утверждения React; PostgreSQL skills не нужны без БД. Binary analysis и внешний API-assist для этой фазы не нужны. Новые внешние skills не устанавливались: `.harness/skills.lock.json` не создавался, отдельный `verify-skills.mjs` без такого набора неприменим. Происхождение установленных core skills проверяется через installed.json.

Нет автоматической установки dependencies, браузеров, MCP, hooks, внешних моделей или бинарников. Глобальные модель, разрешения и настройки не менялись. Git init и origin не создавались. CLI project-memory использовался адресно с реальным CODEX_THREAD_ID; автоматический recall через hooks в этой фазе не проверялся и не заявляется.

## Обновление

Специфичные правила figmobil находятся вне managed-блока AGENTS. Перед обновлением harness сначала dry-run и просмотр конфликтов. `docs/STATE.md` и `docs/LESSONS.md` изменяемые и сохраняются установщиком; их исходные хеши не используются как требование неизменности. Глобальная документация комплекта — справочный материал, её общие примеры моделей/стеков не считаются текущими решениями figmobil.
