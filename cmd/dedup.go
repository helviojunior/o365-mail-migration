package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/helviojunior/o365-mail-migration/internal/ascii"
	"github.com/helviojunior/o365-mail-migration/pkg/graph"
	"github.com/helviojunior/o365-mail-migration/pkg/log"
	"github.com/spf13/cobra"
)

var (
	dedupAccount = "destination"
	dedupDryRun  = false
	dedupYes     = false
	dedupThreads = 4
)

var dedupCmd = &cobra.Command{
	Use:   "dedup",
	Short: "Remove duplicate messages (same Internet-Message-ID) within each folder",
	Long: ascii.LogoHelp(ascii.Markdown(`
# dedup

Finds messages that appear more than once **within the same folder** (identical
` + "`internetMessageId`" + `) in an authenticated mailbox and deletes the
extras, keeping one copy. Useful to clean up duplicates left by repeated
migration runs.

Operates on the **destination** mailbox by default (` + "`--account source`" + `
to target the source). Messages without an Internet-Message-ID (e.g. some
drafts) are left untouched. Deleted messages go to Deleted Items / Recoverable
Items like any Graph delete.`)),
	Example: `
   - o365migrate dedup --dry-run
   - o365migrate dedup
   - o365migrate dedup --account source`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		cacheFile := opts.Auth.DestCache
		if dedupAccount == "source" {
			cacheFile = opts.Auth.SourceCache
		}

		auth, err := graph.NewAuthenticator(opts.Auth.ClientID, opts.Auth.Tenant, opts.Auth.Authority, opts.Auth.Scopes, cacheFile)
		if err != nil {
			return err
		}
		client := graph.NewClient(auth)

		me, err := client.Me(ctx)
		if err != nil {
			return err
		}

		folders, err := client.ListFolders(ctx)
		if err != nil {
			return fmt.Errorf("listing folders: %w", err)
		}

		// Plan: per folder, the message ids to delete (every occurrence of an
		// internetMessageId after the first).
		plan := map[string][]string{}
		var totalDup int
		for _, f := range folders {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			seen := map[string]bool{}
			var toDel []string
			if err := client.ListMessages(ctx, f.ID, func(m graph.Message) error {
				if m.InternetMessageID == "" {
					return nil
				}
				if seen[m.InternetMessageID] {
					toDel = append(toDel, m.ID)
				} else {
					seen[m.InternetMessageID] = true
				}
				return nil
			}); err != nil {
				log.Warnf("skipping folder %s: %v", f.Path, err)
				continue
			}
			if len(toDel) > 0 {
				plan[f.Path] = toDel
				totalDup += len(toDel)
				log.Warnf("%s: %d duplicate(s)", f.Path, len(toDel))
			}
		}

		fmt.Println(ascii.Markdown(fmt.Sprintf(
			"## Deduplicate\n\nMailbox: **%s** (%s)\n\nDuplicate messages found: **%d**\n",
			me.UserPrincipalName, dedupAccount, totalDup)))

		if totalDup == 0 {
			log.Warn("nothing to do")
			return nil
		}
		if dedupDryRun {
			log.Warn("dry-run: nothing deleted")
			return nil
		}
		if !dedupYes {
			if !confirm(fmt.Sprintf("Permanently delete %d duplicate message(s) from %s?", totalDup, me.UserPrincipalName)) {
				log.Warn("aborted by user")
				return nil
			}
		}

		// Delete with a small worker pool.
		threads := dedupThreads
		if threads < 1 {
			threads = 1
		}
		sem := make(chan struct{}, threads)
		var wg sync.WaitGroup
		var deleted, failed int64

		for _, ids := range plan {
			for _, id := range ids {
				if ctx.Err() != nil {
					break
				}
				wg.Add(1)
				sem <- struct{}{}
				go func(id string) {
					defer wg.Done()
					defer func() { <-sem }()
					if err := client.DeleteMessage(ctx, id); err != nil {
						atomic.AddInt64(&failed, 1)
						log.Debugf("delete failed: %v", err)
					} else {
						atomic.AddInt64(&deleted, 1)
					}
				}(id)
			}
		}
		wg.Wait()

		fmt.Println(ascii.Markdown(fmt.Sprintf(
			"## Summary\n\n- Deleted: **%d**\n- Failed: **%d**\n", deleted, failed)))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(dedupCmd)
	dedupCmd.Flags().StringVar(&dedupAccount, "account", "destination", "Which mailbox to clean: 'destination' or 'source'")
	dedupCmd.Flags().BoolVar(&dedupDryRun, "dry-run", false, "Report duplicates without deleting")
	dedupCmd.Flags().BoolVarP(&dedupYes, "yes", "y", false, "Skip the confirmation prompt")
	dedupCmd.Flags().IntVarP(&dedupThreads, "threads", "t", 4, "Concurrent delete operations")
}
