#!/usr/bin/env node
'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');

const packageRoot = path.resolve(__dirname, '..');
const manifestPath = path.join(packageRoot, 'runtime', 'install-manifest.json');

function main() {
  if (!fs.existsSync(manifestPath)) {
    console.error('[brain-cli] runtime not installed. Reinstall the package to download the matching release.');
    process.exit(1);
  }

  const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
  const binaryPath = path.join(packageRoot, 'runtime', manifest.stageName, manifest.binary);

  if (!fs.existsSync(binaryPath)) {
    console.error(`[brain-cli] binary not found: ${binaryPath}`);
    process.exit(1);
  }

  const child = spawn(binaryPath, process.argv.slice(2), {
    stdio: 'inherit',
    env: process.env
  });

  child.on('exit', (code, signal) => {
    if (signal) {
      process.kill(process.pid, signal);
      return;
    }
    process.exit(code ?? 0);
  });

  child.on('error', err => {
    console.error(`[brain-cli] failed to start ${binaryPath}: ${err.message}`);
    process.exit(1);
  });
}

main();
