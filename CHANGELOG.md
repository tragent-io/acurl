# Changelog

All notable changes to acurl are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- `acurl tap` shows the requests Claude Code, Codex, Gemini CLI and Grok send to their model
  providers and the responses they get, one line per exchange, with `--bodies` and JSON lines
  output (`-w`). API keys, tokens and cookies are redacted. See [docs/tap.md](docs/tap.md)
- `acurl tap env` prints shell wrappers that route each CLI through `acurl tap` while it is
  running and leave it alone when it is not
- The `acurl tap` proxy runs in the background, so quitting the viewer never breaks a CLI;
  `acurl tap status`, `acurl tap stop` and `acurl tap serve` manage it
- acurl.dev documents `acurl tap`, and CONTRIBUTING.md explains how to add a CLI to it

### Fixed
- The `indexable` score ignores `noindex` directives from challenged and blocked
  agent responses, where the directive belongs to an intermediary error page

## [1.1.1] - 2026-10-19

### Fixed
- `batch --delay` measures the gap from when the previous check of a host started, so
  checks are never closer together than the delay; on Windows they could be about half
- acurl.dev no longer shows the phone menu under the header on desktop
- acurl.dev versions its stylesheet and script links, so a deploy never pairs new pages
  with cached styles

## [1.1.0] - 2026-10-19

### Fixed
- Vendor scripts that Cloudflare, AWS WAF, Imperva and Kasada inject into normal pages no
  longer mark a 200 response as challenged
- Akamai Bot Manager's interstitial challenge, served with a 200, is now detected
- The Chrome baseline now matches Chrome 156, with its compression and priority headers;
  responses in br, zstd and gzip are decoded
- A redirect loop is counted once and reports the last URL it reached
- robots.txt verdicts are skipped for requests that failed
- Blocked rows say who blocked them, such as "HTTP 403 from CloudFront"
- Unscored pages say why: DNS failure, private address, HTTP status, challenge vendor,
  rate limit or a non-HTML response
- Product pages without Product structured data, and pages marked `noindex`, now lose points
- Invalid flag values (categories, concurrency, retries, timeout, max body, cloak threshold,
  batch parallelism and limit) are rejected
- `batch` skips invalid lines instead of aborting the run
- `go install` builds report their module version
- `acurl serve` refuses foreign Host and Origin headers and non-JSON posts
- Web Bot Auth signing follows the current draft's `Signature-Agent` dictionary format
- `install.sh --help` works when the script is piped to `sh`
- The blocked private address error explains the resolved IP and `--allow-private`

### Added
- `--samples N` repeats each request and flags agents whose verdict changes between runs
- `--page product|other|auto` overrides product page detection for scoring
- An `indexable` score check
- `serve --allowed-host` for running the web UI behind a hostname
- A monthly check that opens an issue when the Chrome baseline falls behind stable Chrome
- acurl.dev: robots.txt, sitemap, llms.txt, a social preview image, structured data, a 404
  page, self-hosted fonts, and phone and tablet layouts

### Removed
- The PyPI wrapper. Install with Homebrew, `install.sh`, npm, Go, Docker or the release archives.

## [1.0.0] - 2026-10-19

The first stable release. The CLI flags, JSON report format, exit codes and the
`pkg/acurl` Go API now follow semantic versioning.

### Added
- [acurl.dev](https://acurl.dev): website and documentation with a command reference
  generated from the CLI
- Install with Homebrew, `install.sh`, npm (`@tragent/acurl`), pip (`acurl`), Docker
  (`ghcr.io/tragent-io/acurl`), deb/rpm/apk packages or release archives
- Release archives include man pages and shell completions; checksums are signed with
  cosign and every archive has an SBOM
- GitHub Action (`uses: tragent-io/acurl@v1`) with a job summary, SARIF upload and `--fail-on`
- Examples for GitHub Actions, GitLab CI, config files, custom profiles, cron and the Go API
- CodeQL scanning


## [0.4.0] - 2026-10-11

### Added
- `acurl serve`: local web UI with live progress, example result and downloads, plus a JSON
  API (`/api/check`, `/api/check/stream`, `/api/robots`, `/api/profiles`, `/api/report`)
- `acurl mcp`: Model Context Protocol server with `check_url`, `readiness_score`,
  `robots_audit` and `list_profiles` tools for Claude, Cursor and other assistants
- Web Bot Auth request signing (RFC 9421, Ed25519) with `acurl keygen`, `--sign` and
  `--signature-agent`, verified against the RFC test vector
- Self-contained HTML reports (`-f html`) and `acurl report` to re-render saved JSON
- `acurl watch` with change detection, JSON and Slack webhooks, and a `--state` file for cron
- New logo and the tragent.io design language across the web UI and reports

### Changed
- Non-HTML responses (PDF, images, JSON) are no longer parsed as HTML

### Fixed
- robots.txt-only tokens are never classified as cloaked
- SSRF guard blocks private, loopback, link-local and metadata addresses for `serve` and `mcp`


## [0.3.0] - 2026-09-27

### Added
- AI readiness score (0-100, A-F) with weighted checks and a recommendation for each failure
- Product data extraction from JSON-LD (including `@graph`, ProductGroup variants and
  AggregateOffer), microdata and OpenGraph, plus per-agent price and availability comparison
- Client-rendered (SPA) page detection, and `--render` to measure JavaScript-only text in headless Chrome
- `acurl batch` for URL lists, CSV files, stdin and sitemaps (`--sitemap`, `--sitemap auto:<site>`),
  with `--parallel`, `--limit` and a per-host `--delay`
- `--fail-on` policy: blocked, challenged, rate-limited, cloaked, robots, error, `score<N`, none
- CSV, JUnit XML and SARIF output formats
- `.acurl.yaml` config files, `ACURL_*` environment variables and `acurl init`
- `--quiet`, `--verbose` request tracing, man pages and shell completions
- Public Go API in `pkg/acurl`

### Fixed
- Small static pages that load one script are no longer flagged as client-rendered
- Pages are not scored when the browser baseline itself was blocked


## [0.2.0] - 2026-09-13

### Added
- `acurl robots <url>`: per-agent robots.txt audit with Content-Signal directives,
  sitemaps and llms.txt / llms-full.txt validation
- `acurl profiles list|show` with `--category` filtering
- 13 new profiles: GPTBot, ClaudeBot, Claude-User, Claude-SearchBot, Perplexity-User,
  MistralAI-User, DuckAssistBot, Meta-ExternalAgent, Amazonbot, Bytespider, CCBot, Applebot,
  plus robots.txt-only tokens Google-Extended and Applebot-Extended
- Profile metadata: `category`, `operator`, `docs`, `browser`, `robots_only`
- `--category` to run one class of agents
- Cloaking detection that compares visible text with the browser baseline, `--cloak-threshold`
- `--diff <profile>` prints a visible-text diff against the browser
- X-Robots-Tag and meta robots directives per agent, including agent-specific tags
- Redirect chains and final URL in results
- `--retries` with exponential backoff for network and gateway errors
- `--proxy`, `--insecure`, `--http 1.1|2`, `-X`, `-I/--head`, `--max-body`, `--tls go|chrome`

### Fixed
- Bot profiles no longer send Chrome client hints and a zh-CN Accept-Language
- `duration_ms` is now milliseconds, not nanoseconds
- Akamai block pages with HTML-encoded reference IDs are detected
- Huge Retry-After values no longer overflow (found by fuzzing)
- URLs without a scheme default to https


## [0.1.0] - 2026-08-30

First public preview.

### Added
- `acurl <url>` fetches a URL once per agent profile with a Chrome TLS fingerprint
- Default profiles: Chrome, ChatGPT-User, OAI-SearchBot, Claude-Web, anthropic-ai,
  PerplexityBot, Googlebot, Bingbot and curl
- Challenge detection for Cloudflare, Akamai, DataDome, PerimeterX, Imperva, Kasada,
  AWS WAF, Sucuri and generic CAPTCHAs
- RFC 9309 robots.txt checks per agent, with `--no-robots` to skip them
- Classification of each result: allowed, blocked, challenged, rate-limited,
  robots-disallowed or error
- Table, JSON and Markdown output (`--format`, `--output`)
- CI-friendly exit codes: 0 allowed, 1 blocked or challenged, 2 errors
- Custom profiles via `--profiles file.yaml`, extra headers via `-H`

[Unreleased]: https://github.com/tragent-io/acurl/compare/v1.1.1...HEAD
[1.1.1]: https://github.com/tragent-io/acurl/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/tragent-io/acurl/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/tragent-io/acurl/compare/v0.4.0...v1.0.0
[0.4.0]: https://github.com/tragent-io/acurl/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/tragent-io/acurl/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/tragent-io/acurl/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/tragent-io/acurl/releases/tag/v0.1.0
