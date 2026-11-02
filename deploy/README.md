# Docker Compose example: Quizzotic with Pocket ID

`docker-compose.yaml` runs the published image (`ghcr.io/chan-labs/quizzotic`), [Pocket ID](https://pocket-id.org) as the identity provider, and Caddy to terminate TLS for both. It is an example to adapt, not a hardened production manifest. All hosts here are placeholders (`example.com`).

## Prerequisites

- Docker with Compose v2.
- Two DNS names pointing at this machine, with ports 80 and 443 reachable so Caddy can get certificates: one for Quizzotic (`QUIZZOTIC_HOST`) and one for Pocket ID (`POCKET_ID_HOST`). Web sign-in sets `Secure` cookies and passkeys need a secure origin, so use HTTPS.

## Setup

Quizzotic needs a Pocket ID client secret that only exists after Pocket ID is running, so bring the stack up in two passes.

1. Configure:

   ```sh
   cd deploy
   cp .env.example .env
   openssl rand -base64 32   # put the result in POCKET_ID_ENCRYPTION_KEY
   ```

   Edit `.env`: set the two hosts and the encryption key. `.env` is gitignored; never commit it.
2. Start the stack: `docker compose up -d`. Quizzotic starts but logs `web login disabled` because no client secret is set yet.
3. Open `https://<POCKET_ID_HOST>/setup` to create the first admin and register a passkey.
4. Register the OAuth clients by following [docs/pocket-id.md](../docs/pocket-id.md). For this example the web callback is `https://<QUIZZOTIC_HOST>/auth/callback`. Copy the `quizzotic-web` secret into `QUIZZOTIC_WEB_CLIENT_SECRET` in `.env`.
5. Apply it: `docker compose up -d`. Compose recreates Quizzotic with the secret.
6. Browse to `https://<QUIZZOTIC_HOST>/auth/login`; you are sent to Pocket ID and back.

### Audience requirement

Quizzotic accepts only access tokens whose `aud` matches one of `auth.issuers[0].audience`. Pocket ID sets a token's `aud` to the OAuth client ID, so list the client ID of every client you use there (placeholders in `quizzotic.yaml`), then verify as described in [docs/pocket-id.md](../docs/pocket-id.md) sections 2 and 4. Tokens with any other audience are rejected.

### Using other clients

`quizzotic.yaml` declares `quizzotic-web`, `quizzotic-cli` and `quizzotic-agent`. Add `quizzotic-headless` (and its `acts_for` subject) as described in [quizzotic.example.yaml](../quizzotic.example.yaml) if you need it.

## How configuration flows

`quizzotic.yaml` is mounted read-only at `/etc/quizzotic/quizzotic.yaml`. The compose file overrides three values from `.env` with `QUIZZOTIC_*` environment variables so the YAML and the proxy hosts cannot disagree: `server.public_url`, `auth.issuers[0].issuer` and the `quizzotic-web` secret. Edit `quizzotic.yaml` for anything else (scheduling, logging, more clients).

The issuer URL must be reachable from the Quizzotic container at the same URL browsers use. Caddy gets network aliases for both hostnames, so inside the compose network they resolve to Caddy.

## Volumes

| Volume | Holds | Back it up |
| --- | --- | --- |
| `quizzotic-data` | SQLite database at `/data/quizzotic.db` | Yes. Or use `quizzotic admin export`. |
| `pocket-id-data` | Pocket ID database, keys, uploads | Yes, together with `POCKET_ID_ENCRYPTION_KEY`. |
| `caddy-data` | Issued certificates | Optional. |

A one-shot `quizzotic-data-init` service makes `quizzotic-data` writable for the image's non-root user (numeric uid/gid 65532, so Kubernetes `runAsNonRoot` works without a `runAsUser` override). To use PostgreSQL instead, set `storage.url` to a `postgres://` or `postgresql://` URL and drop the volume.

## Health checks

The Quizzotic image is distroless: it has no shell or `curl`, so Compose cannot run a healthcheck inside it. The `quizzotic-health` service shares Quizzotic's network namespace and polls `http://localhost:8080/healthz` every 30 seconds, so `docker compose ps` shows it as `healthy`. `/readyz` additionally reports store and JWKS state. If you recreate only `quizzotic`, run `docker compose up -d` again so the probe re-attaches.

## Image tags

`QUIZZOTIC_TAG` defaults to `latest`; pin a release version in production. The image must be public on GHCR (see [docs/releasing.md](../docs/releasing.md)). To test a locally built image set `QUIZZOTIC_IMAGE` and `QUIZZOTIC_TAG` in `.env` (after `mise run snapshot`, tag one of the built images).

## Local trial without public DNS

Set `URL_SCHEME=http` and use hostnames that resolve to this machine (for example entries in `/etc/hosts`). Note that browsers only accept `Secure` cookies and passkeys on plain HTTP for `localhost`, so a full browser sign-in may not work this way; use real HTTPS hostnames for that.
