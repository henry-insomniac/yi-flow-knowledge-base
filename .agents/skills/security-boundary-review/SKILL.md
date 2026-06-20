---
name: security-boundary-review
description: Use when reviewing templates, scripts, installers, skills, or automation that may touch credentials, user data, network calls, file writes, deletion, publishing, or external services.
---

# Security Boundary Review

## When to Use

Use this skill before adding or changing workflows that involve:

- Credentials, tokens, cookies, or account state.
- External API calls or downloads.
- File deletion, overwrite, publish, push, or remote writes.
- Third-party scripts or skills.
- Analytics, IP addresses, or user telemetry.

## Workflow

1. Identify data read, data written, and network destinations.
2. Identify required permissions and whether they are explicit or implicit.
3. Check for hardcoded secrets, personal paths, private URLs, or account-specific details.
4. Confirm destructive actions require explicit user flags or confirmation.
5. For third-party skills, review `SKILL.md`, scripts, assets, license, and update mechanism.
6. Document safe defaults, failure behavior, and residual risk.

## Output

Report findings by severity, then list required changes or accepted risks.

## Validation

Use static inspection first. Run commands only when they are local and non-destructive.

## Safety

Do not expose secrets in summaries. Redact sensitive values and avoid copying private logs into documentation.
