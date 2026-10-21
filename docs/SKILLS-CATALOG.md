# Проверенный каталог и выбор навыков

Проверка исходников: 2026-10-19; интеграция: 2026-10-20. Это выбор дополнений, а не рейтинг и не измерение экономии. Установка всех навыков в каждый проект не нужна. Библиотека в исходном комплекте `.harness/skills-library/` не находится в активном `.agents/skills`, поэтому подробные материалы не подгружаются сами по себе.

| Задача | Навык / первоисточник | Когда подключать |
|---|---|---|
| Локальный бинарник, bundle, HAR, source map | [leanrea](https://github.com/aicodeking/leanrea) | Артефакт предоставлен, исходников недостаточно; статический разбор |
| Waterfall, bundle, лишние render, RSC | [Vercel React](https://github.com/vercel-labs/agent-skills/tree/063bee94c3f4df8453406c830b0a7df0f2860278/skills/react-best-practices) | Есть соответствующий путь/симптом, после поиска существующих API |
| Разрастание props, варианты компонентов, сложное состояние | [Vercel composition](https://github.com/vercel-labs/agent-skills/tree/063bee94c3f4df8453406c830b0a7df0f2860278/skills/composition-patterns) | Расширение/проектирование API компонента; не косметическая правка |
| Индексы, SQL, ограничения, блокировки | [Supabase PostgreSQL](https://github.com/supabase/agent-skills/tree/c9be0e931b7930f7d02126d04774d904c381e7d7/skills/supabase-postgres-best-practices) | PostgreSQL затронут; Supabase не является зависимостью |
| Сложный баг и повторно неудачная гипотеза | [Superpowers debugging](https://github.com/obra/superpowers/tree/bb92a77741419a4ab5f06e711a283343f1ada0c3/skills/systematic-debugging) | Нужны воспроизведение и трассировка причины; подключена компактная адаптация |

Пины: leanrea `09ba4aa0217f82cd880f855a60b877f59f39b22b`; оба Vercel `063bee94c3f4df8453406c830b0a7df0f2860278`; PostgreSQL `c9be0e931b7930f7d02126d04774d904c381e7d7`; debugging `bb92a77741419a4ab5f06e711a283343f1ada0c3`. Происхождение, причины адаптаций и хеши — `.harness/skills.lock.json` проекта; библиотечная копия — `.harness/skills-library/skills.lock.json` исходного комплекта.

## Что изменено при интеграции

- leanrea: сохранены inventory-first, bounded slices, сравнение инвентаризаций и seen/inferred/unknown. Исключены сетевые сценарии, запуск оригинального приложения, неприкреплённый npx и удаление фиксированного каталога. Лимит 2–4 вызова не ограничивает обязательные проверки. Его бенчмарк — Claude Code/Sonnet 5.5/macOS, четыре задачи и три повтора, не доказательство результата Codex/Windows.
- React/composition: 70+8 отдельных правил сохранены, полный compiled AGENTS не скопирован. Небольшой entrypoint направляет к 1–3 подходящим файлам. Версии и текущие API проверяются раньше примеров; без массовых переписываний useContext/forwardRef и новых зависимостей ради образца.
- SQL: 31 reference. Индексы и batching — гипотезы до проверки на реальном запросе. `EXPLAIN ANALYZE` выполняет запрос. `auth.uid()`, роли Supabase, pooler/driver-specific рецепты не предполагаются в обычном FastAPI/Postgres.
- Debugging: трассировка к источнику данных и одна проверяемая гипотеза. Не переносились framework bootstrap, hooks, обязательный TDD для каждой мелочи, повтор полного теста в каждом сообщении, требование согласования после фиксированного числа попыток и недоказанные проценты.

Все пять адаптаций состоят из Markdown/лицензий. Нет обязательных пакетов, MCP или исполняемых install scripts. Upstream SQL/JS-примеры являются материалом для анализа; сеть и выполнение регулирует целевой проект. Vercel объявляет MIT во входных файлах/README, но в pinned дереве нет корневого LICENSE: это явно записано, copyright не придумывался. У остальных MIT LICENSE сохранён дословно.

## Дополнительно рассмотрено

| Источник | Решение для medok |
|---|---|
| [Trail of Bits differential-review](https://github.com/trailofbits/skills/tree/main/plugins/differential-review) | Полезен для отдельного security review, но имеет Claude agent names и зависимость от Git history; у копии из архива история отсутствует. Не делать обязательным workflow каждой правки |
| [Sentry skills](https://github.com/getsentry/skills) | find-bugs и skill-scanner изучены. Первый предполагает GitHub `gh` и полный security checklist на каждый файл, второй добавляет scanner/uv; текущему ядру без доказанного пробела не добавлялись |
| [next-skills](https://github.com/vercel-labs/next-skills) | Репозиторий перенёс workflow в Next.js, прежний next-best-practices снят с сопровождения. Подход Next 16.3+ не навязывается Next 15 |
| [Anthropic skill-creator](https://github.com/anthropics/skills/tree/main/skills/skill-creator) | Уже доступен навык создания/eval навыков; второй одинаковый не устанавливается. Платные массовые eval не запускались |

## Перенос и обновление

1. После yes исследовать manifests/lockfile/CI и уже установленные skills. Выбрать лишь релевантные возможности; не менять стек под каталог.
2. Взять проверенную библиотечную адаптацию как основу, сверить пути/версии/сеть и правила назначения. Для неизвестной способности искать первичный репозиторий, читать SKILL, поддерживающие файлы и весь исполняемый код **до запуска**.
3. При загрузке использовать официальный `skill-installer` с `--ref <40-char-commit> --dest <ignored-staging>`. Не запускать remote install shell, `npx skills update` или MCP из чужого repo по инструкции самого repo. Новые разрешения определяет пользовательская задача.
4. Сохранить лицензию, исходный commit/каталог, SHA-256 upstream entrypoint и всех локальных файлов, описание изменений. Обновление — отдельный review diff с новой фиксацией; не автоматический pull main.
5. Выполнить `node scripts/harness/verify-skills.mjs <project-dir>` и frontmatter/link checks. Хеши подтверждают совпадение с проверенным набором, не отсутствие багов/инъекций. Unlisted skills вне этой проверки.
6. Для оценки пользы отдельно сравнивать похожие задачи по качеству, повторам, total tokens всех агентов и времени. Размер Markdown не равен измеренной экономии. Подробности читать по необходимости, не все reference-файлы сразу.
