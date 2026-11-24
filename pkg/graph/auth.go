package graph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
)

// DefaultClientID is the first-party "Microsoft Graph Command Line Tools"
// public client. It supports the device-code flow and delegated Mail scopes
// with no app registration of your own. Override with --client-id when a tenant
// blocks it or requires your own consented app.
const DefaultClientID = "14d82eec-204b-4c2f-b7e8-296a70dad67c"

// DefaultTenant keeps the login generic across organizations.
const DefaultTenant = "organizations"

// Scopes are the delegated permissions required to read the source mailbox and
// write the destination mailbox. MSAL adds offline_access/openid/profile.
var Scopes = []string{
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

// NewAuthenticator builds a public client for the given tenant, persisting
// tokens to cacheFile.
func NewAuthenticator(clientID, tenant, cacheFile string) (*Authenticator, error) {
	if clientID == "" {
		clientID = DefaultClientID
	}
	if tenant == "" {
		tenant = DefaultTenant
	}
	authority := fmt.Sprintf("https://login.microsoftonline.com/%s", tenant)

	client, err := public.New(clientID,
		public.WithAuthority(authority),
		public.WithCache(&fileCache{path: cacheFile}),
	)
	if err != nil {
		return nil, err
	}
	return &Authenticator{client: client, scopes: Scopes, cacheFile: cacheFile}, nil
}

// SetHomeAccountID pins silent token acquisition to a specific cached account.
func (a *Authenticator) SetHomeAccountID(id string) { a.homeID = id }

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
		return DeviceCodeResult{}, err
	}

	if prompt != nil {
		prompt(dc.Result.Message)
	}

	res, err := dc.AuthenticationResult(ctx)
	if err != nil {
		return DeviceCodeResult{}, err
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

	account := accounts[0]
	if a.homeID != "" {
		for _, acc := range accounts {
			if acc.HomeAccountID == a.homeID {
				account = acc
				break
			}
		}
	}

	res, err := a.client.AcquireTokenSilent(ctx, a.scopes, public.WithSilentAccount(account))
	if err != nil {
		return "", fmt.Errorf("silent token refresh failed (re-run auth): %w", err)
	}
	return res.AccessToken, nil
}
