---
name: skill-author
description: Use when creating, updating, or reviewing Agent Skills with SKILL.md, scripts, references, assets, or agents/openai.yaml. Focuses on trigger clarity, progressive disclosure, and safe boundaries.
---

# Skill Author

## When to Use

Use this skill when the user asks to add, edit, package, review, or install a skill.

## Workflow

1. Define the repeatable workflow the skill should make reliable.
2. Write `SKILL.md` with YAML frontmatter containing `name` and a precise `description`.
3. Keep `SKILL.md` concise. Move long examples, schemas, and variant-specific details into `references/`.
4. Use `scripts/` only when deterministic execution is more reliable than instructions.
5. Use `assets/` for templates, static resources, or boilerplate copied into outputs.
6. State trigger conditions, non-goals, inputs, workflow, output, validation, and safety boundaries.
7. Avoid broad descriptions that make the skill trigger for unrelated work.

## Output

Provide the skill path, trigger summary, safety boundaries, and how it was validated.

## Validation

Check that:

- `SKILL.md` has valid frontmatter.
- The description clearly says when to use the skill.
- Scripts are local, reviewable, and do not require hidden credentials.
- Large reference content is not duplicated in the main instruction file.

## Safety

Treat third-party skills like software dependencies. Review scripts, network calls, file access, and license terms before installing or recommending them.
