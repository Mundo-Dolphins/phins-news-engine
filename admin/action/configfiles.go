package action

import "sync"

// ConfigFilesMu serialises the admin actions that rewrite files under
// configs/ or data/ (catalogs, the team dictionary, the roster), so two
// panel or MCP calls running in parallel cannot interleave a
// load-modify-save of the same file. Hold it for the whole
// read-check-write sequence, including the proposal status update.
var ConfigFilesMu sync.Mutex
