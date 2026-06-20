---
name: repo-onboarding
description: Use when quickly understanding an unfamiliar repository and producing a concise, fact-based orientation for future Agent work.
---

# Repo Onboarding

## When to Use

Use this skill when entering a repository for the first time, preparing a handoff, or updating project context after significant structure changes.

## Workflow

1. Read `AGENTS.md` and `.claude/README.md` if present.
2. Inspect top-level files, package manifests, scripts, and tests with `rg --files`.
3. Identify the main languages, runtimes, entry points, and validation commands from real files.
4. Map major directories to responsibilities.
5. Separate facts from assumptions. Mark unknowns explicitly.
6. Recommend only the smallest documentation updates needed to make future work easier.

## Output

Provide:

- Repository purpose.
- Important directories and files.
- Likely build/test commands.
- Known risks or missing context.
- Suggested docs to update.

## Validation

Prefer commands that only inspect state, such as `git status --short`, `rg --files`, and package-manager script listings.

## Safety

Do not run destructive commands or network installs during onboarding unless the user explicitly asks.
