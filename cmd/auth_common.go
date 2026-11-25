package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/helviojunior/o365-mail-migration/internal/ascii"
	"github.com/helviojunior/o365-mail-migration/pkg/database"
	"github.com/helviojunior/o365-mail-migration/pkg/graph"
	"github.com/helviojunior/o365-mail-migration/pkg/log"
	"github.com/helviojunior/o365-mail-migration/pkg/models"
	"gorm.io/gorm/clause"
)

// runDeviceAuth performs the interactive device-code login for a role
// ("source" or "destination"), persists the token cache, and records the
// resolved identity in the state DB.
func runDeviceAuth(role, cacheFile string) error {
	ctx := context.Background()

	auth, err := graph.NewAuthenticator(opts.Auth.ClientID, opts.Auth.Tenant, opts.Auth.Authority, graph.ScopesForRole(role, opts.Auth.Scopes), cacheFile)
	if err != nil {
		return fmt.Errorf("building authenticator: %w", err)
	}

	log.Warnf("Starting interactive login for the %s mailbox...", role)

	res, err := auth.DeviceCodeLogin(ctx, func(message string) {
		fmt.Println()
		fmt.Println(ascii.Markdown("### Sign in (" + role + ")\n\n" + message))
		fmt.Println()
	})
	if err != nil {
		return fmt.Errorf("device-code login failed: %w", err)
	}

	// Pin the account we just signed into. The cache file may already hold other
	// accounts from earlier logins, and silent acquisition would otherwise act
	// as an arbitrary one — recording the wrong person for this role.
	auth.SetHomeAccountID(res.HomeAccountID)

	client := graph.NewClient(auth)
	me, err := client.Me(ctx)
	if err != nil {
		return fmt.Errorf("fetching signed-in identity: %w", err)
	}

	db, err := database.Connect(opts.Writer.CtrlDbURI, opts.Writer.DbDebug)
	if err != nil {
		return fmt.Errorf("opening state DB: %w", err)
	}

	upn := me.UserPrincipalName
	tenant := opts.Auth.Tenant
	if at := strings.LastIndex(upn, "@"); at >= 0 && tenant == graph.DefaultTenant {
		tenant = upn[at+1:]
	}

	acc := models.Account{
		Role:      role,
		HomeID:    res.HomeAccountID,
		UPN:       upn,
		UserID:    me.ID,
		Tenant:    tenant,
		CacheFile: cacheFile,
		UpdatedAt: time.Now(),
	}
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "role"}},
		DoUpdates: clause.AssignmentColumns([]string{"home_id", "upn", "user_id", "tenant", "cache_file", "updated_at"}),
	}).Create(&acc).Error; err != nil {
		return fmt.Errorf("saving account: %w", err)
	}

	log.Warnf("Authenticated %s mailbox: %s", role, upn)
	fmt.Println(ascii.Markdown(fmt.Sprintf("✅ **%s** signed in as `%s` (token cached at `%s`).", role, upn, cacheFile)))
	return nil
}
