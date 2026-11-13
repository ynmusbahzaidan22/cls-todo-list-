import { readdirSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { resolve } from 'node:path';

const skippedDirectories = new Set(['.git', '.cache', 'node_modules']);
const files = [];

function collect(directory) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (!skippedDirectories.has(entry.name)) collect(resolve(directory, entry.name));
    } else if (/\.(?:js|mjs)$/.test(entry.name)) {
      files.push(resolve(directory, entry.name));
    }
  }
}

collect(process.cwd());
for (const file of files) {
  const result = spawnSync(process.execPath, ['--check', file], { stdio: 'inherit' });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status ?? 1);
}

console.log(`node --check passed for ${files.length} JavaScript files.`);
