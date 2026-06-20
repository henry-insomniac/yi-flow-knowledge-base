---
name: template-maintainer
description: Use when changing scaffold templates under templates/agent-docs or the generated documentation contract. Ensures template, README, architecture, and CLI behavior stay aligned.
---

# Template Maintainer

## When to Use

Use this skill when modifying `templates/agent-docs/`, adding template placeholders, changing generated file names, or changing what the initializer writes.

## Workflow

1. Read `AGENTS.md`, `.claude/README.md`, and `.claude/project-architecture.md`.
2. Inspect `templates/agent-docs/` and `scripts/init_agent_docs.py`.
3. If adding or removing generated files, update:
   - `README.md`
   - `.claude/project-architecture.md`
   - `.claude/tech-stack.md` if scripts or validation commands change.
4. If adding placeholders, update both the renderer values in `scripts/init_agent_docs.py` and the placeholder list in `README.md`.
5. Keep templates generic. Do not embed this repository's local paths, accounts, or deployment-only details unless the template is explicitly for this repository.
6. Verify dry-run and write behavior.

## Output

Summarize template changes, script changes, documentation updates, and validation results.

## Validation

At minimum run:

```bash
python3 scripts/init_agent_docs.py --target /tmp/agent-docs-test --project-name demo --description "一个测试项目" --force
python3 scripts/init_agent_docs.py --target /tmp/agent-docs-test --project-name demo --dry-run
git status --short
```

## Safety

Default behavior must continue to protect existing target files. Any overwrite behavior must require an explicit flag.
