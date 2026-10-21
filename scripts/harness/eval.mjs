#!/usr/bin/env node
import { readFileSync } from 'node:fs';

// Input is explicit exported/manual data; never scans private chat/session logs.
export function summarize(runs) {
  if (!Array.isArray(runs) || !runs.length) throw new Error('Expected a non-empty array of runs');
  const groups = new Map();
  for (const run of runs) {
    if (!run.task || !run.variant || typeof run.accepted !== 'boolean') throw new Error('Each run needs task, variant, accepted:boolean');
    for (const field of ['inputTokens', 'outputTokens', 'cachedInputTokens', 'seconds', 'rework']) {
      if (run[field] != null && (!Number.isFinite(run[field]) || run[field] < 0)) throw new Error(`Invalid ${field}`);
    }
    if ((run.cachedInputTokens ?? 0) > (run.inputTokens ?? Infinity)) throw new Error('Cached input is a subset of input');
    const list = groups.get(run.variant) ?? [];
    list.push(run);
    groups.set(run.variant, list);
  }
  return [...groups].map(([variant, rows]) => {
    const accepted = rows.filter(r => r.accepted).length;
    const knownTokens = rows.filter(r => r.inputTokens != null && r.outputTokens != null);
    const totalTokens = knownTokens.reduce((sum, r) => sum + r.inputTokens + r.outputTokens, 0);
    const knownSeconds = rows.filter(r => r.seconds != null);
    return {
      variant, runs: rows.length, tasks: [...new Set(rows.map(r => r.task))].sort(), accepted,
      acceptanceRate: accepted / rows.length,
      tokenCoverage: knownTokens.length / rows.length,
      totalTokens: knownTokens.length === rows.length ? totalTokens : null,
      tokensPerAccepted: accepted && knownTokens.length === rows.length ? totalTokens / accepted : null,
      secondsPerAccepted: accepted && knownSeconds.length === rows.length ? knownSeconds.reduce((s, r) => s + r.seconds, 0) / accepted : null,
      totalRework: rows.every(r => r.rework != null) ? rows.reduce((s, r) => s + r.rework, 0) : null,
    };
  });
}

import { pathToFileURL } from 'node:url';
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    if (process.argv.length !== 3) throw new Error('Usage: node scripts/harness/eval.mjs <runs.json>');
    console.log(JSON.stringify({ ok: true, variants: summarize(JSON.parse(readFileSync(process.argv[2], 'utf8'))),
      note: 'Descriptive metrics only. Count all agents and retries. Cached tokens are already included in input; no dollar-cost inference.' }, null, 2));
  } catch (error) {
    console.log(JSON.stringify({ ok: false, error: 'eval-failed', message: error.message }));
    process.exitCode = 1;
  }
}
