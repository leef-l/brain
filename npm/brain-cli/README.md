# `@leef-l/brain-cli`

Minimal npm/pnpm wrapper package for the official BrainKernel CLI release
binaries.

## Install

```bash
pnpm add -g @leef-l/brain-cli
```

During `postinstall`, the package:

1. detects the current platform and architecture
2. downloads the matching GitHub Release asset
3. verifies the archive against `SHA256SUMS`
4. extracts the runtime locally

After installation:

```bash
brain version
```

## Environment Overrides

- `BRAIN_INSTALL_REPO`
  Override the GitHub repository. Default: `leef-l/brain`
- `BRAIN_INSTALL_VERSION`
  Override the release version to download. Default: package version
- `BRAIN_INSTALL_BASE_URL`
  Override the download base URL entirely
- `BRAIN_INSTALL_SKIP_DOWNLOAD=1`
  Skip `postinstall` downloading
- `BRAIN_INSTALL_SKIP_CHECKSUM=1`
  Skip checksum verification
