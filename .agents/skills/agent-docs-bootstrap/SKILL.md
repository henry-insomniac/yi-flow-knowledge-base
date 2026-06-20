---
name: agent-docs-bootstrap
description: Use when initializing or repairing project-level Agent collaboration docs such as AGENTS.md and .claude/*. It should create a minimal truthful documentation baseline without inventing nonexistent architecture.
---

# Agent Docs Bootstrap

## When to Use

Use this skill when a user asks to initialize, refresh, repair, or review project-level Agent documentation.

Do not use it to rewrite mature project docs without a clear request.

## Workflow

1. Read `AGENTS.md` if present, then inspect `.claude/README.md` and related `.claude/*` files.
2. Inspect the real repository structure with `rg --files` or a shallow directory listing.
3. Identify missing baseline docs:
   - `AGENTS.md`
   - `.claude/README.md`
   - `.claude/project-architecture.md`
   - `.claude/bug-fix-log.md`
   - `.claude/git-collaboration.md`
   - `.claude/tech-stack.md`
4. Add only facts that are visible in the repository or explicitly provided by the user.
5. Leave placeholders for unknown architecture, commands, or deployment details instead of guessing.
6. Preserve existing project-specific guidance unless it is clearly obsolete and the user asked to update it.

## Output

Report:

- Files created or updated.
- Facts inferred from the repository.
- Unknowns left for maintainers.
- Verification commands run.

## Validation

Run `git status --short` and any repository-specific documentation check if one exists.

## Safety

Do not write personal paths, credentials, account names, private URLs, or unverified deployment details into templates or project docs.
