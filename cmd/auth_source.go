package cmd

import (
	"github.com/helviojunior/mail_migration/internal/ascii"
	"github.com/spf13/cobra"
)

var authSourceCmd = &cobra.Command{
	Use:   "auth-source",
	Short: "Interactive login to the SOURCE mailbox (tenant to migrate FROM)",
	Long: ascii.LogoHelp(ascii.Markdown(`
# auth-source

Performs an interactive device-code login against the **source** tenant and
caches the delegated access/refresh token. Run this once before ` + "`migrate`" + `.

The signed-in user's own mailbox is the one that will be read.`)),
	Example: `
   - mailmigrate auth-source
   - mailmigrate auth-source --tenant contoso.onmicrosoft.com
   - mailmigrate auth-source --client-id <your-app-id>`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDeviceAuth("source", opts.Auth.SourceCache)
	},
}

func init() {
	rootCmd.AddCommand(authSourceCmd)
	authSourceCmd.Flags().StringVar(&opts.Auth.SourceCache, "cache-file", "", "Token cache file (default ~/.mailmigrate/source.json)")
}
