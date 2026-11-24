package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
)

// DefaultClientID is Thunderbird's generic multi-tenant public client. It is
// widely pre-consented across tenants and supports the device-code flow.
// Override with --client-id to use your own registered app.
const DefaultClientID = "9e5f94bc-e8a4-4e73-b8be-63364c29d753"

// DefaultTenant keeps the login generic across all account types.
const DefaultTenant = "common"

// DefaultAuthority is the Entra ID login host (without the tenant segment).
const DefaultAuthority = "https://login.microsoftonline.com"

// DefaultScopes are the delegated Microsoft Graph permissions required to read
// the source mailbox and write the destination mailbox. MSAL adds
// offline_access/openid/profile automatically. Override with --scope.
var DefaultScopes = []string{
	"https://graph.microsoft.com/Mail.ReadWrite",
	"https://graph.microsoft.com/MailboxSettings.Read",
	"https://graph.microsoft.com/User.Read",
}

// fileCache is an MSAL token-cache accessor backed by a single 0600 JSON file.
type fileCache struct {
	path string
	mu   sync.Mutex
}

func (c *fileCache) Replace(ctx context.Context, u cache.Unmarshaler, _ cache.ReplaceHints) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := os.ReadFile(c.path)
	if err != nil {
		// Missing cache is normal on first login.
		return nil
	}
	return u.Unmarshal(data)
}

func (c *fileCache) Export(ctx context.Context, m cache.Marshaler, _ cache.ExportHints) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := m.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(c.path, data, 0o600)
}

// Authenticator wraps an MSAL public client bound to one token-cache file.
type Authenticator struct {
	client    public.Client
	scopes    []string
	cacheFile string
	homeID    string // optional: pin a specific cached account
}

// NewAuthenticator builds a public client for the given tenant/authority,
// persisting tokens to cacheFile. Empty arguments fall back to the defaults.
func NewAuthenticator(clientID, tenant, authorityBase string, scopes []string, cacheFile string) (*Authenticator, error) {
	if clientID == "" {
		clientID = DefaultClientID
	}
	if tenant == "" {
		tenant = DefaultTenant
	}
	if authorityBase == "" {
		authorityBase = DefaultAuthority
	}
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}
	authority := strings.TrimRight(authorityBase, "/") + "/" + tenant

	client, err := public.New(clientID,
		public.WithAuthority(authority),
		public.WithCache(&fileCache{path: cacheFile}),
	)
	if err != nil {
		return nil, err
	}
	return &Authenticator{client: client, scopes: scopes, cacheFile: cacheFile}, nil
}

// SetHomeAccountID pins silent token acquisition to a specific cached account.
func (a *Authenticator) SetHomeAccountID(id string) { a.homeID = id }

// AccountInfo is a cached account's stable identifier and its username.
type AccountInfo struct {
	HomeAccountID string
	Username      string
}

// Accounts lists the accounts currently present in the token-cache file. A
// single cache file accumulates every interactive sign-in, so it can hold more
// than one account; callers use this to resolve which identity to act as
// instead of trusting an arbitrary order.
func (a *Authenticator) Accounts(ctx context.Context) ([]AccountInfo, error) {
	accs, err := a.client.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AccountInfo, 0, len(accs))
	for _, acc := range accs {
		out = append(out, AccountInfo{
			HomeAccountID: acc.HomeAccountID,
			Username:      acc.PreferredUsername,
		})
	}
	return out, nil
}

// DeviceCodeResult carries the identity resolved after an interactive login.
type DeviceCodeResult struct {
	AccessToken   string
	HomeAccountID string
}

// DeviceCodeLogin runs the interactive device-code flow. prompt is called once
// with the instruction message (URL + code) to show the user; the call blocks
// until the user completes authentication or ctx is cancelled.
func (a *Authenticator) DeviceCodeLogin(ctx context.Context, prompt func(message string)) (DeviceCodeResult, error) {
	dc, err := a.client.AcquireTokenByDeviceCode(ctx, a.scopes)
	if err != nil {
		return DeviceCodeResult{}, parseOAuthError(err)
	}

	if prompt != nil {
		prompt(dc.Result.Message)
	}

	res, err := dc.AuthenticationResult(ctx)
	if err != nil {
		return DeviceCodeResult{}, parseOAuthError(err)
	}
	return DeviceCodeResult{
		AccessToken:   res.AccessToken,
		HomeAccountID: res.Account.HomeAccountID,
	}, nil
}

// Token returns a valid access token, refreshing silently from the cache. It
// never prompts; run the matching auth command first.
func (a *Authenticator) Token(ctx context.Context) (string, error) {
	accounts, err := a.client.Accounts(ctx)
	if err != nil {
		return "", err
	}
	if len(accounts) == 0 {
		return "", fmt.Errorf("no cached account in %s; run the auth command first", a.cacheFile)
	}

	// Resolve which cached account to act as. MSAL returns accounts from a map,
	// so their order is unstable: never pick accounts[0] blindly when more than
	// one is cached — that is how a silent token ends up for the wrong person.
	var account public.Account
	if a.homeID != "" {
		for _, acc := range accounts {
			if acc.HomeAccountID == a.homeID {
				account = acc
				break
			}
		}
		if account.IsZero() {
			return "", fmt.Errorf("pinned account %s not found in %s; re-run the auth command", a.homeID, a.cacheFile)
		}
	} else if len(accounts) == 1 {
		account = accounts[0]
	} else {
		return "", fmt.Errorf("%d accounts cached in %s and none selected; pin one with SetHomeAccountID (re-run the auth command)", len(accounts), a.cacheFile)
	}

	res, err := a.client.AcquireTokenSilent(ctx, a.scopes, public.WithSilentAccount(account))
	if err != nil {
		return "", fmt.Errorf("silent token refresh failed (re-run auth): %w", parseOAuthError(err))
	}
	return res.AccessToken, nil
}

// oauthError is the standard OAuth 2.0 / Entra ID token-endpoint error body.
type oauthError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorCodes       []int  `json:"error_codes"`
}

// parseOAuthError turns the raw JSON blob MSAL surfaces from the token endpoint
// into a concise, human-readable error. It keeps the AADSTS line and drops the
// Trace/Correlation/Timestamp noise, and adds a hint for common cases. If the
// error carries no recognizable JSON body, the original error is returned.
func parseOAuthError(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return err
	}

	var oe oauthError
	if jErr := json.Unmarshal([]byte(s[start:end+1]), &oe); jErr != nil || oe.Error == "" {
		return err
	}

	// Keep only the first, meaningful line of the description.
	desc := oe.ErrorDescription
	for _, sep := range []string{"\r\n", "\n", "\r", "Trace ID:"} {
		if i := strings.Index(desc, sep); i >= 0 {
			desc = desc[:i]
		}
	}
	desc = strings.TrimSpace(desc)

	msg := oe.Error
	if desc != "" {
		msg = oe.Error + ": " + desc
	}

	if hint := oauthHint(oe); hint != "" {
		msg += "\n\n" + hint
	}
	return errors.New(msg)
}

// oauthHint returns actionable guidance for well-known Entra ID error codes.
func oauthHint(oe oauthError) string {
	for _, c := range oe.ErrorCodes {
		switch c {
		case 700016: // application not found in directory
			return "Hint: the default public client is not registered/consented in this tenant. " +
				"Register your own multi-tenant app in Entra ID and pass --client-id <app-id> " +
				"(optionally --tenant <tenant-id>), or have an admin consent to the default client."
		case 65001: // user/admin has not consented
			return "Hint: consent is required. Have an administrator grant admin consent for the Mail scopes, " +
				"or sign in as a user allowed to consent."
		case 50020, 50128, 50049: // account/tenant not found or external account
			return "Hint: check the --tenant value and that the account you sign in with belongs to it."
		}
	}
	return ""
}
