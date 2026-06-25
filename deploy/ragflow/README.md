# RAGFlow Auth-Gated Deployment Templates

These files are deployment templates for `https://rag.yi-flow.com`.

They are intentionally examples:

- copy `oauth2-proxy.env.example` to `oauth2-proxy.env` on the server
- copy `Caddyfile.example` to `Caddyfile` on the server
- keep secrets in the server secret store or local deployment directory, not git
- keep RAGFlow internal ports unpublished

## Auth Boundary

The intended edge path is:

```text
Internet -> Caddy :443 -> oauth2-proxy :4180 -> RAGFlow :80
```

`oauth2-proxy` uses auth-service OIDC:

- issuer: `https://auth.yi-flow.com`
- client_id: `ragflow-admin`
- redirect_uri: `https://rag.yi-flow.com/oauth2/callback`

## Files

| File | Purpose |
| --- | --- |
| `oauth2-proxy.env.example` | OIDC/auth gate environment variables with secret placeholders |
| `Caddyfile.example` | TLS termination, security headers, body limits, reverse proxy |
| `docker-compose.auth-gate.example.yml` | Minimal edge stack showing Caddy + oauth2-proxy + private RAGFlow upstream |

## Verification

Before exposing production traffic:

```bash
scripts/verify-ragflow-host-readiness.sh
```

After DNS and auth are configured:

```bash
RAGFLOW_CHECK_PUBLIC_INGRESS=1 \
scripts/verify-ragflow-replacement-smoke.sh
```

The unauthenticated public ingress check should return redirect/deny status, not `200`.
