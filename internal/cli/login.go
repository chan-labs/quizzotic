// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/chan-labs/quizzotic/internal/platform"
)

// Default OAuth client IDs, matching docs/pocket-id.md.
const (
	DefaultHumanClientID = "quizzotic-cli"
	DefaultAgentClientID = "quizzotic-agent"
)

// errNoBrowser means no browser could be started and device flow can take over.
var errNoBrowser = errors.New("no browser available")

// ErrLoginRequired is wrapped by errors telling the user to run "quizzotic login".
var ErrLoginRequired = errors.New("login required")

func (e *Env) applyDefaults() {
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	if e.HTTP == nil {
		e.HTTP = &http.Client{Transport: platform.TraceTransport(nil)}
	}
	if e.Stdin == nil {
		e.Stdin = os.Stdin
	}
	if e.Stdout == nil {
		e.Stdout = io.Discard
	}
	if e.Stderr == nil {
		e.Stderr = io.Discard
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.OpenBrowser == nil {
		e.OpenBrowser = openBrowser
	}
	if e.Sleep == nil {
		e.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// ServerBase resolves the Quizzotic base URL for other entry points (the MCP
// server) so they share the CLI's precedence rules.
func ServerBase(flagVal string, env Env) (string, error) {
	env.applyDefaults()
	return serverBase(flagVal, env)
}

// serverBase resolves the Quizzotic base URL.
//
// Precedence: --server, $QUIZZOTIC_SERVER, the config file's server key,
// then DefaultServer. A malformed config file is an error.
func serverBase(flagVal string, env Env) (string, error) {
	base := flagVal
	if base == "" {
		base = env.Getenv("QUIZZOTIC_SERVER")
	}
	if base == "" {
		cfg, err := loadFileConfig(env.Getenv)
		if err != nil {
			return "", err
		}
		base = cfg.Server
	}
	if base == "" {
		base = DefaultServer
	}
	return strings.TrimRight(base, "/"), nil
}

func wantAgent(flagVal bool, env Env) bool {
	if flagVal {
		return true
	}
	v := strings.ToLower(env.Getenv("QUIZZOTIC_AGENT"))
	return v == "1" || v == "true" || v == "yes"
}

// resolveIssuer finds the OIDC issuer: an explicit value (--issuer or
// $QUIZZOTIC_ISSUER), else the first authorization server the Quizzotic
// server names in its protected-resource metadata (RFC 9728).
func resolveIssuer(ctx context.Context, env Env, explicit, server string) (string, error) {
	if explicit == "" {
		explicit = env.Getenv("QUIZZOTIC_ISSUER")
	}
	if explicit == "" {
		cfg, err := loadFileConfig(env.Getenv)
		if err != nil {
			return "", err
		}
		explicit = cfg.Issuer
	}
	if explicit != "" {
		return strings.TrimRight(explicit, "/"), nil
	}
	if err := checkIssuerURL(server); err != nil {
		return "", fmt.Errorf("server URL: %w", err)
	}
	var doc struct {
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := getJSON(ctx, env.HTTP, server+"/.well-known/oauth-protected-resource", &doc); err != nil || len(doc.AuthorizationServers) == 0 {
		return "", fmt.Errorf("cannot find the identity provider: pass --issuer or set QUIZZOTIC_ISSUER (server lookup failed: %v)", err)
	}
	iss := strings.TrimRight(doc.AuthorizationServers[0], "/")
	if err := checkIssuerURL(iss); err != nil {
		return "", fmt.Errorf("authorization server advertised by %s: %w", server, err)
	}
	return iss, nil
}

// AccessToken returns the bearer token for API calls, in this order:
// $QUIZZOTIC_TOKEN; a headless client ($QUIZZOTIC_CLIENT_ID and
// $QUIZZOTIC_CLIENT_SECRET, exchanged by client credentials); the stored
// login for the human or, with agent, the agent profile, silently refreshed
// when expired. It returns "" with no error when nothing is configured and
// agent is false, so the API answers 401 itself.
func AccessToken(ctx context.Context, env Env, server string, agent bool) (string, error) {
	env.applyDefaults()
	if tok := env.Getenv("QUIZZOTIC_TOKEN"); tok != "" {
		return tok, nil
	}
	id, secret := env.Getenv("QUIZZOTIC_CLIENT_ID"), env.Getenv("QUIZZOTIC_CLIENT_SECRET")
	if id != "" || secret != "" {
		if id == "" || secret == "" {
			return "", errors.New("headless login needs both QUIZZOTIC_CLIENT_ID and QUIZZOTIC_CLIENT_SECRET")
		}
		issuer, err := resolveIssuer(ctx, env, "", server)
		if err != nil {
			return "", err
		}
		return clientCredentials(ctx, env.HTTP, issuer, id, secret)
	}

	path := credentialsPath(env.Getenv)
	cf, err := loadCredentials(path)
	if err != nil {
		return "", err
	}
	name := profileName(agent)
	cred, ok := cf[name]
	if !ok {
		if agent {
			return "", fmt.Errorf("%w: no agent login stored; run \"quizzotic login --agent\"", ErrLoginRequired)
		}
		return "", nil
	}
	if env.Now().Add(refreshSkew).Before(cred.ExpiresAt) {
		return cred.AccessToken, nil
	}
	relogin := "quizzotic login"
	if agent {
		relogin += " --agent"
	}
	if cred.RefreshToken == "" {
		return "", fmt.Errorf("%w: the stored access token expired and has no refresh token; run %q", ErrLoginRequired, relogin)
	}
	fresh, err := refresh(ctx, env.HTTP, env.Now(), cred)
	if err != nil {
		var oe *oauthError
		if errors.As(err, &oe) && (oe.Code == "invalid_grant" || oe.Status == http.StatusUnauthorized || oe.Status == http.StatusBadRequest) {
			return "", fmt.Errorf("%w: the stored login can no longer be refreshed (%v); run %q", ErrLoginRequired, err, relogin)
		}
		return "", fmt.Errorf("refreshing the stored login: %w", err)
	}
	cf[name] = fresh
	if err := saveCredentials(path, cf); err != nil {
		return "", fmt.Errorf("saving refreshed credentials: %w", err)
	}
	return fresh.AccessToken, nil
}

// Login implements "quizzotic login [--agent] [--device]". It returns the
// process exit code.
func Login(ctx context.Context, args []string, env Env) int {
	env.applyDefaults()
	fs := flag.NewFlagSet("quizzotic login", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	server := fs.String("server", "", "Quizzotic server base URL (default $QUIZZOTIC_SERVER, then config.yaml, then "+DefaultServer+")")
	issuer := fs.String("issuer", "", "OIDC issuer URL (default $QUIZZOTIC_ISSUER, then issuer: in config.yaml, else asked of the server)")
	clientID := fs.String("client-id", "", "OAuth client ID (default $QUIZZOTIC_LOGIN_CLIENT_ID, then login_client_id: in config.yaml, else "+DefaultHumanClientID+", or "+DefaultAgentClientID+" with --agent)")
	agentFlag := fs.Bool("agent", false, "log in with the agent client; stored separately from the human login")
	device := fs.Bool("device", false, "use the device flow instead of a browser (for machines without one)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(env.Stderr, "quizzotic: login takes no arguments")
		return 2
	}
	agent := *agentFlag
	cid := *clientID
	if cid == "" {
		cid = env.Getenv("QUIZZOTIC_LOGIN_CLIENT_ID")
	}
	if cid == "" {
		cfg, err := loadFileConfig(env.Getenv)
		if err != nil {
			fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
			return 1
		}
		cid = cfg.LoginClientID
	}
	if cid == "" {
		cid = DefaultHumanClientID
		if agent {
			cid = DefaultAgentClientID
		}
	}

	base, err := serverBase(*server, env)
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	iss, err := resolveIssuer(ctx, env, *issuer, base)
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	d, err := discover(ctx, env.HTTP, iss)
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}

	var tok tokenResponse
	useDevice := *device
	if !useDevice {
		tok, err = loopbackLogin(ctx, env.HTTP, d, cid, func(authURL string) error {
			if oerr := env.OpenBrowser(authURL); oerr != nil {
				if d.DeviceAuthorizationEndpoint != "" {
					fmt.Fprintf(env.Stderr, "No browser could be opened (%v); using the device flow.\n", oerr)
					return errNoBrowser
				}
				fmt.Fprintf(env.Stderr, "Could not open a browser (%v).\n", oerr)
			}
			fmt.Fprintf(env.Stderr, "Open this URL to sign in:\n  %s\n", authURL)
			return nil
		})
		if errors.Is(err, errNoBrowser) {
			useDevice, err = true, nil
		}
	}
	if useDevice {
		tok, err = deviceLogin(ctx, env.HTTP, d, cid, env.Stderr, env.Sleep)
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: login failed: %v\n", err)
		return 1
	}
	cred, err := credentialFrom(env.Now(), iss, cid, d.TokenEndpoint, tok, "")
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: login failed: %v\n", err)
		return 1
	}
	path := credentialsPath(env.Getenv)
	cf, err := loadCredentials(path)
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	cf[profileName(agent)] = cred
	if err := saveCredentials(path, cf); err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: saving credentials: %v\n", err)
		return 1
	}
	kind := "Logged in"
	if agent {
		kind = "Logged in as an agent"
	}
	note := ""
	if cred.RefreshToken == "" {
		note = " (the identity provider issued no refresh token, so you will need to log in again when it expires)"
	}
	fmt.Fprintf(env.Stdout, "%s with client %s. Credentials saved to %s%s\n", kind, cid, path, note)
	return 0
}

// Logout implements "quizzotic logout [--agent] [--all]": it forgets the
// stored login (the human one by default).
func Logout(_ context.Context, args []string, env Env) int {
	env.applyDefaults()
	fs := flag.NewFlagSet("quizzotic logout", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	agent := fs.Bool("agent", false, "forget the agent login instead of the human one")
	all := fs.Bool("all", false, "forget every stored login")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	path := credentialsPath(env.Getenv)
	cf, err := loadCredentials(path)
	if err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	removed := false
	switch {
	case *all:
		removed = len(cf) > 0
		cf = credentialsFile{}
	default:
		name := profileName(*agent)
		_, removed = cf[name]
		delete(cf, name)
	}
	if err := saveCredentials(path, cf); err != nil {
		fmt.Fprintf(env.Stderr, "quizzotic: %v\n", err)
		return 1
	}
	if removed {
		fmt.Fprintln(env.Stdout, "Logged out.")
	} else {
		fmt.Fprintln(env.Stdout, "Not logged in.")
	}
	return 0
}
