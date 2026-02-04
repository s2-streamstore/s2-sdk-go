---
name: release
description: Release a new version of the Go SDK
---

# /release

Release a new version of the S2 Go SDK.

## How releases work

This project uses git tags for versioning and git-cliff for automatic changelog generation. The release workflow is triggered when a version tag (`vX.Y.Z`) is pushed to the repository.

**Release automation:**
- `task release:prepare` - Creates a release branch with updated CHANGELOG.md
- `task release:tag` - Tags the release after the PR is merged
- GitHub Actions creates the release and refreshes Go module docs on tag push

## Usage

```
/release [VERSION]
```

If VERSION is not provided, determine the next version by analyzing commits since the last release.

## Steps

### 1. Determine the version

If no version is provided, analyze commits to suggest one:

```bash
git fetch origin --tags
git log $(git describe --tags --abbrev=0)..HEAD --oneline
```

Apply semantic versioning rules:
- `fix:` commits → patch bump (e.g., v0.11.9 → v0.11.10)
- `feat:` commits → minor bump (e.g., v0.11.9 → v0.12.0)
- `feat!:` or `BREAKING CHANGE:` → major bump (e.g., v0.11.9 → v1.0.0)

Get the current version:
```bash
git describe --tags --abbrev=0
```

### 2. Check for existing release PR

```bash
gh pr list --search "release-v" --state open
```

If a release PR already exists, skip to step 5.

### 3. Prepare the release

Run the prepare task (creates branch with updated changelog):

```bash
task release:prepare VERSION=X.Y.Z
```

This will:
- Checkout latest main
- Create a release branch (`release-vX.Y.Z-<timestamp>`)
- Generate CHANGELOG.md using git-cliff
- Commit with message `chore(release): vX.Y.Z`

### 4. Push and create PR

```bash
git push -u origin HEAD
gh pr create --title "chore(release): vX.Y.Z" --body "$(cat <<'EOF'
## Release vX.Y.Z

### Changelog

See CHANGELOG.md for details.

### Checklist

- [ ] Changelog looks correct
- [ ] CI passes
- [ ] Ready to release
EOF
)"
```

### 5. Verify the changelog

Review the changelog diff:
```bash
gh pr diff <PR_NUMBER> -- CHANGELOG.md
```

Compare with commits since last release to ensure nothing is missing.

### 6. Run tests

Ensure all tests pass:
```bash
task test
task lint
```

### 7. Merge the PR

Once CI passes and changelog is verified:
```bash
gh pr merge <PR_NUMBER> --squash
```

### 8. Tag the release

After the PR is merged, create and push the tag:

```bash
task release:tag VERSION=X.Y.Z
git push origin vX.Y.Z
```

This triggers the GitHub release workflow which:
- Creates a GitHub release with changelog content
- Refreshes Go module documentation on pkg.go.dev

## Verify release

Check the release was created:
```bash
gh release view vX.Y.Z
```

Check workflow status:
```bash
gh run list --workflow=release.yml
```

Verify Go module is available:
```bash
curl -s "https://proxy.golang.org/github.com/s2-streamstore/s2-sdk-go/@v/vX.Y.Z.info"
```

## Notes

- Version format: `vX.Y.Z` (semver with `v` prefix)
- Changelog is auto-generated from conventional commits
- `chore:` commits are excluded from the changelog
- The release workflow runs automatically on tag push
