package shared

// DefaultProgramName is the tools CLI's name unless the host program sets
// another one through cli.SetProgramName.
const DefaultProgramName = "phins-news-engine-tools"

// DefaultServiceBinaryName is the service binary's file name (under build/)
// that the editorial cycle deploys, unless the host program sets another
// one through cli.SetServiceBinaryName.
const DefaultServiceBinaryName = "phins-news-engine"

// DefaultRemoteSyncScriptPath is the remote database sync script (relative to
// the tools binary's working directory) the editorial cycle's sync step runs,
// unless the host program sets another one through
// cli.SetRemoteSyncScriptPath.
const DefaultRemoteSyncScriptPath = "scripts/sync-remote-sqlite.sh"

// DefaultStatsLogPath is the default of `stats --log-path` (the process log
// scanned for cycle activity) unless the host program sets another one
// through cli.SetStatsLogPath.
const DefaultStatsLogPath = "data/logs/out.log"

var (
	// ProgramName is the tools CLI's name, used in the root command's Use
	// line, in hints printed to the operator and as the tools binary's file
	// name under build/.
	ProgramName = DefaultProgramName

	// ServiceBinaryName is the service binary's file name under build/.
	ServiceBinaryName = DefaultServiceBinaryName

	// RemoteSyncScriptPath is the remote sync script the editorial cycle runs.
	RemoteSyncScriptPath = DefaultRemoteSyncScriptPath

	// StatsLogPath is the process log `stats` scans unless --log-path is given.
	StatsLogPath = DefaultStatsLogPath
)

// DefaultDictionaryPath is the default of every --dictionary flag unless the
// host program sets another one through cli.SetDefaultDictionaryPath.
const DefaultDictionaryPath = "configs/dictionary.yml"
