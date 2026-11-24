// Package ascii holds the banner and small terminal helpers (same role as in
// enumdns).
package ascii

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/helviojunior/mail_migration/internal/version"
)

const logo = `
                _ _               _                 _   _
 _ __ ___   __ _(_) |  _ __ ___ (_) __ _ _ __ __ _| |_(_) ___  _ __
| '_ ` + "`" + ` _ \ / _` + "`" + ` | | | | '_ ` + "`" + ` _ \| |/ _` + "`" + ` | '__/ _` + "`" + ` | __| |/ _ \| '_ \
| | | | | | (_| | | | | | | | | | | (_| | | | (_| | |_| | (_) | | | |
|_| |_| |_|\__,_|_|_| |_| |_| |_|_|\__, |_|  \__,_|\__|_|\___/|_| |_|
                                   |___/
 Mailbox migration between Microsoft 365 tenants`

// Logo returns the banner including the current version.
func Logo() string {
	return logo + fmt.Sprintf("\n ver. %s by Helvio Junior (m4v3r1ck)\n", version.Version)
}

// LogoHelp prefixes a help/long text with the banner.
func LogoHelp(s string) string {
	return Logo() + "\n" + s
}

// Markdown renders markdown for the terminal, falling back to the raw text.
func Markdown(md string) string {
	out, err := glamour.Render(md, "dark")
	if err != nil {
		return md
	}
	return strings.TrimRight(out, "\n")
}

// SetConsoleColors is a no-op placeholder kept for parity with enumdns's
// Execute() wiring; colouring is handled by the logger/glamour.
func SetConsoleColors() error { return nil }

// ClearLine erases the current terminal line.
func ClearLine() { fmt.Fprint(os.Stderr, "\r\033[K") }

// ShowCursor makes the cursor visible again (after spinners, etc.).
func ShowCursor() { fmt.Fprint(os.Stderr, "\033[?25h") }
