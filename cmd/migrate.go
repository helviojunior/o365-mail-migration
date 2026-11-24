package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/helviojunior/mail_migration/internal/ascii"
	"github.com/helviojunior/mail_migration/pkg/database"
	"github.com/helviojunior/mail_migration/pkg/graph"
	"github.com/helviojunior/mail_migration/pkg/log"
	"github.com/helviojunior/mail_migration/pkg/runner"
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate all e-mail from the source mailbox to the destination mailbox",
	Long: ascii.LogoHelp(ascii.Markdown(`
# migrate

Copies every folder and message (Inbox, Archive, Deleted Items, Junk, Sent,
Drafts and custom folders) from the authenticated **source** mailbox into the
authenticated **destination** mailbox, preserving the original received/sent
dates (via MIME re-import, reinforced by MAPI extended properties) plus read
state, flags and categories.

Date handling:
- Primary: the original MIME (with its ` + "`Date:`/`Received:`" + ` headers) is
  re-imported, so attachments and inline content are kept byte-for-byte.
- Reinforcement (` + "`--fix-dates`" + `, on by default): the received/sent
  timestamps are also written via MAPI extended properties (0x0E06 / 0x0039).
- ` + "`--verify-dates`" + ` re-reads each copy and reports any date drift.

Run ` + "`auth-source`" + ` and ` + "`auth-destination`" + ` first. The run is
resumable: already-migrated messages are skipped unless ` + "`--force`" + ` is set.`)),
	Example: `
   - o365migrate migrate --dry-run
   - o365migrate migrate
   - o365migrate migrate --yes --threads 4
   - o365migrate migrate --include-folder Inbox --include-folder Archive`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		// Source authenticator (read-only side).
		srcAuth, err := graph.NewAuthenticator(opts.Auth.ClientID, opts.Auth.Tenant, opts.Auth.Authority, opts.Auth.Scopes, opts.Auth.SourceCache)
		if err != nil {
			return err
		}
		dstAuth, err := graph.NewAuthenticator(opts.Auth.ClientID, opts.Auth.Tenant, opts.Auth.Authority, opts.Auth.Scopes, opts.Auth.DestCache)
		if err != nil {
			return err
		}

		src := graph.NewClient(srcAuth)
		dst := graph.NewClient(dstAuth)

		db, err := database.Connect(opts.Writer.CtrlDbURI, opts.Writer.DbDebug)
		if err != nil {
			return fmt.Errorf("opening state DB: %w", err)
		}

		mig, err := runner.NewMigrator(ctx, *opts, db, src, dst)
		if err != nil {
			return err
		}

		srcUser := mig.SourceUser()
		dstUser := mig.DestUser()

		if srcUser.UserPrincipalName == dstUser.UserPrincipalName {
			return errors.New("source and destination are the same mailbox; re-run auth-destination with the target account")
		}

		fmt.Println(ascii.Markdown(fmt.Sprintf(
			"## Migration plan\n\nYou will migrate **all e-mail** from:\n\n- **Source:** `%s`\n- **Destination:** `%s`\n",
			srcUser.UserPrincipalName, dstUser.UserPrincipalName)))

		if opts.Migrate.DryRun {
			log.Warn("dry-run: listing folders only, nothing will be copied")
		} else if !opts.Migrate.Yes {
			if !confirm(fmt.Sprintf("Migrate all e-mail from %s to %s?", srcUser.UserPrincipalName, dstUser.UserPrincipalName)) {
				log.Warn("aborted by user")
				return nil
			}
		}

		stats, err := mig.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}

		summary := fmt.Sprintf(
			"## Summary\n\n- Folders: **%d**\n- Copied: **%d**\n- Skipped: **%d**\n- Failed: **%d**\n",
			stats.Folders, stats.Copied, stats.Skipped, stats.Failed)
		if opts.Migrate.VerifyDates {
			summary += fmt.Sprintf("- Date drift (received/sent mismatch): **%d**\n", stats.DateDrift)
		}
		fmt.Println(ascii.Markdown(summary))

		if errors.Is(err, context.Canceled) {
			log.Warn("interrupted; re-run migrate to resume where it stopped")
		}
		return nil
	},
}

// confirm asks a yes/no question on stdin (default no).
func confirm(question string) bool {
	fmt.Printf("%s [y/N]: ", question)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}

func init() {
	rootCmd.AddCommand(migrateCmd)

	migrateCmd.Flags().IntVarP(&opts.Migrate.Threads, "threads", "t", 4, "Concurrent message copies per folder")
	migrateCmd.Flags().BoolVar(&opts.Migrate.DryRun, "dry-run", false, "List folders and message counts without copying")
	migrateCmd.Flags().BoolVarP(&opts.Migrate.Yes, "yes", "y", false, "Skip the confirmation prompt")
	migrateCmd.Flags().BoolVarP(&opts.Migrate.Force, "force", "F", false, "Re-copy messages already recorded as migrated")
	migrateCmd.Flags().IntVar(&opts.Migrate.MaxSizeMB, "max-size", 0, "Skip messages larger than this many MB (0 = no limit)")
	migrateCmd.Flags().BoolVar(&opts.Migrate.FixDates, "fix-dates", true, "Also force the original received/sent dates via MAPI extended properties (0x0E06/0x0039)")
	migrateCmd.Flags().BoolVar(&opts.Migrate.VerifyDates, "verify-dates", false, "Re-read each copied message and report received/sent date drift (extra API call per message)")
	migrateCmd.Flags().StringArrayVar(&opts.Migrate.IncludeFolders, "include-folder", nil, "Only migrate these folder paths (repeatable; prefix match, e.g. Inbox)")
	migrateCmd.Flags().StringArrayVar(&opts.Migrate.ExcludeFolders, "exclude-folder", nil, "Skip these folder paths (repeatable; prefix match)")
}
