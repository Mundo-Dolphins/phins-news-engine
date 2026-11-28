# ADR 0044: Config file editor

## Status

Accepted

## Context

`settings.yml` is over a thousand lines, and the five hand-maintained policy
files it references (`sources.yml`, `source-policies.yml`,
`editorial_vetoes.yml`, `league_alias_rules.yml`, `event_clustering_rules.yml`)
carry a running rationale in their comments. Editing them by hand meant reading
the whole file, finding the field, and risking a comment, an indent, or a type.
The owner wanted an easier way to change config from the panel while running
locally, without the panel ever able to break the running bot.

## Decision

The engine gains `infra/configdoc`, a comment-preserving YAML document editor
built directly on `gopkg.in/yaml.v3` nodes. Go's `yaml.Marshal` does not
preserve comments, so the only way to change a value and keep the rest of the
file byte-for-byte is to edit the parsed `*yaml.Node` tree in place and
re-serialize it. `configdoc` loads a file into its node tree, applies a list of
surgical `Edit`s addressed by key path (arrays by integer index) that set, add
or remove a leaf while copying each changed node's comment from the old one,
and renders the tree back. The unchanged nodes keep their original formatting
and comments; only the touched leaves change.

The bot registers three local-only actions over `configdoc`: `config.files`
(lists the six editable files and which resolve to a real path), `config.read`
(returns one file's decoded value, the comment beside each field, and its
top-level sections) and `config.apply` (applies the edits). `config.apply`
resolves the file path from the loaded settings, applies the edits in memory,
writes the candidate to a temp file in the same directory, and validates it with
the file's own loader (`config.Load`, `sourcesfile.Load`, etc.) before touching
the real file; an invalid candidate is removed and its error returned, the file
unchanged. On success it writes a timestamped backup to `data/config-backups/`
and renames the temp file over the real one under `ConfigFilesMu`. The edits
are passed as a JSON-encoded string param because the action framework's params
are flat-typed (string, bool, number, `[]string`).

The SPA gains `/settings/config` behind the `LocalOnly` guard: a file switcher
and a recursive `ConfigForm` over the decoded value and comments. Each scalar is
a typed input, nested sections and lists are collapsible, and keys or list items
can be added and removed. The screen keeps the original value and the working
tree, and on save diffs them to send only the changed leaves.

## Decisions

**Validate before write.** The candidate is checked by the same loader the
pipeline uses at start, so a change can never leave a config file the pipeline
would reject; validation runs on a temp file so the real file is never invalid.

**Surgical, comment-preserving writes.** The edit goes through the node tree,
not a full re-marshal, so comments and untouched lines survive; the SPA sends
only the changed leaves, not the whole document.

**Local only.** Config edits change the operator's committed files; they reach
the Pi through the normal git/deploy flow. The editor works on the local copy,
so a mis-edit is a local mistake with a backup and a git history, not a remote
outage.

## Consequences

A file whose value is `map[string]any` (the scraping domains, per-event handler
config) renders as free-form key/value pairs. Durations stay strings in the file
(the form shows them as text). `config.apply` is serialized with the other
config-file writers through `ConfigFilesMu`, so it never interleaves with a CLI
that rewrites the same file. `configdoc` is generic and host-agnostic; only the
bot wires it to the six files and their loaders.
