# @tragent/acurl

curl for AI agents: request a URL as Chrome and 21 AI agent and crawler
profiles and compare the responses. This package installs the
[acurl](https://acurl.dev) CLI, a free, open-source tool by [tragent.io](https://tragent.io).

```bash
npx @tragent/acurl https://example.com
npm install -g @tragent/acurl && acurl https://example.com
```

The package downloads the prebuilt binary for your platform from
[GitHub Releases](https://github.com/tragent-io/acurl/releases) and checks it against the
release's SHA-256 checksums. Documentation: https://acurl.dev/docs/
