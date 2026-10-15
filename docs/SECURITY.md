# Security notes

This page describes the defensive defaults that the library applies. To
report a vulnerability, follow the [security policy](../SECURITY.md).

## Network client hardening

Every outbound HTTP client in the library uses the shared helpers in
`infra/netguard`. The rules below apply to the feed fetchers (`infra/rssclient`,
and the `rss`, `reddit` and `youtube` adapters built on it), the Bluesky,
ESPN scoreboard and Telegram adapters, `infra/llmclient`, `infra/websearch`
(SearXNG and Tavily), `infra/wikidata` and the roster download in
`infra/nflverse/rostersync`.

### Timeouts

No client runs without a timeout. When the configured timeout is zero or
negative, the client uses its own default:

| Client | Default timeout |
|---|---|
| RSS/Atom feeds, Bluesky, Telegram | 30 s |
| ESPN scoreboard, SearXNG, Tavily | 10 s |
| LLM chat completions | 5 min (local models can be slow) |
| Roster download | 60 s |

Wikidata requires a positive `timeout_seconds` and rejects anything else at
start-up.

### Response size limits

Every response body is read through `netguard.ReadBody`. A body that is
larger than its cap fails with an error that matches `netguard.ErrBodyTooLarge`
and states the limit. The body is never silently truncated.

| Client | Cap |
|---|---|
| RSS/Atom feeds, Bluesky | `http.max_response_bytes`; 10 MiB if unset (1 MiB for Bluesky) |
| Telegram, LLM chat completions | 1 MiB |
| SearXNG, Tavily | 2 MiB |
| ESPN scoreboard | 4 MiB (a full week of events can exceed 1 MiB) |
| Wikidata | `MaxResponseBytes`; 16 MiB if unset (entity documents are large) |
| Roster download | 16 MiB, streamed to a temporary file |

### Redirects

Clients that follow redirects use `netguard.CheckRedirect`. It follows at
most 5 redirects and refuses a redirect from `https` to `http`. Go's HTTP
client also drops the `Authorization` and `Cookie` headers when a redirect
moves to another host. SearXNG, Tavily and Wikidata never follow redirects,
so a credential can never be forwarded.

### Credentials in errors

Transport errors from Go embed the full request URL. Every client passes
these errors through `netguard.RedactError` before it wraps, logs or
returns them. The function removes the following:

- query-string values (the keys stay, for diagnostics);
- userinfo (`user:password@`);
- a `/bot<token>/` path segment;
- `Bearer` and `Basic` credentials;
- any secret that the caller names explicitly, such as an API key or a bot
  token.

The redacted error still unwraps to the underlying transport error, so
`errors.Is(err, context.DeadlineExceeded)` and `net.Error` checks keep
working. It no longer exposes the `*url.Error` that carries the URL. Feed
URLs in log lines and error messages are also passed through
`netguard.RedactURL`.

### URL and destination validation

Configured URLs must use the `http` or `https` scheme, must name a host and
must not contain userinfo. The library checks the destination address
twice: once on the URL (for an IP literal or `localhost`) and again at dial
time, on every resolved address, through `net.Dialer.Control`. The second
check stops a hostname that resolves, or re-resolves, to a forbidden
address. This covers DNS rebinding.

There are two policies:

- **Block private** applies to URLs that come from a source catalog or that
  point at a public service. It refuses loopback, RFC 1918 and RFC 4193
  private ranges, CGNAT (`100.64.0.0/10`), link-local (including the
  `169.254.169.254` metadata endpoint), unspecified and multicast
  addresses. Under this policy the environment proxy is turned off, because
  a proxy would reach the target for the client and skip the check.
- **Allow private** applies to services that the host operator runs. It
  allows loopback and private networks but still refuses link-local,
  unspecified and multicast addresses.

Both policies also refuse an explicit deny list of cloud metadata
endpoints that sit inside ranges the allow-private policy would otherwise
permit: `fd00:ec2::254` (inside RFC 4193) and `100.100.100.200` (inside
CGNAT). The list is not exhaustive: a provider-specific metadata address
on a private range that is not on it is reachable under allow private.

**Proxies.** Allow-private clients keep the standard `HTTP_PROXY`,
`HTTPS_PROXY` and `NO_PROXY` environment variables. When a proxy is set,
the client dials the proxy, not the target: the dial-time check then sees
only the proxy's address, and the proxy can reach destinations that the
check would refuse (link-local and metadata addresses included). The URL
check still runs on IP literals. This applies to the LLM client, SearXNG,
Tavily, the ESPN scoreboard client, Bluesky, Telegram, the default roster
download client, and RSS, Reddit and Wikidata fetches when their
`allow_private_hosts` opt-in is set. Run the engine without proxy
variables, or with a proxy that enforces its own egress policy, when this
matters.

| Client | Policy | Opt-in |
|---|---|---|
| RSS and Reddit feeds | block private | `http.allow_private_hosts: true` |
| YouTube RSS bridge | allow private | none needed: the bridge is operator infrastructure |
| Wikidata | block private | `entity_discovery.wikidata.allow_private_hosts: true` |
| LLM endpoints, SearXNG, Tavily | allow private | none needed: base URLs are operator infrastructure |
| Telegram | allow private | none needed: a self-hosted Bot API server is supported |
| Bluesky, ESPN scoreboard | allow private | none needed: the hosts are fixed public APIs, and only tests override them |
| Roster download | allow private | none needed: the URL is operator-supplied |

If the host refuses a feed URL, the `rss` adapter does not hand that URL to
the optional scraper fallback. The fetch is also not retried, and the
Wikidata adapter does not report the refusal as a transient error.

Library code that builds its own `rssclient.Config` (for example, a host
adapter that reads a local bridge) must set `AllowPrivateHosts` to reach a
loopback or LAN address.

### User-Agent

Clients that have no configured User-Agent send the neutral
`phins-news-engine/1.0` (`netguard.DefaultUserAgent`). The direct RSS
fetcher keeps a desktop-browser User-Agent, because some publishers reject
library agents. The ESPN scoreboard and Wikidata clients use the
User-Agent from their configuration.

## Files written for the host

Data that the library writes for the host is private to the account that
runs it. This covers the editorial model registry, dictionary snapshots,
sessions and history, absence logs, audit and evaluation reports, the
roster file and new SQLite databases. Files are created `0600`, and the
library creates directories `0700`. Existing directories and existing
database files keep their mode.

## Running commands

`infra/shellrunner` runs an argument vector through `os/exec` and never
through a shell. Before anything runs, it validates the program name. The
name must be a bare program name that is looked up on `PATH`, or a clean
absolute path to an existing file. The runner rejects the following with
`shellrunner.ErrUnsafeCommand`:

- relative paths;
- any `..` element;
- names that start with `-`;
- a shell that is asked to run a command string (`sh -c`).
