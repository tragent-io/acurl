# Watching AI CLI traffic

`acurl tap` shows what an AI coding CLI sends to its model provider and what comes back,
the way tcpdump shows packets. Run it in one terminal and keep using Claude Code, Codex,
Gemini CLI or Grok in another.

```bash
# terminal 1
acurl tap
```

```bash
# terminal 2
eval "$(acurl tap env)"
claude
```

```
20:44:42 #1 claude:3142 anthropic HEAD /api/hello 200 449ms 0B > 0B
20:44:43 #2 claude:3142 anthropic POST /v1/messages?beta=true claude-opus-5-5 200 2.328s 70.1KB > 1.5KB stream
```

Each line summarizes a completed exchange: time it started, number, process name and PID
(when available), provider, method and path, model, status, duration, request and response size.
`stream` marks server-sent events,
`canceled` an exchange the CLI hung up on, and `websocket` a WebSocket the CLI opened. Long
streams finish after shorter requests that started later, so numbers can arrive out of order.

The process label identifies the local socket owner, so a CLI implemented in Node.js may
appear as `node`, rather than its CLI command. Lookup depends on OS permissions and available
process information; an unavailable lookup leaves the label out. Metadata lookup is
asynchronous and can briefly delay publication after an exchange ends. acurl records only the
process name and PID, never command lines or executable paths.

Results are cached for each connection, including unsuccessful lookups. Captures wait at
most 500 ms for metadata; forwarding never waits. If discovery is too busy or too many
captures are waiting, exchanges are published without a process label.

Linux lookup requires visibility into the socket owner's `/proc` entries. macOS uses
`/usr/sbin/lsof`, which must be available. Windows uses native TCP table and process APIs.
Restricted permissions or a process that exits before lookup finishes can leave metadata
unavailable on any platform.

## Filter by process

```bash
acurl tap --process claude                 # all PIDs with this exact name
acurl tap --process node:3142              # one process
acurl tap --process claude --process codex # either name
```

Names match exactly, ignoring case; a PID must be a positive decimal integer. Filters apply
only to this viewer, so other viewers can watch different processes. Without a filter,
exchanges with unavailable metadata remain visible. With a filter, they are excluded.
Filters affect summary lines, `--bodies`, and JSON on stdout (`-w -`). A session file
(`-w session.jsonl`) still captures every exchange, including those hidden from the viewer.

## How it works

Provider traffic is HTTPS, so a passive sniffer would only see encrypted bytes. Instead,
each CLI is pointed at acurl through the base URL variable it already supports. The CLI talks
plain HTTP to `127.0.0.1:8899`, and acurl forwards the request to the real provider over HTTPS.
No CA certificate is installed and nothing about the CLI's login changes.

`acurl tap env` prints a shell function for each CLI. While the proxy is running, the
function sets the variable and runs the CLI; when it is not, it runs the CLI unchanged. It is
safe to put the `eval` line in your `~/.zshrc` or `~/.bashrc`.

## The proxy and the viewer

The first `acurl tap` starts the proxy as a background process and then shows its traffic.
Quitting `acurl tap` only stops watching: the proxy keeps running, so a CLI you started
through it keeps working. Run `acurl tap` again to watch again; several can watch at once.

```bash
acurl tap status    # is the proxy running, and with which settings
acurl tap stop      # stop it; quit tapped CLIs first, they lose their connection
acurl tap serve     # run the proxy in the foreground, e.g. under launchd or systemd
```

The proxy logs to `acurl/tap.log` in your user cache directory. `--max-body` and
`--keep-credentials` take effect when the proxy starts; stop it to change them. After you
upgrade acurl, `acurl tap` warns you if the proxy is still the old version.

```bash
eval "$(acurl tap env)"                              # bash and zsh
acurl tap env --shell fish | source                  # fish
acurl tap env --shell powershell | Out-String | Invoke-Expression
eval "$(acurl tap env claude grok)"                  # only some CLIs
```

| CLI | Variables | Notes |
|---|---|---|
| `claude` | `ANTHROPIC_BASE_URL` | API key and claude.ai login both work |
| `codex` | `OPENAI_BASE_URL` | API key login only; ChatGPT login talks to chatgpt.com and bypasses the base URL |
| `gemini` | `GOOGLE_GEMINI_BASE_URL`, `CODE_ASSIST_ENDPOINT` | Gemini API key and Google login; Vertex AI is not routed |
| `grok` | `GROK_CLI_CHAT_PROXY_BASE_URL` | Grok Build CLI with grok.com login |

`acurl tap env --list` prints the same table. The wrappers send each CLI to the provider's
public endpoint, so they replace any base URL you set yourself for a gateway.

Requests only go where a CLI's own variable sends them. Traffic the CLI makes to other hosts,
such as MCP servers or web fetches, does not pass through acurl.

## Bodies and sessions

```bash
acurl tap --bodies              # print request and response bodies under each line
acurl tap -w session.jsonl      # append every exchange as a JSON line
acurl tap -w - | jq .model      # JSON lines on stdout
```

Each JSON line holds the URL, model, status, timing, headers and both bodies, plus `process`
and `pid` when known. Older captures simply lack these optional fields. Bodies sent
with `Content-Encoding` (Grok compresses requests with zstd) are decoded. acurl keeps the first
1 MB of each body (`--max-body`) and marks it `truncated`; the CLI always gets the whole body.

## Secrets

`authorization`, `x-api-key`, `x-goog-api-key`, cookies and `key=` query parameters are
replaced with `[redacted]` in everything acurl prints or writes. Each removal is listed under
`redactions` with where it was and why. Add your own strings with `--redact`, for example a
customer name that appears in prompts:

```bash
acurl tap --redact "Acme Corp" -w session.jsonl
```

`--redact` also removes matching strings from process names. Process filters match the name
after this redaction.

Prompts, code and model output are not redacted, so treat session files like your shell
history. They are created readable only by you.

`acurl tap` only listens on loopback and refuses requests that carry an `Origin` header or
name another host, so web pages cannot use it to reach your provider accounts.

## Limits

- Claude Code turns off MCP tool search when `ANTHROPIC_BASE_URL` is not Anthropic's, so
  sessions that load many MCP tools send every tool schema with each request while tapped.
- The wrapper decides when the CLI starts. A CLI started before the proxy talks to the
  provider directly until you restart it.
- WebSocket connections pass through, but only the handshake is recorded, not the frames.
- A viewer that cannot keep up (more than 1,024 exchanges behind) skips exchanges and
  says how many it missed. The proxy never slows a CLI down for a viewer.

## Benchmark process attribution

From a development checkout, run:

```bash
go test ./internal/tap -run '^$' -bench 'Process|Attribution' -benchmem -count=5
```

The benchmarks separate native lookup and cached access from end-to-end proxy requests.
Proxy cases compare disabled, native and deliberately slow discovery on new connections,
keep-alive connections and concurrent clients. They report client latency (`p95-ns/op`),
publication delay (`event-delay-ns/op`) and the percentage of attributed exchanges, so a
fast response is not mistaken for a successful lookup. Run them on each supported OS.
