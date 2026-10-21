#!/usr/bin/env node
import { existsSync, readFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { createRequire } from 'node:module';
import { parseArgs } from 'node:util';
import { spawnSync } from 'node:child_process';

try {
  const { values, positionals } = parseArgs({ options: { visual: { type: 'boolean' } }, allowPositionals: true });
  if (positionals.length > 1) throw new Error('Usage: doctor.mjs [project-dir] [--visual]');
  const root = resolve(positionals[0] ?? '.');
  const checks = [];
  const add = (id, status, detail) => checks.push({ id, status, detail });
  const localRequire = createRequire(join(root, 'package.json'));
  const read = (file) => readFileSync(join(root, file), 'utf8').replace(/^\uFEFF/, '');
  const [major, minor] = process.versions.node.split('.').map(Number);
  add('node', major > 22 || (major === 22 && minor >= 13) ? 'pass' : 'error', `${process.version}; harness requires >=22.13`);
  for (const file of ['AGENTS.md', 'docs/MAP.md', 'docs/LESSONS.md', 'docs/HARNESS.md']) {
    add(file, existsSync(join(root, file)) ? 'pass' : 'error', 'Required harness entrypoint');
  }
  if (existsSync(join(root, 'AGENTS.md'))) {
    const text = read('AGENTS.md');
    const lines = text.split(/\r?\n/).length;
    add('instruction-size', lines <= 200 ? 'pass' : 'warn', { lines, bytes: Buffer.byteLength(text) });
  }
  const git = spawnSync('git', ['-C', root, 'rev-parse', '--is-inside-work-tree'], { encoding: 'utf8', windowsHide: true });
  add('git', git.status === 0 ? 'pass' : 'warn', git.status === 0 ? 'Worktree available' : 'No Git history available; initialize separately if needed');
  if (existsSync(join(root, 'package.json'))) {
    const pkg = JSON.parse(read('package.json'));
    add('commands', 'info', pkg.scripts ?? {});
    const locks = ['package-lock.json', 'pnpm-lock.yaml', 'yarn.lock', 'bun.lock', 'bun.lockb'].filter(f => existsSync(join(root, f)));
    add('lockfile', locks.length === 1 ? 'pass' : 'warn', locks.length ? locks : 'No lockfile; reproducibility not established');
    // Detect only known direct executables. This is not a general shell/script validator.
    const executables = { vite: 'vite', eslint: 'eslint', tsc: 'typescript', vitest: 'vitest' };
    for (const [binary, module] of Object.entries(executables)) {
      if (!Object.values(pkg.scripts ?? {}).some(cmd => new RegExp(`(^|[ &])${binary}([ &]|$)`).test(cmd))) continue;
      try { localRequire.resolve(module); add(`dependency:${module}`, 'pass', 'Resolvable from project'); }
      catch { add(`dependency:${module}`, 'error', 'Declared command cannot resolve its tool; install project dependencies'); }
    }
  } else {
    add('commands', 'info', 'No package.json; use native manifest/CI commands. Node is needed only for harness tools.');
  }
  if (values.visual) {
    for (const module of ['playwright', 'pngjs', 'pixelmatch']) {
      try { localRequire.resolve(module); add(`visual:${module}`, 'pass', 'Resolvable'); }
      catch { add(`visual:${module}`, 'error', 'Install visual profile dependencies in the target project'); }
    }
    try {
      const { chromium } = localRequire('playwright');
      add('chromium', existsSync(chromium.executablePath()) ? 'pass' : 'warn', 'Browser binary check only; use npx playwright install chromium if missing');
    } catch { /* Dependency check above reports this. */ }
    add('reference', existsSync(join(root, 'design/active/approved.png')) ? 'pass' : 'warn', 'A reference is needed only for image-based work');
  }
  const ok = !checks.some(c => c.status === 'error');
  console.log(JSON.stringify({ ok, root, checks, note: 'Static diagnostics; successful build/tests and model availability are not implied.' }, null, 2));
  process.exitCode = ok ? 0 : 1;
} catch (error) {
  console.log(JSON.stringify({ ok: false, error: 'doctor-failed', message: error.message }));
  process.exitCode = 1;
}
