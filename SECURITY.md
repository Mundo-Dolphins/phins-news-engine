# Security policy

## Reporting a vulnerability

Use **Security > Report a vulnerability** (a private GitHub security
advisory) in this repository. Do not open a public issue with the details
of a vulnerability, and never paste tokens, API keys, session files or
database contents into issues or pull requests.

Please include the affected version (tag or commit), the package, and a
minimal way to reproduce the problem. Reports are handled on a best-effort
basis by the maintainers.

## Supported versions

The library is in `v0.x`. Fixes land on `main` and ship in the next patch
or minor release; older `v0.x` releases are not patched. Pin a version in
your `go.mod` and upgrade to receive fixes.

## Using the library safely

- Credentials (bot tokens, LLM and search API keys) belong in the
  environment or in a file outside the repository, never in `settings.yml`
  under version control and never on the command line, where other users
  can read the process list.
- Feed URLs come from your source catalog and are treated as untrusted:
  loopback, private, link-local and cloud metadata destinations are refused
  unless you opt in.
- The host program owns the SQLite database, the model registry and any
  session files. The library creates them `0600` (directories `0700`).

The technical hardening (timeouts, body caps, redirect policy, SSRF guard,
proxy caveats, redaction of credentials, file modes and command execution)
is described in [docs/SECURITY.md](docs/SECURITY.md).
