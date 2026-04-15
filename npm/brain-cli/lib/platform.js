'use strict';

const PLATFORM_MAP = {
  linux: 'linux',
  darwin: 'darwin',
  win32: 'windows',
  freebsd: 'freebsd'
};

const ARCH_MAP = {
  x64: 'amd64',
  arm64: 'arm64'
};

function resolveTarget(platform = process.platform, arch = process.arch, version = '0.7.0') {
  const goos = PLATFORM_MAP[platform];
  if (!goos) {
    throw new Error(`unsupported platform: ${platform}`);
  }

  const goarch = ARCH_MAP[arch];
  if (!goarch) {
    throw new Error(`unsupported architecture: ${arch}`);
  }

  const ext = goos === 'windows' ? '.zip' : '.tar.gz';
  const binary = goos === 'windows' ? 'brain.exe' : 'brain';
  const stageName = `brain_${version}_${goos}_${goarch}`;
  const assetName = `${stageName}${ext}`;

  return {
    version,
    platform,
    arch,
    goos,
    goarch,
    binary,
    stageName,
    assetName,
    archiveExt: ext
  };
}

module.exports = {
  resolveTarget
};
