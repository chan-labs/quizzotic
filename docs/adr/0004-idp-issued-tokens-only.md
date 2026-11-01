# 0004. IdP-issued tokens only

Status: Accepted

## Context

Humans and agents both need to authenticate. Running a token issuer inside Quizzotic would add credential storage, key management and an attack surface that an existing OIDC provider already handles.

## Decision

Quizzotic trusts one configured OIDC issuer and mints no tokens. Access tokens are verified locally as JWTs against the issuer's cached JWKS (signature, `iss`, `aud`, `exp`, `nbf`; `nonce` for ID tokens). The only credential Quizzotic issues itself is the server-side web session cookie, which is not a bearer token for the API. Users are stored as (issuer, subject) references and provisioned on first login. Principal kind (human or agent) comes from the token's client ID as declared in `auth.clients`; unknown clients are rejected. Agent clients are refused `attempts:write`, `sessions:write` and `admin` at config validation, so "humans answer, agents curate" is enforced by scopes.

## Consequences

- No credential issuance, password storage or signup flow in Quizzotic; any compliant OIDC issuer works.
- Revoking or rotating credentials happens at the IdP.
- Quizzotic depends on the IdP's availability and on correct client declarations in config.
- JWT verification, PKCE and sessions are written on the standard library, so they need careful tests.
