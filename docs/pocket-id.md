# Setting up Pocket ID as the identity provider

Quizzotic trusts one OIDC issuer and mints no tokens ([ADR 0004](adr/0004-idp-issued-tokens-only.md)). [Pocket ID](https://pocket-id.org) is the documented example IdP; nothing in Quizzotic is specific to it. This guide registers the four OAuth clients Quizzotic expects and lists each client's ID as an accepted audience, because Pocket ID sets the access-token `aud` to the client ID.

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
   - Client ID: `quizzotic-web` (Pocket ID generates client IDs; copy each generated ID into `auth.issuers[0].clients` and `audience`, the examples below use `quizzotic-web` and so on as placeholders).
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

## 2. Accept each client's ID as an audience

Quizzotic verifies `iss`, signature, `exp`, `nbf` and `aud` locally against the issuer's JWKS. Pocket ID issues JWT access tokens whose `aud` is the ID of the client that requested them, and client IDs cannot be chosen freely, so there is no single audience to configure. Instead, `auth.issuers[0].audience` takes a list: a token is accepted when its `aud` (a string or an array) contains any listed value. List the IDs of all four clients (a single string still works for an IdP that issues one shared audience). Tokens with any other `aud` are rejected. Quizzotic still classifies a token by its `client_id` or `azp` claim, falling back to the one declared client named in `aud`.

Quizzotic does not read audience from ID tokens: the web client's ID token (whose `aud` is the client ID, as OIDC requires) is used only for sign-in and `nonce` checks.

## 3. Configure Quizzotic

[`quizzotic.example.yaml`](../quizzotic.example.yaml) contains the matching block; the relevant part is:

```yaml
auth:
  issuers:
    - name: "pocketid"
      issuer: "https://auth.example.com"
      # Pocket ID sets aud to the client ID; list the four IDs from step 1.
      audience: ["quizzotic-web", "quizzotic-cli", "quizzotic-agent", "quizzotic-headless"]
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

## 4. Verify each client's `aud`

Decode the access token (the payload is the second dot-separated segment, base64url) and check its claims, for example with `cut -d. -f2 <<<"$TOKEN" | base64 -d | jq '{iss, aud, sub, client_id, exp}'`. Discover endpoints from `https://auth.example.com/.well-known/openid-configuration`.

- **`quizzotic-headless`:** request a token directly.

  ```sh
  curl -s https://auth.example.com/api/oidc/token \
    -d grant_type=client_credentials \
    -d client_id=quizzotic-headless \
    -d client_secret="$HEADLESS_SECRET"
  ```

  Use the `token_endpoint` from discovery if it differs. Expect `aud` to be this client's ID.
- **`quizzotic-cli` and `quizzotic-agent`:** run `quizzotic login` and `quizzotic login --agent`, or perform an Authorization Code + PKCE (or device-flow) exchange by hand, and decode the returned `access_token`.
- **`quizzotic-web`:** complete a browser sign-in with a test client or OIDC debugger using the same client and secret, and decode the `access_token` from the token response.

Success for every client: `aud` is that client's own ID (as a string or an array), it appears in `auth.issuers[0].audience`, and `iss` equals your configured issuer.

## Using the CLI logins

`quizzotic login` finds the issuer from `$QUIZZOTIC_SERVER`'s `/.well-known/oauth-protected-resource` (override with `--issuer` or `QUIZZOTIC_ISSUER`) and requests the scopes `openid profile email offline_access`, so the clients must be allowed to receive refresh tokens. Without one the CLI still works but you log in again when the access token expires. `--device` (or an automatic fallback when no browser opens) uses device flow when the issuer advertises it. Unattended scripts set `QUIZZOTIC_CLIENT_ID=quizzotic-headless` and `QUIZZOTIC_CLIENT_SECRET`; the CLI exchanges them by client credentials on each run and stores nothing. Logins live in `~/.config/quizzotic/credentials.json` (mode 0600); `quizzotic me` (shorthand for `quizzotic meta me`) shows who the server thinks you are, and `--agent` or `QUIZZOTIC_AGENT=1` selects the agent login.

### CLI config file

Instead of exporting `QUIZZOTIC_SERVER` in every shell, put the defaults in `config.yaml` in the same directory as `credentials.json` (`$XDG_CONFIG_HOME/quizzotic/` or `~/.config/quizzotic/`; the directory of `$QUIZZOTIC_CREDENTIALS` when that is set). Set `QUIZZOTIC_CLI_CONFIG` to read a different file. The file is optional, plain YAML, and holds no secrets, so it needs no special permissions. Never put tokens or client secrets in it.

```yaml
# ~/.config/quizzotic/config.yaml
server: https://quiz.example.com        # base URL of the Quizzotic server
issuer: https://id.example.com          # OIDC issuer; skips the server lookup at login
login_client_id: quizzotic-cli          # client ID for `quizzotic login`
```

All keys are optional. Precedence, highest first: command-line flag (`--server`, `--issuer`, `--client-id`), environment variable (`QUIZZOTIC_SERVER`, `QUIZZOTIC_ISSUER`, `QUIZZOTIC_LOGIN_CLIENT_ID`), config file, built-in default (`http://localhost:8080`, the issuer the server advertises, `quizzotic-cli`/`quizzotic-agent`). `quizzotic mcp` uses the same base URL resolution. A missing file is ignored; an unreadable, malformed or unknown-key file is an error naming the file.

## Remote MCP clients

Pocket ID supports [Client ID Metadata Documents](https://pocket-id.org/docs/guides/client-id-metadata-documents) for public clients, which lets remote MCP clients register without manual setup. Such clients must be declared `agent` in `auth.issuers[0].clients` before Quizzotic accepts their tokens.
