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

// logoArt is the coloured "O365" banner. ANSI SGR escapes are written with a
// bare "[0..." for source legibility; the ESC byte is substituted in by the
// package-level logo var below.
const logoArt = ` [0;37m ▄▀▀▄  ▄[0;97;47m▄▄▄[0;37m▄   ▄[0;97;47m▄▄▄[0;37m  [0;97m██[0;97;47m▄▄▄[0;37m▄ [0m
  [0;97;47m▄[0;97;41m█[0;37m  [0;97;41m█[0;97;47m▄[0;37m ▀[0;97;47m▀▀[0;37m [0;97;41m█[0;97;47m▄[0;37m [0;97;47m▄[0;97;41m█[0;37m [0;97;47m▀▀[0;37m  [0;97;47m█[0;97;41m█[0;37m   [0;97;41m▒[0;37m█[0m
  [0;97;41m██[0;37m  [0;97;41m██[0;37m     [0;97;41m██[0;37m [0;97;41m██[0;37m     [0;97m██▄▄▄[0;37m  [0m
  [0;97;41m██[0;37m  [0;97;41m██[0;37m   ▄▄[0;97;41m▓▓[0;37m [0;97;41m▓▓[0;97;47m▒▀[0;97;41m█[0;97m▄[0;37m  [0;97m▀[0;37m▀[0;97m▀▀[0;97;41m▒[0;37m▄[0m
  [0;91;41m▓▓[0;37m  [0;91;41m▓▓[0;37m     [0;91;41m▓▓[0;37m [0;91;41m▓▓[0;37m  [0;97m█[0;97;47m▄[0;37m [0;91;47m▄▄[0;37m   [0;91;41m▓▓[0m
  [0;91;41m▀█[0;37m  [0;91;41m█▀[0;37m  [0;31m▄▄░[0;91;41m█▀[0;37m [0;91;41m▀█[0;37m  [0;91;41m▓▓[0;37m [0;91;41m▒▓[0;37m   [0;91;41m██[0m
  [0;37m [0;31m▀▄▄▀[0;37m   [0;91;41m ▀▀[0;31m▀[0;37m   [0;31m▀[0;91;41m▀▀ [0;31m▀[0;37m [0;91m███[0;91;41m██[0;91m▀[0;37m [0m`

// logo is logoArt with real ESC bytes substituted for the bare "[0" markers.
var logo = strings.ReplaceAll(logoArt, "[0", "\x1b[0")

// Logo returns the banner including the current version. The version/author
// line is right-aligned to the subtitle width.
func Logo() string {
	subtitle := " Mailbox migration between Microsoft 365 tenants"
	ver := fmt.Sprintf("v%s by Helvio Junior (m4v3r1ck)", version.Version)
	if pad := len([]rune(subtitle)) - len([]rune(ver)); pad > 0 {
		ver = strings.Repeat(" ", pad) + ver
	}
	return "\n" + logo +
		"    Mail Migration\n\n" + subtitle + "\n" + ver + "\n"
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
