package action

// PathParam is a file path param of a both-mode action and the configured
// value it overrides.
type PathParam struct{ Name, Value, Configured string }

// RefusePathOverrides rejects, in production, any path param that differs
// from its configured value (like stats.report's log_path): any
// authenticated caller could otherwise make the admin read arbitrary files.
func RefusePathOverrides(env Env, params ...PathParam) error {
	if env.Mode != ModeProduction {
		return nil
	}
	for _, p := range params {
		if p.Value != "" && p.Value != p.Configured {
			return &Error{Code: CodeInvalidParams, Message: p.Name + " cannot be changed in production"}
		}
	}
	return nil
}
