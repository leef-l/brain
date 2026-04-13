'use strict';

const fs = require('node:fs');
const fsp = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const https = require('node:https');
const crypto = require('node:crypto');
const AdmZip = require('adm-zip');
const tar = require('tar');
const { resolveTarget } = require('../lib/platform');

const pkg = require('../package.json');

const PACKAGE_ROOT = path.resolve(__dirname, '..');
const RUNTIME_ROOT = path.join(PACKAGE_ROOT, 'runtime');
const MANIFEST_PATH = path.join(RUNTIME_ROOT, 'install-manifest.json');
const DEFAULT_REPO = process.env.BRAIN_INSTALL_REPO || 'leef-l/brain';
const DEFAULT_VERSION = process.env.BRAIN_INSTALL_VERSION || pkg.version;
const DEFAULT_BASE_URL =
  process.env.BRAIN_INSTALL_BASE_URL ||
  `https://github.com/${DEFAULT_REPO}/releases/download/v${DEFAULT_VERSION}`;

async function main() {
  if (process.env.BRAIN_INSTALL_SKIP_DOWNLOAD === '1') {
    console.log('[brain-cli] skipping download because BRAIN_INSTALL_SKIP_DOWNLOAD=1');
    return;
  }

  const target = resolveTarget(process.platform, process.arch, DEFAULT_VERSION);
  const assetUrl = `${DEFAULT_BASE_URL}/${target.assetName}`;
  const checksumsUrl = `${DEFAULT_BASE_URL}/SHA256SUMS`;

  console.log(`[brain-cli] installing ${target.assetName}`);
  console.log(`[brain-cli] source ${assetUrl}`);

  const tmpDir = await fsp.mkdtemp(path.join(os.tmpdir(), 'brain-cli-'));
  const archivePath = path.join(tmpDir, target.assetName);
  const checksumsPath = path.join(tmpDir, 'SHA256SUMS');

  try {
    await download(assetUrl, archivePath);
    await download(checksumsUrl, checksumsPath);
    await verifyChecksum(archivePath, checksumsPath, target.assetName);
    await extractArchive(archivePath, target.archiveExt, RUNTIME_ROOT);
    await writeManifest(target);
    await ensureExecutable(target);
  } finally {
    await fsp.rm(tmpDir, { recursive: true, force: true });
  }
}

async function writeManifest(target) {
  await fsp.mkdir(RUNTIME_ROOT, { recursive: true });
  const manifest = {
    version: target.version,
    stageName: target.stageName,
    binary: target.binary,
    assetName: target.assetName,
    installedAt: new Date().toISOString()
  };
  await fsp.writeFile(MANIFEST_PATH, JSON.stringify(manifest, null, 2) + '\n', 'utf8');
}

async function ensureExecutable(target) {
  if (target.goos === 'windows') {
    return;
  }
  const binaryPath = path.join(RUNTIME_ROOT, target.stageName, target.binary);
  await fsp.chmod(binaryPath, 0o755);
}

async function extractArchive(archivePath, archiveExt, runtimeRoot) {
  await fsp.rm(runtimeRoot, { recursive: true, force: true });
  await fsp.mkdir(runtimeRoot, { recursive: true });

  if (archiveExt === '.zip') {
    const zip = new AdmZip(archivePath);
    zip.extractAllTo(runtimeRoot, true);
    return;
  }

  await tar.x({
    file: archivePath,
    cwd: runtimeRoot
  });
}

async function verifyChecksum(archivePath, checksumsPath, assetName) {
  if (process.env.BRAIN_INSTALL_SKIP_CHECKSUM === '1') {
    console.log('[brain-cli] skipping checksum verification because BRAIN_INSTALL_SKIP_CHECKSUM=1');
    return;
  }

  const checksums = await fsp.readFile(checksumsPath, 'utf8');
  const expected = parseChecksum(checksums, assetName);
  if (!expected) {
    throw new Error(`checksum for ${assetName} not found in SHA256SUMS`);
  }

  const actual = await sha256File(archivePath);
  if (actual !== expected) {
    throw new Error(`checksum mismatch for ${assetName}: expected ${expected}, got ${actual}`);
  }
}

function parseChecksum(contents, assetName) {
  for (const line of contents.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed) {
      continue;
    }
    const parts = trimmed.split(/\s+/);
    if (parts.length < 2) {
      continue;
    }
    const [hash, file] = parts;
    if (file === assetName) {
      return hash;
    }
  }
  return '';
}

async function sha256File(filePath) {
  return new Promise((resolve, reject) => {
    const hash = crypto.createHash('sha256');
    const stream = fs.createReadStream(filePath);
    stream.on('error', reject);
    stream.on('data', chunk => hash.update(chunk));
    stream.on('end', () => resolve(hash.digest('hex')));
  });
}

async function download(url, destination, redirects = 5) {
  await fsp.mkdir(path.dirname(destination), { recursive: true });

  return new Promise((resolve, reject) => {
    const client = url.startsWith('http://') ? http : https;
    const request = client.get(url, response => {
      if (
        response.statusCode &&
        response.statusCode >= 300 &&
        response.statusCode < 400 &&
        response.headers.location
      ) {
        response.resume();
        if (redirects <= 0) {
          reject(new Error(`too many redirects for ${url}`));
          return;
        }
        const redirected = new URL(response.headers.location, url).toString();
        download(redirected, destination, redirects - 1).then(resolve, reject);
        return;
      }

      if (response.statusCode !== 200) {
        response.resume();
        reject(new Error(`download failed for ${url}: HTTP ${response.statusCode}`));
        return;
      }

      const file = fs.createWriteStream(destination);
      response.pipe(file);
      file.on('finish', () => {
        file.close(resolve);
      });
      file.on('error', err => {
        file.close(() => reject(err));
      });
    });

    request.on('error', reject);
  });
}

main().catch(err => {
  console.error(`[brain-cli] install failed: ${err.message}`);
  process.exitCode = 1;
});
