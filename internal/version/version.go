package version

// These variables are set at build time via -ldflags -X (see Makefile).
var (
	Version     = "dev"
	GitHash     = ""
	GoBuildEnv  = ""
	GoBuildTime = ""
)
