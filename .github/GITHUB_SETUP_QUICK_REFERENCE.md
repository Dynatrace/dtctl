# GitHub Repository Setup - Quick Reference

This is a quick reference for setting up the GitHub repository metadata. For detailed instructions, see [REPOSITORY_SETUP.md](REPOSITORY_SETUP.md).

## GitHub About Section

**Location**: Repository homepage → Settings icon (⚙️) next to "About"

### Configuration

**Description:**
```
kubectl-inspired CLI for managing Dynatrace platform resources from your terminal
```

**Website:**
```
https://github.com/Dynatrace/dtctl
```

**Topics (copy-paste ready):**
```
dynatrace, cli, kubectl, kubernetes, devops, observability, golang, command-line, workflow-automation, dql, dashboards, monitoring, platform-engineering, developer-tools
```

**Features:**
- ✅ Releases
- ❌ Packages
- ❌ Deployments

---

## Branch Protection (Main Branch)

**Location**: Settings → Branches → Add rule for `main`

**Quick checklist:**
- ✅ Require pull request (1 approval)
- ✅ Require status checks: `build`, `test`, `lint`, `security`
- ✅ Require conversation resolution
- ✅ Restrict push access
- ❌ Allow force pushes
- ❌ Allow deletions

---

## Security Settings

**Location**: Settings → Security & analysis

**Enable all:**
- ✅ Dependency graph
- ✅ Dependabot alerts
- ✅ Dependabot security updates
- ✅ Code scanning (CodeQL)
- ✅ Secret scanning
- ✅ Secret scanning push protection
- ✅ Private vulnerability reporting

---

## Community Settings

**Location**: Settings → General

**Enable:**
- ✅ Issues
- ✅ Discussions
- ❌ Wiki (use docs/ instead)
- ✅ Auto-delete head branches

---

## Files Checklist

All these files are now in the repository:

- ✅ `.github/ISSUE_TEMPLATE/bug_report.yml`
- ✅ `.github/ISSUE_TEMPLATE/feature_request.yml`
- ✅ `.github/ISSUE_TEMPLATE/config.yml`
- ✅ `.github/pull_request_template.md`
- ✅ `.github/workflows/` (build, test, lint, security, release)
- ✅ `CONTRIBUTING.md`
- ✅ `CODE_OF_CONDUCT.md`
- ✅ `SECURITY.md`
- ✅ `CITATION.cff`
- ✅ `LICENSE`
- ✅ `README.md` (with badges)
- ✅ `.gitignore` (including `releases/`)

---

## Optional Enhancements

### CODEOWNERS

Create `.github/CODEOWNERS`:
```
* @maintainer-username
/docs/ @docs-team
```

### Dependabot

Create `.github/dependabot.yml`:
```yaml
version: 2
updates:
  - package-ecosystem: "gomod"
    directory: "/"
    schedule:
      interval: "weekly"
```

### Release Notes

Create `.github/release.yml`:
```yaml
changelog:
  categories:
    - title: Breaking Changes 🚨
      labels: [breaking-change]
    - title: New Features 🎉
      labels: [enhancement, feature]
    - title: Bug Fixes 🐛
      labels: [bug]
```

---

## Community Standards Check

**Location**: Insights → Community Standards

Verify all items are ✅:
- Description
- README
- Code of conduct
- Contributing
- License
- Security policy
- Issue templates
- Pull request template

---

## Quick Commands for Repository Owner

### Enable Discussions
```bash
# Navigate to repository settings
# Settings → General → Features → Discussions → Enable
```

### Set Topics via CLI (requires gh CLI)
```bash
gh repo edit Dynatrace/dtctl \
  --add-topic dynatrace \
  --add-topic cli \
  --add-topic kubectl \
  --add-topic kubernetes \
  --add-topic devops \
  --add-topic observability \
  --add-topic golang \
  --add-topic command-line \
  --add-topic workflow-automation \
  --add-topic dql \
  --add-topic dashboards \
  --add-topic monitoring \
  --add-topic platform-engineering \
  --add-topic developer-tools
```

### Update Description via CLI
```bash
gh repo edit Dynatrace/dtctl \
  --description "kubectl-inspired CLI for managing Dynatrace platform resources from your terminal"
```

---

**Next Steps After Setup:**

1. ✅ Configure About section (description, website, topics)
2. ✅ Enable Discussions
3. ✅ Set up branch protection rules
4. ✅ Enable all security features
5. ✅ Verify Community Standards (should all be green)
6. ✅ Create first Discussion post in "Announcements"
7. ✅ Pin important issues or discussions
8. ✅ Add repository to GitHub's "topic" pages for visibility

---

**Last Updated**: 2026-01-12
