---
name: release-packaging
description: Use when preparing install.sh, release tarballs, versioned packaging, or post-install validation for this scaffold or similar CLI installers.
---

# Release Packaging

## When to Use

Use this skill when changing install scripts, packaging layout, release artifacts, or installation verification.

## Workflow

1. Read `README.md`, `.claude/project-architecture.md`, and `.claude/tech-stack.md`.
2. Inspect install entry points such as `install.sh` and CLI scripts.
3. Confirm the package includes required templates, skills, registry files, and scripts.
4. Keep install behavior idempotent and explicit about overwrite or destructive steps.
5. Verify the installed command can run `--help`.
6. Document release risks, required system tools, and rollback approach.

## Output

Summarize package contents, install behavior, verification commands, and any compatibility risk.

## Validation

Prefer:

```bash
sh install.sh
init-agent-docs --help
git status --short
```

Use local override URLs or directories when testing unpublished artifacts.

## Safety

Refuse unsafe install destinations and avoid global writes unless the user runs with the required permissions intentionally.
