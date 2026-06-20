---
name: bug-fix-recorder
description: Use after fixing a bug or regression to update .claude/bug-fix-log.md with symptom, impact, root cause, fix, validation, and follow-up.
---

# Bug Fix Recorder

## When to Use

Use this skill after a real bug, regression, failed install, broken script, or incorrect generated output has been diagnosed and fixed.

Do not use it for typo-only edits or pure formatting changes.

## Workflow

1. Identify the user-visible symptom and affected workflow.
2. Record the impact and severity in practical terms.
3. State the root cause concretely. Avoid vague entries such as "logic error".
4. Describe the fix and why it resolves the issue.
5. Record validation commands and relevant manual checks.
6. Add follow-up tasks only when they are actionable.
7. Keep the newest entry near the top if the project already uses reverse chronological order.

## Output

Update `.claude/bug-fix-log.md` and summarize the entry title plus validation performed.

## Validation

Run the bug-specific reproduction or regression command when possible. If no automated check exists, state the manual verification.

## Safety

Do not include secrets, private user data, full tokens, or sensitive logs in the bug record.
