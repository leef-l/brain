'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { resolveTarget } = require('../lib/platform');

test('resolveTarget maps linux x64 to release asset', () => {
  const target = resolveTarget('linux', 'x64', '0.7.0');
  assert.equal(target.assetName, 'brain_0.6.0_linux_amd64.tar.gz');
  assert.equal(target.binary, 'brain');
});

test('resolveTarget maps windows arm64 to release asset', () => {
  const target = resolveTarget('win32', 'arm64', '0.7.0');
  assert.equal(target.assetName, 'brain_0.6.0_windows_arm64.zip');
  assert.equal(target.binary, 'brain.exe');
});

test('resolveTarget rejects unsupported platform', () => {
  assert.throws(() => resolveTarget('sunos', 'x64', '0.7.0'), /unsupported platform/);
});
