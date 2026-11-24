package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/helviojunior/o365-mail-migration/internal/ascii"
	"github.com/helviojunior/o365-mail-migration/pkg/graph"
	"github.com/helviojunior/o365-mail-migration/pkg/log"
	"github.com/helviojunior/o365-mail-migration/pkg/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// resolveRoleAccount pins auth to the correct cached account for a role before
// it is used for silent token acquisition. A single token-cache file can hold
// several accounts (every interactive sign-in is appended), so acting as an
// arbitrary one would migrate the wrong mailbox. Resolution order:
//
//  1. none cached        -> error, tell the user to run the matching auth command;
//  2. exactly one cached -> use it;
//  3. the role's saved account is still in the cache -> use it (silent, no prompt);
//  4. several cached and ambiguous -> ask the user which account to use, then
//     remember the choice so later runs are silent.
func resolveRoleAccount(ctx context.Context, auth *graph.Authenticator, db *gorm.DB, role, cacheFile string) error {
	accounts, err := auth.Accounts(ctx)
	if err != nil {
		return fmt.Errorf("reading cached accounts for the %s mailbox: %w", role, err)
	}
	if len(accounts) == 0 {
		return fmt.Errorf("no cached account for the %s mailbox; run auth-%s first", role, role)
	}

	if len(accounts) == 1 {
		auth.SetHomeAccountID(accounts[0].HomeAccountID)
		return nil
	}

	// Several accounts in the cache. Prefer the one already saved for this role.
	var saved models.Account
	if err := db.Where("role = ?", role).First(&saved).Error; err == nil && saved.HomeID != "" {
		for _, acc := range accounts {
			if acc.HomeAccountID == saved.HomeID {
				auth.SetHomeAccountID(acc.HomeAccountID)
				return nil
			}
		}
	}

	// Ambiguous: ask the user which account is the intended one for this role.
	chosen, err := chooseAccount(role, accounts)
	if err != nil {
		return err
	}
	auth.SetHomeAccountID(chosen.HomeAccountID)
	rememberAccount(db, role, chosen, cacheFile)
	return nil
}

// chooseAccount prints the cached accounts and reads the user's selection.
func chooseAccount(role string, accounts []graph.AccountInfo) (graph.AccountInfo, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "### Select the %s account\n\nMore than one account is signed in; choose which one to use:\n\n", role)
	for i, acc := range accounts {
		name := acc.Username
		if name == "" {
			name = acc.HomeAccountID
		}
		fmt.Fprintf(&b, "%d. `%s`\n", i+1, name)
	}
	fmt.Println(ascii.Markdown(b.String()))

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("Account for the %s mailbox [1-%d]: ", role, len(accounts))
		line, err := reader.ReadString('\n')
		if err != nil {
			return graph.AccountInfo{}, fmt.Errorf("reading account selection: %w", err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && n >= 1 && n <= len(accounts) {
			return accounts[n-1], nil
		}
		log.Warnf("Enter a number between 1 and %d.", len(accounts))
	}
}

// rememberAccount persists the user's account choice so later runs skip the
// prompt. Failure to save is non-fatal: the run proceeds with the pinned
// account and the user is simply asked again next time.
func rememberAccount(db *gorm.DB, role string, acc graph.AccountInfo, cacheFile string) {
	row := models.Account{
		Role:      role,
		HomeID:    acc.HomeAccountID,
		UPN:       acc.Username,
		CacheFile: cacheFile,
		UpdatedAt: time.Now(),
	}
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "role"}},
		DoUpdates: clause.AssignmentColumns([]string{"home_id", "upn", "cache_file", "updated_at"}),
	}).Create(&row).Error; err != nil {
		log.Warnf("Could not remember the %s account selection: %v", role, err)
	}
}
