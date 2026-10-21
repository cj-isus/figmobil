#!/usr/bin/env node
/**
 * map.mjs — карта проекта для coding agent (дешёвый аналог aider repo-map).
 *
 * Запуск из проекта (после setup):
 *   node scripts/harness/map.mjs [--out docs/MAP.md] [--json] [--depth 3]
 *
 * Что собирает (без зависимостей, только fs):
 *   - дерево каталогов (с фильтрацией node_modules/dist/.next и т.п.)
 *   - stack: package.json scripts + key deps + детект фреймворка
 *   - ГДЕ ИСКАТЬ переиспользуемое: компоненты, токены, правила, storybook, тесты
 *   - список правил проекта (AGENTS.md / .cursor/rules / .claude/rules / lint)
 *
 * Принцип: обзор с ограниченным деревом, а не полный индекс исходников; агент читает
 * её ПЕРЕД grep/чтением, а не сканирует репо вслепую.
 *
 * Выход: docs/MAP.md (или stdout при --out -) | JSON при --json. Ошибки — всегда JSON.
 */

import { readdirSync, readFileSync, existsSync, mkdirSync, writeFileSync, statSync } from 'node:fs';
import { join, dirname, resolve, relative } from 'node:path';
import { parseArgs } from 'node:util';

const fail = (error, message, code = 1) => {
  console.log(JSON.stringify({ ok: false, error, message }));
  process.exit(code);
};
let parsed;
try {
  parsed = parseArgs({ allowPositionals: true, options: {
    out: { type: 'string', default: 'docs/MAP.md' },
    json: { type: 'boolean', default: false },
    depth: { type: 'string', default: '3' },
  } });
} catch (e) { fail('bad-args', e.message, 2); }
if (parsed.positionals.length > 1 || !parsed.values.out) fail('bad-args', 'Expected at most one project directory and a nonempty --out.', 2);
const JSON_OUT = parsed.values.json;
const proj = resolve(parsed.positionals[0] ?? '.');
try {
  if (!statSync(proj).isDirectory()) fail('bad-project', 'Project must be a directory.', 2);
} catch (e) { fail('bad-project', e.message, 2); }

const outPath = parsed.values.out;
const DEPTH = Number(parsed.values.depth);
if (!Number.isInteger(DEPTH) || DEPTH < 1 || DEPTH > 6) fail('bad-depth', '--depth must be an integer from 1 to 6.', 2);

const SKIP = new Set(['node_modules', '.git', 'dist', 'build', 'out', '.next', '.nuxt',
  '.output', 'coverage', '.turbo', '.cache', '.artifacts', '.codex',
  '__pycache__', '.venv', 'venv', 'vendor', 'generated', '.generated', 'target',
  '.pytest_cache', '.mypy_cache', '.ruff_cache', '.tox', '.gradle', 'cache', '.harness', '.dataset-test-build']);
const skipDir = (dir, name) => SKIP.has(name) || name.startsWith('.next') || relative(proj, join(dir, name)).replaceAll('\\', '/') === 'evals/runs';

// ── Дерево ────────────────────────────────────────────────────────────────
const treeLines = [];
const TREE_LIMIT = 160;
let treeTruncated = false;
const walk = (dir, depth, maxDirCount = 40) => {
  if (depth > DEPTH) return;
  if (treeLines.length >= TREE_LIMIT) { treeTruncated = true; return; }
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true })
      .filter((e) => !(e.isDirectory() && skipDir(dir, e.name)))
      .sort((a, b) => (Number(b.isDirectory()) - Number(a.isDirectory())) || a.name.localeCompare(b.name));
  } catch { return; }
  const dirs = entries.filter((e) => e.isDirectory()).slice(0, maxDirCount);
  if (entries.filter((e) => e.isDirectory()).length > maxDirCount) treeTruncated = true;
  const files = entries.filter((e) => e.isFile());
  for (const d of dirs) {
    if (treeLines.length >= TREE_LIMIT) { treeTruncated = true; break; }
    treeLines.push(`${'  '.repeat(depth - 1)}${d.name}/`);
    walk(join(dir, d.name), depth + 1);
  }
  if (depth <= 2) {
    const re = /^(AGENTS|CLAUDE|README|package|components|tsconfig|vite|next|tailwind|biome|eslint|pyproject|requirements|Cargo|go\.mod|pnpm-workspace|lerna|turbo)/;
    for (const f of files.filter((f) => re.test(f.name)).slice(0, 15)) {
      if (treeLines.length >= TREE_LIMIT) { treeTruncated = true; break; }
      treeLines.push(`${'  '.repeat(depth - 1)}${f.name}`);
    }
  }
};
walk(proj, 1);

// ── Stack ─────────────────────────────────────────────────────────────────
let pkg = {};
let pkgErr = null;
try {
  pkg = JSON.parse(readFileSync(join(proj, 'package.json'), 'utf8').replace(/^\uFEFF/, ''));
} catch (e) { pkgErr = String(e?.message ?? e).slice(0, 80); }
const scripts = pkg.scripts ?? {};
const deps = { ...(pkg.dependencies ?? {}), ...(pkg.devDependencies ?? {}) };

// Детект тремя способами: deps → конфиг-файлы в корне → команды в scripts.
// (deps бывает пуст ещё до npm install — конфиг и скрипт уже есть.)
let rootFiles = [];
try { rootFiles = readdirSync(proj); } catch { /* proj проверен */ }
const fromDeps = [['next', 'Next.js'], ['vite', 'Vite'], ['astro', 'Astro'], ['nuxt', 'Nuxt'],
  ['react', 'React'], ['vue', 'Vue'], ['svelte', 'Svelte'], ['@angular/core', 'Angular'],
  ['tailwindcss', 'Tailwind'], ['typescript', 'TypeScript']]
  .filter(([k]) => deps[k]).map(([, v]) => v);
const fromCfg = [['next.config', 'Next.js'], ['vite.config', 'Vite'], ['astro.config', 'Astro'],
  ['nuxt.config', 'Nuxt'], ['svelte.config', 'Svelte'], ['angular.json', 'Angular'],
  ['tailwind.config', 'Tailwind'], ['tsconfig.', 'TypeScript'], ['components.json', 'shadcn']]
  .filter(([p]) => rootFiles.some((f) => f.startsWith(p))).map(([, v]) => v);
const scriptBlob = Object.values(scripts).join(' ');
const fromScripts = [[/next (dev|start|build)/, 'Next.js'], [/\bvite\b/, 'Vite'],
  [/\bastro\b/, 'Astro'], [/nuxt|nuxi/, 'Nuxt'], [/\btailwind\b/, 'Tailwind']]
  .filter(([re]) => re.test(scriptBlob)).map(([, v]) => v);
const fromManifests = [['pyproject.toml', 'Python'], ['requirements.txt', 'Python'],
  ['setup.py', 'Python'], ['Pipfile', 'Python'], ['Cargo.toml', 'Rust'], ['go.mod', 'Go']]
  .filter(([file]) => rootFiles.includes(file)).map(([, label]) => label);
const stack = [...new Set([...fromDeps, ...fromCfg, ...fromScripts, ...fromManifests])];

// ── Где искать переиспользуемое ───────────────────────────────────────────
const findFiles = (dir, re, limit = 40, acc = [], depth = 0, accept = () => true) => {
  if (depth > 6 || acc.length >= limit) return acc;
  let es; try { es = readdirSync(dir, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name)); } catch { return acc; }
  for (const e of es) {
    if (acc.length >= limit) break;
    if (e.isDirectory()) { if (!skipDir(dir, e.name)) findFiles(join(dir, e.name), re, limit, acc, depth + 1, accept); }
    else if (e.isFile() && re.test(e.name) && accept(relative(proj, join(dir, e.name)))) acc.push(relative(proj, join(dir, e.name)));
  }
  return acc;
};
const noRootAgents = (names) => names.filter((n) => !/^(AGENTS|CLAUDE)\.md$/.test(n));

// Apply the UI predicate before the limit: unrelated scripts/API files must not
// consume the budget and hide actual reusable components later in the tree.
const components = noRootAgents(findFiles(proj, /\.(?:[cm]?[jt]sx?|vue|svelte)$/i, 200, [], 0,
  (f) => /(?:^|\/)(?:components?|ui|widgets|blocks|elements|pages)\//i.test(f.replaceAll('\\', '/'))));
const stories = findFiles(proj, /\.(stories|story)\.[tj]sx?$/i, 20);
const tokens = findFiles(proj, /(tokens?|theme|palette)[^/]*\.(json|css|scss|ts|js)$/i, 15)
  .filter((f) => !/package-lock|\.min\./.test(f));
const tests = findFiles(proj, /(?:\.(?:test|spec)\.[cm]?[tj]sx?|(?:^test_|_test\.)[^/]*|_test\.go)$/i, 20);
const ruleFiles = ['AGENTS.md', 'CLAUDE.md', '.cursorrules']
  .filter((f) => existsSync(join(proj, f)))
  .concat(findFiles(proj, /(?:^AGENTS\.md$|\.rules\.md$|\.mdc$)/i, 30))
  .concat(findFiles(join(proj, '.claude', 'rules'), /\.md$/i, 15));
const lintCfg = ['eslint.config.js', 'eslint.config.mjs', '.eslintrc.cjs', 'biome.json',
  '.stylelintrc.json', 'tsconfig.json', 'components.json'].filter((f) => existsSync(join(proj, f)));
const docs = findFiles(proj, /^(ARCHITECTURE|CONTRIBUTING|DECISIONS)\.md$/i, 10)
  .concat(findFiles(join(proj, 'docs', 'adr'), /\.md$/i, 10), findFiles(join(proj, 'adr'), /\.md$/i, 10));
const manifests = findFiles(proj, /^(?:package\.json|pyproject\.toml|Cargo\.toml|go\.mod)$/i, 40);
const subprojects = [...new Set(manifests.filter((f) => dirname(f) !== '.').map((f) => dirname(f)))];
const subprojectScripts = {};
for (const manifest of manifests.filter((file) => file.endsWith('package.json') && dirname(file) !== '.')) {
  try {
    const path = join(proj, manifest);
    if (statSync(path).size > 128 * 1024) continue;
    const nested = JSON.parse(readFileSync(path, 'utf8').replace(/^\uFEFF/, ''));
    subprojectScripts[dirname(manifest)] = Object.keys(nested.scripts ?? {});
    const nestedDeps = { ...nested.dependencies, ...nested.devDependencies };
    for (const [dependency, label] of [['next', 'Next.js'], ['react', 'React'], ['vite', 'Vite'], ['vue', 'Vue'], ['typescript', 'TypeScript']]) {
      if (nestedDeps[dependency] && !stack.includes(label)) stack.push(label);
    }
  } catch { /* A malformed nested manifest must not hide the remaining map. */ }
}
const backend = ['app/api', 'app/db', 'app/tasks', 'backend', 'server', 'api', 'services', 'src/server', 'src/api', 'cmd', 'internal']
  .filter((dir) => { try { return statSync(join(proj, dir)).isDirectory(); } catch { return false; } });

// ── Сборка ────────────────────────────────────────────────────────────────
const oneLine = (title, arr, hint) => arr.length
  ? `- **${title}** (${arr.length}): ${arr.slice(0, 12).join(', ')}${arr.length > 12 ? ', …' : ''}`
  : `- **${title}**: не найдено — ${hint}`;

const md = [
  `# MAP — карта проекта (сгенерирована map.mjs; обновляй после структурных изменений)`,
  ``,
  `## Stack`,
  stack.length ? `- ${stack.join(', ')}` : `- не определён${pkgErr ? ` (package.json: ${pkgErr})` : ''}`,
  Object.keys(scripts).length
    ? `- Commands: ${Object.entries(scripts).slice(0, 8).map(([k, v]) => `${k}=\`${v}\``).join('; ')}`
    : `- Commands: npm scripts не найдены; команды определяй по манифесту проекта`,
  ...Object.entries(subprojectScripts).map(([dir, names]) => `- ${dir}/package.json: ${names.slice(0, 12).join(', ')}${names.length > 12 ? ', …' : ''} (наличие не доказывает работоспособность)`),
  ``,
  `## ГДЕ ИСКАТЬ (прочитать перед созданием нового)`,
  oneLine('Компоненты (переиспользовать, не дублировать)', components, 'уточни точечным поиском, если задача касается UI'),
  oneLine('Storybook / stories (каталог UI)', stories, 'может быть неприменимо к этому проекту'),
  oneLine('Токены/темы', tokens, 'уточни при работе над UI'),
  oneLine('Тесты', tests, 'определи подходящую проверку для задачи'),
  oneLine('Backend-каталоги (эвристика)', backend, 'уточни поиском'),
  oneLine('Подпроекты по манифестам', subprojects, 'вложенные манифесты не найдены'),
  ``,
  `## Правила проекта (читай, не нарушай)`,
  ruleFiles.length ? [...new Set(ruleFiles)].map((f) => `- ${f}`).join('\n')
    : `- Файлы правил не найдены; проверь применимые инструкции перед изменениями`,
  lintCfg.length ? `- Линтеры/конфиги: ${lintCfg.join(', ')}` : `- Линтеры не найдены`,
  docs.length ? `- Docs: ${docs.join(', ')}` : `- Документы архитектуры не найдены`,
  ``,
  `## Дерево (глубина ${DEPTH})`,
  '```',
  ...treeLines,
  ...(treeTruncated ? ['… дерево сокращено; используй точечный поиск'] : []),
  '```',
  ``,
  `Это ограниченный эвристический обзор, не полный индекс и не доказательство отсутствия файла.`,
  `Навигация: карта → точечный rg → чтение нужных файлов. Репо целиком не дампить.`,
].join('\n');

if (JSON_OUT) {
  console.log(JSON.stringify({
    ok: true, project: proj, stack, scripts: Object.keys(scripts),
    components, stories, tokens, tests, rules: [...new Set(ruleFiles)], lint: lintCfg, docs, backend, subprojects, manifests, subprojectScripts,
    treeLines: treeLines.length, treeTruncated, mapChars: md.length,
    scope: 'bounded heuristic overview; use targeted search for completeness',
  }, null, 2));
} else if (outPath === '-') {
  console.log(md);
} else {
  const outAbs = resolve(proj, outPath);
  try {
    mkdirSync(dirname(outAbs), { recursive: true });
    writeFileSync(outAbs, md);
  } catch (e) { fail('write-failed', e.message); }
  console.log(JSON.stringify({ ok: true, out: outAbs, treeLines: treeLines.length, chars: md.length, components: components.length }));
}

