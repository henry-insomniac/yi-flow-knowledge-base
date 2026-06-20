---
name: agent-handoff
description: Use when summarizing completed or paused Agent work into a concise handoff that another Agent or maintainer can continue from.
---

# Agent Handoff

## When to Use

Use this skill when work is paused, transferred, or complex enough that future maintainers need a durable summary.

## Workflow

1. Summarize the original goal and current status.
2. List files changed or intentionally left untouched.
3. Record commands run and their outcomes.
4. Capture decisions, assumptions, and unresolved questions.
5. Identify the next concrete steps.
6. Keep the handoff factual and avoid hidden reasoning.

## Output

Provide a compact handoff with:

- Goal.
- Completed work.
- Important files.
- Validation.
- Risks.
- Next steps.

## Validation

Run `git status --short` before finalizing if repository state matters.

## Safety

Do not include secrets, private credentials, or unnecessary logs.
