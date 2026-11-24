package cmd

import (
	"github.com/helviojunior/mail_migration/internal/ascii"
	"github.com/spf13/cobra"
)

var authDestinationCmd = &cobra.Command{
	Use:   "auth-destination",
	Short: "Interactive login to the DESTINATION mailbox (tenant to migrate TO)",
	Long: ascii.LogoHelp(ascii.Markdown(`
# auth-destination

Performs an interactive device-code login against the **destination** tenant and
caches the delegated access/refresh token. Run this once before ` + "`migrate`" + `.

The signed-in user's own mailbox is the one that will be written.`)),
	Example: `
   - o365migrate auth-destination
   - o365migrate auth-destination --tenant fabrikam.onmicrosoft.com`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDeviceAuth("destination", opts.Auth.DestCache)
	},
}

func init() {
	rootCmd.AddCommand(authDestinationCmd)
	authDestinationCmd.Flags().StringVar(&opts.Auth.DestCache, "cache-file", "", "Token cache file (default ~/.o365migrate/destination.json)")
}
