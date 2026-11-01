# Setting up Pocket ID as the identity provider

Quizzotic trusts one OIDC issuer and mints no tokens ([ADR 0004](adr/0004-idp-issued-tokens-only.md)). [Pocket ID](https://pocket-id.org) is the documented example IdP; nothing in Quizzotic is specific to it. This guide registers the four OAuth clients Quizzotic expects and makes each one receive an access token whose `aud` is `quizzotic`.

Hosts below are placeholders: `https://auth.example.com` is your Pocket ID instance and `https://quizzotic.example.com` is your Quizzotic `server.public_url`. Pocket ID's admin UI changes between releases, so treat the field names as a guide and check the [Pocket ID documentation](https://pocket-id.org/docs) for your version.

## The clients

| Client ID | Kind in config | Type | Grant | Used by |
| --- | --- | --- | --- | --- |
| `quizzotic-web` | `human` | Confidential (has a secret) | Authorization Code + PKCE | Web UI sign-in |
| `quizzotic-cli` | `human` | Public (no secret) | Authorization Code + PKCE, loopback redirect | `quizzotic login` |
| `quizzotic-agent` | `agent` | Public (no secret) | Authorization Code + PKCE (loopback) or device flow | `quizzotic login --agent`, local MCP |
| `quizzotic-headless` | `agent` | Confidential | Client credentials | Unattended agents and scripts |

Use exactly these IDs, or change both the IdP and `auth.issuers[0].clients` to match. Quizzotic rejects tokens from any client not declared in its config.

## 1. Create the clients in Pocket ID

In the Pocket ID admin area, open OIDC Clients and add each client.

1. **`quizzotic-web`**
   - Client ID: `quizzotic-web` (set it explicitly if your version lets you; otherwise copy the generated ID into the config).
   - Callback URL: `https://quizzotic.example.com/auth/callback`
   - Logout callback URL (if asked): `https://quizzotic.example.com/`
   - Public client: off. Generate a client secret and keep it out of version control; you will pass it as `QUIZZOTIC_AUTH_ISSUERS_0_CLIENTS_0_SECRET`.
   - PKCE: on.
2. **`quizzotic-cli`**
   - Public client: on (no secret). PKCE: on.
   - Callback URLs: loopback redirects per RFC 8252, `http://127.0.0.1/callback` and `http://[::1]/callback`. If your Pocket ID version requires an exact port, register the fixed port the CLI is configured to use.
   - If you want device flow, enable it for this client when your version offers it.
3. **`quizzotic-agent`**
   - Same settings as `quizzotic-cli`; device flow enabled if available. It is a separate client so Quizzotic can tell agent traffic from human traffic and apply `allowed_scopes`.
4. **`quizzotic-headless`**
   - Public client: off. Enable the client-credentials grant if your version exposes it, and generate a secret (never commit it).
   - Note which Pocket ID user or subject this client's tokens should act for; that value goes in `acts_for` (step 3).

Restrict each client to the users or groups allowed to use Quizzotic, using Pocket ID's allowed-groups setting.

## 2. Make the access-token audience `quizzotic`

Quizzotic verifies `iss`, signature, `exp`, `nbf` and `aud` locally against the issuer's JWKS, and `aud` must equal `auth.issuers[0].audience` (`quizzotic`). Pocket ID issues JWT access tokens; configure each of the four clients so that the access-token `aud` is `quizzotic` rather than the default (typically the client's own ID). Use the audience or resource setting that your Pocket ID version provides for OIDC clients. If your version has no such setting, do not work around it in Quizzotic: open an issue so the supported audience mapping can be documented.

Quizzotic does not read audience from ID tokens: the web client's ID token (whose `aud` is the client ID, as OIDC requires) is used only for sign-in and `nonce` checks.

## 3. Configure Quizzotic

[`quizzotic.example.yaml`](../quizzotic.example.yaml) contains the matching block; the relevant part is:

```yaml
auth:
  issuers:
    - name: "pocketid"
      issuer: "https://auth.example.com"
      audience: "quizzotic"
      scopes: ["openid", "profile", "email"]
      clients:
        - id: "quizzotic-web"
          kind: "human"
          secret: ""             # QUIZZOTIC_AUTH_ISSUERS_0_CLIENTS_0_SECRET
        - id: "quizzotic-cli"
          kind: "human"
        - id: "quizzotic-agent"
          kind: "agent"
          allowed_scopes: ["problems:read", "problems:write", "tags:read", "tags:write", "quizzes:read", "quizzes:write", "metrics:read"]
        - id: "quizzotic-headless"
          kind: "agent"
          acts_for: "<issuer sub of the human>"
          allowed_scopes: ["problems:read", "problems:write", "tags:read", "tags:write", "metrics:read"]
```

Rules enforced at start-up: `kind` is `human` or `agent`; `acts_for` is valid only on `agent` clients; agent clients may never hold `attempts:write`, `sessions:write` or `admin`. Secrets belong in environment variables or your secret store, never in a committed file.

## 4. Verify each client gets `aud=quizzotic`

Decode the access token (the payload is the second dot-separated segment, base64url) and check its claims, for example with `cut -d. -f2 <<<"$TOKEN" | base64 -d | jq '{iss, aud, sub, client_id, exp}'`. Discover endpoints from `https://auth.example.com/.well-known/openid-configuration`.

- **`quizzotic-headless`:** request a token directly.

  ```sh
  curl -s https://auth.example.com/api/oidc/token \
    -d grant_type=client_credentials \
    -d client_id=quizzotic-headless \
    -d client_secret="$HEADLESS_SECRET"
  ```

  Use the `token_endpoint` from discovery if it differs. Expect `aud` to be `quizzotic`.
- **`quizzotic-cli` and `quizzotic-agent`:** run `quizzotic login` and `quizzotic login --agent`, or perform an Authorization Code + PKCE (or device-flow) exchange by hand, and decode the returned `access_token`.
- **`quizzotic-web`:** complete a browser sign-in with a test client or OIDC debugger using the same client and secret, and decode the `access_token` from the token response.

Success for every client: `aud` is `quizzotic` (as a string or a one-element array), and `iss` equals your configured issuer.

## Using the CLI logins

`quizzotic login` finds the issuer from `$QUIZZOTIC_SERVER`'s `/.well-known/oauth-protected-resource` (override with `--issuer` or `QUIZZOTIC_ISSUER`) and requests the scopes `openid profile email offline_access`, so the clients must be allowed to receive refresh tokens. Without one the CLI still works but you log in again when the access token expires. `--device` (or an automatic fallback when no browser opens) uses device flow when the issuer advertises it. Unattended scripts set `QUIZZOTIC_CLIENT_ID=quizzotic-headless` and `QUIZZOTIC_CLIENT_SECRET`; the CLI exchanges them by client credentials on each run and stores nothing. Logins live in `~/.config/quizzotic/credentials.json` (mode 0600); `quizzotic me` (shorthand for `quizzotic meta me`) shows who the server thinks you are, and `--agent` or `QUIZZOTIC_AGENT=1` selects the agent login.

## Remote MCP clients

Pocket ID supports [Client ID Metadata Documents](https://pocket-id.org/docs/guides/client-id-metadata-documents) for public clients, which lets remote MCP clients register without manual setup. Such clients must be declared `agent` in `auth.issuers[0].clients` before Quizzotic accepts their tokens.
