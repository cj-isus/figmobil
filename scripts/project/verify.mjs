#!/usr/bin/env node
// Runs only this application's synthetic checks; never imported project code.
import { execFileSync } from 'node:child_process';
import { copyFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const evidence = resolve(root, '.artifacts/local-verification.json');
const checks = [
  { name: 'planning-and-harness', command: process.execPath, args: ['scripts/project/check.mjs'] },
  { name: 'harness-regressions', command: process.execPath, args: ['--test', 'scripts/project/check.test.mjs'] },
  { name: 'ui-view-model', command: process.execPath, args: ['--test', 'internal/webui/view-model.test.mjs'] },
  { name: 'ui-controller', command: process.execPath, args: ['--test', 'internal/webui/app.test.mjs'] },
  { name: 'channel-ui-model', command: process.execPath, args: ['--test', 'internal/webui/channel-model.test.mjs'] },
  { name: 'channel-ui-controller', command: process.execPath, args: ['--test', 'internal/webui/channel.test.mjs'] },
  { name: 'go-format', command: 'gofmt', args: ['-l', 'cmd', 'internal'], emptyOutput: true },
  { name: 'go-tests', command: 'go', args: ['test', './...'] },
  { name: 'go-vet', command: 'go', args: ['vet', './...'] },
  { name: 'windows-build', command: 'go', args: ['build', '-trimpath', '-o', 'dist/figmobil.exe', './cmd/figmobil'] },
];

mkdirSync(dirname(evidence), { recursive: true });
mkdirSync(resolve(root, 'dist'), { recursive: true });
const results = [];
for (const check of checks) {
  const started = Date.now();
  try {
    const output = execFileSync(check.command, check.args, {
      cwd: root, encoding: 'utf8', timeout: 180_000, windowsHide: true, maxBuffer: 8 * 1024 * 1024,
      env: { ...process.env, GOTOOLCHAIN: 'local', GOPROXY: 'off' },
    });
    if (check.emptyOutput && output.trim()) throw new Error(`Formatting required:\n${output}`);
    results.push({ name: check.name, command: [check.command, ...check.args], ok: true, elapsedMs: Date.now() - started, output });
  } catch (error) {
    results.push({ name: check.name, command: [check.command, ...check.args], ok: false, elapsedMs: Date.now() - started, output: String(error.stdout ?? ''), error: String(error.stderr ?? error.message) });
  }
  console.log(JSON.stringify({ name: check.name, ok: results.at(-1).ok }));
}
const report = { ok: results.every(check => check.ok), scope: 'Local manual channel, Windows key/file operations and synthetic fixtures. Browser and real Figma/two-PC transport remain unverified.', checks: results };
if (report.ok) copyFileSync(resolve(root, 'internal/secure/THIRD-PARTY-NOTICES.txt'), resolve(root, 'dist/THIRD-PARTY-NOTICES.txt'));
writeFileSync(evidence, `${JSON.stringify(report, null, 2)}\n`);
process.exitCode = report.ok ? 0 : 1;
