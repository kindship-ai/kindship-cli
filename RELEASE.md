# Kindship CLI Release Manual

This document describes the release process for the Kindship CLI.

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Release Process](#release-process)
- [Post-Release Verification](#post-release-verification)
- [Rollback Procedure](#rollback-procedure)
- [Troubleshooting](#troubleshooting)

## Overview

The Kindship CLI uses [GoReleaser](https://goreleaser.com/) to automatically build and publish releases for multiple platforms. Releases are triggered by pushing a version tag to the repository.

### Supported Platforms

The CLI is built for 6 platform combinations:

- **Linux**: amd64, arm64
- **macOS (Darwin)**: amd64, arm64
- **Windows**: amd64, arm64

## Prerequisites

Before creating a release, ensure:

1. **All changes are merged to `main`**
   ```bash
   git checkout main
   git pull origin main
   ```

2. **Tests pass** (if test suite exists)
   ```bash
   go test ./...
   ```

3. **Code builds successfully**
   ```bash
   go build -o kindship
   ```

4. **Version number decided**
   - Follow [Semantic Versioning](https://semver.org/)
   - Format: `vMAJOR.MINOR.PATCH` (e.g., `v0.1.3`)
   - Increment appropriately:
     - **PATCH**: Bug fixes, minor changes
     - **MINOR**: New features, backwards compatible
     - **MAJOR**: Breaking changes

## Release Process

### Step 1: Update Version (if applicable)

If the CLI has internal version tracking, update it:

```bash
# Example: Update version in cmd/version.go or similar
# Commit the version bump
git add .
git commit -m "chore: bump version to v0.1.3"
git push origin main
```

### Step 2: Create and Push Git Tag

```bash
# Create annotated tag
git tag -a v0.1.3 -m "Release v0.1.3 - Multi-platform update support"

# Push tag to GitHub
git push origin v0.1.3
```

### Step 3: Monitor GitHub Actions

The release process is automated via GitHub Actions:

1. Navigate to: https://github.com/kindship-ai/kindship-cli/actions
2. Find the workflow run triggered by your tag
3. Monitor build progress (~5-10 minutes)
4. Ensure all platform builds succeed

### Step 4: Verify Release Assets

Once GitHub Actions completes:

```bash
# View release
gh release view v0.1.3

# Or visit:
# https://github.com/kindship-ai/kindship-cli/releases/tag/v0.1.3
```

**Expected assets:**
```
kindship_0.1.3_checksums.txt
kindship_0.1.3_darwin_amd64.tar.gz
kindship_0.1.3_darwin_arm64.tar.gz
kindship_0.1.3_linux_amd64.tar.gz
kindship_0.1.3_linux_arm64.tar.gz
kindship_0.1.3_windows_amd64.zip
kindship_0.1.3_windows_arm64.zip
kindship (loose binary for backwards compatibility)
```

### Step 5: Test Installation

Test the installation on at least one platform:

```bash
# macOS
curl -fsSL https://kindship.ai/install.sh | bash
kindship version

# Should show: v0.1.3
```

## Post-Release Verification

### 1. Test Update Command

Verify the update mechanism works:

```bash
# Install previous version first
# Then run update
kindship update
kindship version  # Should show new version
```

### 2. Test on Multiple Platforms

If possible, verify on:
- ✅ macOS (arm64 or amd64)
- ✅ Linux (via Docker or VM)
- ✅ Windows (via VM or WSL)

### 3. Monitor Download Stats

```bash
# Check download statistics
gh api repos/kindship-ai/kindship-cli/releases/latest \
  | jq '.assets[] | {name, download_count}'
```

### 4. Check Web API Compatibility

Verify the web API serves correct binaries:

```bash
# Test each platform
curl -I "https://kindship.ai/cli/kindship?os=darwin&arch=arm64" \
  | grep "x-version"

# Should return: x-version: v0.1.3
```

## Rollback Procedure

If a release has critical issues:

### Option 1: Delete Release (Pre-Production)

```bash
# Delete tag and release
gh release delete v0.1.3 --yes
git tag -d v0.1.3
git push origin :refs/tags/v0.1.3
```

### Option 2: Create Hotfix Release

```bash
# Fix the issue
git checkout main
# Make fixes
git commit -m "fix: critical issue in v0.1.3"
git push

# Create new patch version
git tag -a v0.1.4 -m "Release v0.1.4 - Hotfix for v0.1.3"
git push origin v0.1.4
```

### Option 3: Mark Release as Pre-release

```bash
# Mark the release as pre-release to warn users
gh release edit v0.1.3 --prerelease
```

## Troubleshooting

### GitHub Actions Build Fails

**Symptom**: Build fails during GitHub Actions workflow

**Solutions**:
1. Check the Actions logs for specific errors
2. Ensure `.goreleaser.yaml` is valid
3. Verify all dependencies are accessible
4. Check if GitHub token permissions are correct

### Missing Platform Binaries

**Symptom**: Not all 6 platform binaries are created

**Solutions**:
1. Check `.goreleaser.yaml` `builds` configuration
2. Ensure `goos` and `goarch` combinations are correct
3. Verify build constraints don't exclude platforms

### Web API Returns Wrong Version

**Symptom**: API still returns old version after release

**Solutions**:
1. Verify tag was pushed: `git ls-remote --tags origin`
2. Check GitHub release exists: `gh release list`
3. Clear CDN cache (Vercel auto-clears, but check if needed)
4. Ensure web API is fetching `/releases/latest`

### Users Can't Update

**Symptom**: `kindship update` fails for users

**Solutions**:
1. Verify update command includes platform detection:
   ```bash
   # Should see platform in output
   kindship update
   ```
2. Check web API logs for errors:
   ```bash
   # From kindship-vercel repo
   pnpm logs -- --level=error --last=1h
   ```
3. Test update endpoint manually:
   ```bash
   curl -I "https://kindship.ai/cli/kindship?os=darwin&arch=arm64"
   ```

## Release Checklist

Use this checklist for each release:

**Pre-Release:**
- [ ] All changes merged to `main`
- [ ] Tests passing
- [ ] Version number decided
- [ ] CHANGELOG updated (if maintained)

**Release:**
- [ ] Version updated in code (if applicable)
- [ ] Tag created and pushed
- [ ] GitHub Actions workflow succeeded
- [ ] All 6 platform binaries published
- [ ] Checksums file present
- [ ] Loose binary present (backwards compatibility)

**Post-Release:**
- [ ] Installation tested on at least 1 platform
- [ ] Update command tested
- [ ] Web API returns correct version
- [ ] Release notes published (optional)
- [ ] Documentation updated (if needed)

**Monitoring (24h):**
- [ ] No spike in error rates
- [ ] Download counts increasing
- [ ] No critical issues reported

## Emergency Contacts

- **Repository**: https://github.com/kindship-ai/kindship-cli
- **Web API Repo**: https://github.com/kindship-ai/kindship-vercel
- **Issues**: https://github.com/kindship-ai/kindship-cli/issues

## Related Documentation

- [GoReleaser Configuration](./.goreleaser.yaml)
- [GitHub Actions Workflow](./.github/workflows/release.yml)
- [Web API Endpoint](../kindship-vercel/apps/web/app/cli/kindship/route.ts)
- [Installation Script](../kindship-vercel/apps/web/app/install.sh/route.ts)
