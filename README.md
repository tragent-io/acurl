<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <img src="assets/logo-light.svg" alt="acurl" width="72" height="72">
  </picture>
</p>

<h1 align="center">acurl</h1>

<p align="center">curl for AI agents.<br>
<a href="https://acurl.dev">acurl.dev</a> · <a href="https://acurl.dev/docs/">Documentation</a> · <a href="https://acurl.dev/docs/#reference">Command reference</a> · <a href="https://tragent.io">tragent.io</a></p>

<p align="center">
  <a href="https://github.com/tragent-io/acurl/actions/workflows/ci.yml"><img src="https://github.com/tragent-io/acurl/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/tragent-io/acurl/releases"><img src="https://img.shields.io/github/v/release/tragent-io/acurl?color=6a2bc4" alt="Release"></a>
  <a href="https://pkg.go.dev/github.com/tragent-io/acurl/pkg/acurl"><img src="https://pkg.go.dev/badge/github.com/tragent-io/acurl/pkg/acurl.svg" alt="Go Reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-1f1534" alt="License"></a>
</p>

**acurl** sends one HTTP request as a real Chrome browser and as 21 AI agent and crawler
profiles (ChatGPT-User, Claude-User, GPTBot, PerplexityBot, Googlebot and others), then compares
the responses: status, WAF challenge, robots.txt rule, visible-text similarity and product data.
Output is a table, JSON, SARIF, JUnit, Markdown, CSV or HTML, and the exit code follows a
`--fail-on` policy, so it drops into scripts and CI.

Apache-2.0, maintained by [tragent.io](https://tragent.io).

```console
$ acurl shop.example/products/wool-runner
Fetching https://shop.example/products/wool-runner with 23 profiles...

PROFILE            STATUS  TIME    RESULT             DETAILS
Chrome (Browser)   200     412ms   allowed            Wool Runner | Example Store
ChatGPT-User       403     51ms    challenged         challenge: Cloudflare
Claude-User        200     388ms   allowed            OK
Perplexity-User    200     402ms   allowed            product: price 89.00 vs browser 98.00
OAI-SearchBot      200     97ms    cloaked            31% similar to browser
GPTBot             200     365ms   robots-disallowed  robots.txt: disallowed
Google-Extended    -       -       robots-disallowed  robots.txt token (not fetched)

AI readiness 71/100 (C)
  ✗ AI assistants can fetch the page            16.7/25  ChatGPT-User (challenged)
  ✗ AI search crawlers can fetch the page       10.0/20  OAI-SearchBot (cloaked)
  ✓ Content is in the HTML without JavaScript   15.0/15
  ✓ Product structured data is present          10.0/10

How to improve
  1. AI assistants can fetch the page
     Allow ChatGPT-User, Claude-User and Perplexity-User in your WAF or bot-management rules...
```

## Install

| | |
|---|---|
| Homebrew | `brew install tragent-io/tap/acurl` |
| Shell | `curl -fsSL https://acurl.dev/install.sh \| sh` |
| Go | `go install github.com/tragent-io/acurl/cmd/acurl@latest` |
| npm | `npx @tragent/acurl <url>` |
| Docker | `docker run --rm ghcr.io/tragent-io/acurl <url>` |
| Binaries | [GitHub Releases](https://github.com/tragent-io/acurl/releases), with checksums, SBOMs and cosign signatures |

## What it checks

- **21 AI agents and crawlers** in four categories, plus the browser baseline and curl: AI assistants (ChatGPT-User, Claude-User, Perplexity-User, ...),
  AI search (OAI-SearchBot, Claude-SearchBot, PerplexityBot, ...), AI training (GPTBot, ClaudeBot, CCBot,
  Google-Extended, ...), search engines, and a curl baseline
- **A real browser baseline** with Chrome's TLS and HTTP/2 fingerprint. Bot profiles send only their own
  headers, like real crawlers.
- **WAF challenges** from Cloudflare, Akamai, DataDome, PerimeterX, Imperva, Kasada, AWS WAF, Sucuri and CAPTCHAs
- **Cloaking**: agents shown substantially different visible text than the browser, with `--diff` to see what's missing
- **robots.txt** (RFC 9309), **Content Signals**, `X-Robots-Tag`, meta robots and **llms.txt**
- **Product data parity**: schema.org Product from JSON-LD, microdata or OpenGraph, and agents shown a different price or stock
- **JavaScript dependence**: client-rendered shells, and `--render` to measure JavaScript-only text in headless Chrome
- **An AI readiness score** (0-100) from [documented checks](docs/scoring.md), with a fix for each failure

## Commands

| Command | |
|---|---|
| `acurl <url>` | Check a page with every agent and score it |
| `acurl robots <url>` | robots.txt verdict per agent, Content Signals, sitemaps and llms.txt |
| `acurl batch [file]` | URL lists, CSV, stdin or whole sitemaps (`--sitemap auto:shop.example`) |
| `acurl watch <url>...` | Re-check on a schedule and alert Slack or a webhook on changes |
| `acurl report <file>` | Render saved JSON as HTML, Markdown, SARIF, JUnit or CSV |
| `acurl serve` | Local web UI and JSON API |
| `acurl mcp` | MCP server so Claude, Cursor and other assistants can run checks |
| `acurl tap` | [Watch the API traffic](docs/tap.md) of Claude Code, Codex, Gemini CLI or Grok, like tcpdump |
| `acurl keygen` | Ed25519 key for [Web Bot Auth](docs/signing.md) request signing |
| `acurl profiles list` | The agent catalog |
| `acurl init` | Create a `.acurl.yaml` config file |

Every flag is in the [command reference](https://acurl.dev/docs/#reference).

## In CI

```yaml
- uses: tragent-io/acurl@v1
  with:
    urls: |
      https://staging.shop.example/
      https://staging.shop.example/products/best-seller
    fail-on: blocked,challenged,score<80
```

Output formats: `table`, `json`, `markdown`, `html`, `sarif`, `junit` and `csv`. Exit codes follow
`--fail-on`. See the [CI guide](docs/ci.md).

## With your AI assistant

```bash
claude mcp add acurl -- acurl mcp
```

Then ask *"Can ChatGPT read our new product page?"* See [MCP setup](docs/mcp.md) for Claude Desktop and Cursor.

## Watch your AI CLI's traffic

```bash
acurl tap                    # terminal 1
eval "$(acurl tap env)"      # terminal 2, then run claude, codex, gemini or grok
```

Every request the CLI sends to its model provider, and the response, shows up as a line, like
tcpdump. No CA certificate, and API keys are redacted. See [Watching AI CLI traffic](docs/tap.md).

## As a Go library

```go
c, _ := acurl.New(acurl.Options{BlockPrivate: true})
rep, _ := c.Check(ctx, "https://shop.example/products/1")
fmt.Println(rep.Score.Value)
```

## Caveat

acurl sends agent user-agent strings from your own IP address. Real AI agents come from published IP
ranges and may sign their requests, so results show how a site responds to agent user agents, not
guaranteed agent access. It's the fastest first check, and it catches most real-world blocking.

## Contributing

Bug reports, new agent profiles, detection signatures and new CLIs for `acurl tap` are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).
Security issues: [SECURITY.md](SECURITY.md).

## License

Apache 2.0. Copyright tragent. acurl is built and maintained by [tragent.io](https://tragent.io), which
helps online stores get found, understood and bought from by AI shopping agents.
