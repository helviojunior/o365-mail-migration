package runner

// Options is the single, flag-bound configuration struct (same pattern as
// enumdns).
type Options struct {
	Logging struct {
		Debug   bool
		Silence bool
	}

	Writer struct {
		UserPath  string // resolved home dir
		CtrlDbURI string // control/state DB (sqlite by default)
		DbDebug   bool
	}

	Auth struct {
		ClientID    string
		Tenant      string
		SourceCache string
		DestCache   string
	}

	Migrate struct {
		Threads        int
		IncludeFolders []string
		ExcludeFolders []string
		DryRun         bool
		Yes            bool
		Force          bool
		MaxSizeMB      int
		FixDates       bool // also set original received/sent via MAPI extended props
		VerifyDates    bool // re-read each copy and report date drift
	}
}
