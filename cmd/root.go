package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"github.com/helviojunior/o365-mail-migration/internal/ascii"
	"github.com/helviojunior/o365-mail-migration/internal/tools"
	"github.com/helviojunior/o365-mail-migration/pkg/graph"
	"github.com/helviojunior/o365-mail-migration/pkg/log"
	"github.com/helviojunior/o365-mail-migration/pkg/runner"
	"github.com/spf13/cobra"
)

const sqlitePrefix = "sqlite:///"

var (
	opts    = &runner.Options{}
	baseDir = ""
)

var rootCmd = &cobra.Command{
	Use:   "o365migrate",
	Short: "O365 Mail Migration — migrate mailboxes between Microsoft 365 tenants",
	Long:  ascii.Logo(),
	Example: `
   - o365migrate auth-source
   - o365migrate auth-destination
   - o365migrate migrate --dry-run
   - o365migrate migrate`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if cmd.CalledAs() != "version" && !opts.Logging.Silence {
			fmt.Println(ascii.Logo())
		}

		if opts.Logging.Silence {
			log.EnableSilence()
		}
		if opts.Logging.Debug && !opts.Logging.Silence {
			log.EnableDebug()
			log.Debug("debug logging enabled")
		}

		usr, err := user.Current()
		if err != nil {
			return err
		}
		opts.Writer.UserPath = usr.HomeDir

		baseDir = filepath.Join(usr.HomeDir, ".o365migrate")
		if _, err := tools.CreateDir(baseDir); err != nil {
			return fmt.Errorf("creating state dir %s: %w", baseDir, err)
		}

		if opts.Writer.CtrlDbURI == "" {
			opts.Writer.CtrlDbURI = sqlitePrefix + filepath.Join(baseDir, "state.db")
		}
		if opts.Auth.SourceCache == "" {
			opts.Auth.SourceCache = filepath.Join(baseDir, "source.json")
		}
		if opts.Auth.DestCache == "" {
			opts.Auth.DestCache = filepath.Join(baseDir, "destination.json")
		}

		return nil
	},
}

// Execute runs the root command (same error-banner behaviour as enumdns).
func Execute() {
	if err := ascii.SetConsoleColors(); err != nil {
		log.Error("failed to set console colors", "err", err)
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		ascii.ClearLine()
		ascii.ShowCursor()
		log.Warn("interrupted, shutting down...")
		os.Exit(2)
	}()

	rootCmd.CompletionOptions.DisableDefaultCmd = true
	rootCmd.SilenceErrors = true

	if err := rootCmd.Execute(); err != nil {
		var name string
		if c, _, cerr := rootCmd.Find(os.Args[1:]); cerr == nil {
			name = c.Name()
		}
		v := "\n"
		if name != "" {
			v += fmt.Sprintf("An error occured running the `%s` command\n", name)
		} else {
			v += "An error has occured. "
		}
		v += "The error was:\n\n" + fmt.Sprintf("```%s```", err)
		fmt.Println(ascii.Markdown(v))
		os.Exit(1)
	}

	time.Sleep(time.Second / 4)
	ascii.ShowCursor()
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&opts.Logging.Debug, "debug-log", "D", false, "Enable debug logging")
	rootCmd.PersistentFlags().BoolVarP(&opts.Logging.Silence, "quiet", "q", false, "Silence (almost all) logging")

	rootCmd.PersistentFlags().StringVar(&opts.Auth.ClientID, "client-id", graph.DefaultClientID, "Public client (application) id used for interactive login")
	rootCmd.PersistentFlags().StringVar(&opts.Auth.Tenant, "tenant", graph.DefaultTenant, "Tenant to authenticate against ('common', 'organizations' or a tenant id/domain)")
	rootCmd.PersistentFlags().StringVar(&opts.Auth.Authority, "authority", graph.DefaultAuthority, "Entra ID login authority host (without the tenant segment)")
	rootCmd.PersistentFlags().StringArrayVar(&opts.Auth.Scopes, "scope", graph.DefaultScopes, "Delegated OAuth scope (repeatable); defaults to the Microsoft Graph Mail scopes")

	rootCmd.PersistentFlags().StringVar(&opts.Writer.CtrlDbURI, "state-db-uri", "", "Migration state DB URI (default sqlite at ~/.o365migrate/state.db)")
	rootCmd.PersistentFlags().BoolVar(&opts.Writer.DbDebug, "state-db-debug", false, "Enable state DB query debug logging (verbose)")
}
