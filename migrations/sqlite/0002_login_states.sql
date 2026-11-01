-- Pre-login state for the web OIDC flow, kept server-side. Rows are single use
-- and short lived; only a hash of the state value is stored. There is no owner
-- yet: the user is unknown until the callback completes.
CREATE TABLE login_states (
    state_hash    TEXT PRIMARY KEY,
    nonce         TEXT NOT NULL,
    code_verifier TEXT NOT NULL,
    return_to     TEXT NOT NULL,
    expires_at    INTEGER NOT NULL
);

CREATE INDEX login_states_expires_at ON login_states (expires_at);
