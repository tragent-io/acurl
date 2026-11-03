package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/tragent-io/acurl/internal/tap"
)

const defaultTapAddr = "127.0.0.1:8899"

// tapProxyFlags are the settings of the proxy process itself.
type tapProxyFlags struct {
	addr            string
	maxBody         int
	keepCredentials bool
}

func (f *tapProxyFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.addr, "listen", defaultTapAddr, "loopback address the proxy listens on")
	cmd.Flags().IntVar(&f.maxBody, "max-body", tap.DefaultMaxBody, "bytes of each body to keep; the rest is counted, not stored")
	cmd.Flags().BoolVar(&f.keepCredentials, "keep-credentials", false, "do not redact API keys, tokens and cookies")
}

func (f *tapProxyFlags) validate() error {
	if err := loopbackAddr(f.addr); err != nil {
		return err
	}
	if f.maxBody <= 0 {
		return fmt.Errorf("--max-body must be positive, got %d", f.maxBody)
	}
	return nil
}

func newTapCmd(a *app) *cobra.Command {
	var (
		pf        tapProxyFlags
		write     string
		bodies    bool
		secrets   []string
		processes []string
	)
	cmd := &cobra.Command{
		Use:   "tap",
		Short: "Watch the API requests AI coding CLIs make, like tcpdump",
		Long: `tap prints every request that Claude Code, Codex, Gemini CLI or Grok sends to
its model provider, with the response it got back.

Run it in one terminal, and run the CLI in another after loading the wrappers
from ` + "`acurl tap env`" + `. The wrappers point each CLI at a local proxy through its
base URL variable while the proxy is running, and leave it alone when it is
not. Traffic is plain HTTP on loopback and HTTPS to the provider, so no CA
certificate is needed.

The first ` + "`acurl tap`" + ` starts the proxy in the background and keeps it running
after you quit, so CLIs started through it keep working. Stop it with
` + "`acurl tap stop`" + `.

API keys, tokens and cookies are replaced with [redacted] in everything tap
prints or writes. Add other strings to remove with --redact.`,
		Example: `  # terminal 1
  acurl tap

  # terminal 2
  eval "$(acurl tap env)"
  claude

  acurl tap --bodies
  acurl tap -w session.jsonl
  acurl tap stop`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := pf.validate(); err != nil {
				return err
			}
			filter, err := parseTapProcessFilter(processes)
			if err != nil {
				return err
			}
			info, err := a.ensureTapProxy(cmd, pf)
			if err != nil {
				return err
			}
			sink, closeSink, err := a.tapSink(write, bodies, filter)
			if err != nil {
				return err
			}
			defer closeSink()
			a.logf("watching acurl tap on http://%s (proxy pid %d); Ctrl+C stops watching, `acurl tap stop` stops the proxy\n", pf.addr, info.PID)
			a.logf("in another terminal: eval \"$(acurl tap env)\", then run claude, codex, gemini or grok\n")
			return a.watchTap(cmd.Context(), pf.addr, secrets, sink)
		},
	}
	pf.register(cmd)
	cmd.Flags().StringVarP(&write, "write", "w", "", "append every exchange as JSON lines to this file (- for stdout)")
	cmd.Flags().BoolVar(&bodies, "bodies", false, "print request and response bodies under each line")
	cmd.Flags().StringArrayVar(&secrets, "redact", nil, "extra string to replace with [redacted] (repeatable)")
	cmd.Flags().StringArrayVar(&processes, "process", nil, "show an exact process name or name:pid (case-insensitive; repeatable)")
	cmd.AddCommand(newTapEnvCmd(a), newTapServeCmd(a), newTapStopCmd(a), newTapStatusCmd(a))
	return cmd
}

func newTapServeCmd(a *app) *cobra.Command {
	var pf tapProxyFlags
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the acurl tap proxy in the foreground",
		Long: `serve runs the proxy that ` + "`acurl tap`" + ` normally starts in the background. Use it
to run the proxy under a service manager, or to watch its log.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := pf.validate(); err != nil {
				return err
			}
			ctx, stop := context.WithCancel(cmd.Context())
			defer stop()
			p, err := tap.New(tap.Options{MaxBody: pf.maxBody, KeepCredentials: pf.keepCredentials, Version: a.info.Version, OnStop: stop})
			if err != nil {
				return err
			}
			ln, err := net.Listen("tcp", pf.addr)
			if err != nil {
				return err
			}
			a.logf("acurl tap proxy on http://%s (pid %d)\n", ln.Addr(), os.Getpid())
			err = serveUntilDone(ctx, ln, p, p.Close)
			a.logf("acurl tap proxy stopped\n")
			return err
		},
	}
	pf.register(cmd)
	return cmd
}

func newTapStopCmd(a *app) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the acurl tap proxy",
		Long: `stop shuts the background proxy down. CLIs started through it lose their
connection to the provider, so quit them first.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := loopbackAddr(addr); err != nil {
				return err
			}
			info, err := pingTap(cmd.Context(), addr)
			if errors.Is(err, errTapNotRunning) {
				a.logf("acurl tap is not running on %s\n", addr)
				return nil
			} else if err != nil {
				return err
			}
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, "http://"+addr+tap.StopPath, nil)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				return fmt.Errorf("stop: proxy answered %s", resp.Status)
			}
			a.logf("stopped acurl tap on %s (pid %d)\n", addr, info.PID)
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "listen", defaultTapAddr, "address the proxy listens on")
	return cmd
}

func newTapStatusCmd(a *app) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether the acurl tap proxy is running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := loopbackAddr(addr); err != nil {
				return err
			}
			info, err := pingTap(cmd.Context(), addr)
			if errors.Is(err, errTapNotRunning) {
				fmt.Fprintf(a.stdout, "acurl tap is not running on %s\n", addr)
				return exitError(1)
			} else if err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "acurl tap %s on http://%s: pid %d, %d viewer(s), max body %d bytes, credentials %s\n",
				info.Version, addr, info.PID, info.Viewers, info.MaxBody, map[bool]string{true: "kept", false: "redacted"}[info.KeepCredentials])
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "listen", defaultTapAddr, "address the proxy listens on")
	return cmd
}

func newTapEnvCmd(a *app) *cobra.Command {
	var (
		addr  string
		shell string
		list  bool
	)
	cmd := &cobra.Command{
		Use:   "env [cli...]",
		Short: "Print shell functions that route AI CLIs through acurl tap",
		Long: `env prints shell functions named after each CLI (` + strings.Join(tap.CLINames(), ", ") + `).
Each one checks whether the acurl tap proxy is running. If it is, it runs the
CLI with its base URL variable pointing at the proxy; if not, it runs the CLI
unchanged.

Load them in the current shell with eval, or add that line to your shell's rc
file. The shell is taken from $SHELL unless --shell is set.`,
		Example: `  eval "$(acurl tap env)"
  eval "$(acurl tap env claude)"
  acurl tap env --shell fish | source
  acurl tap env --shell powershell | Out-String | Invoke-Expression
  acurl tap env --list`,
		RunE: func(_ *cobra.Command, args []string) error {
			if list {
				for _, c := range tap.CLIs {
					var envs []string
					for _, b := range c.Bindings {
						envs = append(envs, b.Env)
					}
					fmt.Fprintf(a.stdout, "%-8s %-50s %s\n", c.Name, strings.Join(envs, ", "), c.Note)
				}
				return nil
			}
			if err := loopbackAddr(addr); err != nil {
				return err
			}
			clis := tap.CLIs
			if len(args) > 0 {
				clis = nil
				for _, name := range args {
					c, ok := tap.FindCLI(name)
					if !ok {
						return fmt.Errorf("unknown CLI %q; known: %s", name, strings.Join(tap.CLINames(), ", "))
					}
					clis = append(clis, c)
				}
			}
			if shell == "" {
				shell = a.defaultShell()
			}
			return tap.WriteEnv(a.stdout, shell, "http://"+addr, clis)
		},
	}
	cmd.Flags().StringVar(&addr, "listen", defaultTapAddr, "address the proxy listens on")
	cmd.Flags().StringVar(&shell, "shell", "", "shell to write for: "+strings.Join(tap.Shells, ", ")+" (default from $SHELL)")
	cmd.Flags().BoolVar(&list, "list", false, "list the supported CLIs and the variables they use")
	return cmd
}

// ensureTapProxy returns the running proxy on pf.addr, starting one in the
// background when none is running.
func (a *app) ensureTapProxy(cmd *cobra.Command, pf tapProxyFlags) (tap.Info, error) {
	ctx := cmd.Context()
	if info, err := pingTap(ctx, pf.addr); err == nil {
		if info.Version != a.info.Version {
			a.logf("acurl tap: the running proxy is version %s and this is %s; run `acurl tap stop` to restart it\n", info.Version, a.info.Version)
		}
		changed := cmd.Flags().Changed
		if changed("max-body") && info.MaxBody != pf.maxBody || changed("keep-credentials") && info.KeepCredentials != pf.keepCredentials {
			a.logf("acurl tap: the proxy is already running with other settings; run `acurl tap stop` first to apply --max-body or --keep-credentials\n")
		}
		return info, nil
	} else if !errors.Is(err, errTapNotRunning) {
		return tap.Info{}, err
	}

	log, err := tapLogPath()
	if err != nil {
		return tap.Info{}, err
	}
	args := []string{"tap", "serve", "--listen", pf.addr, "--max-body", strconv.Itoa(pf.maxBody)}
	if pf.keepCredentials {
		args = append(args, "--keep-credentials")
	}
	if err := a.startTap(args, log); err != nil {
		return tap.Info{}, fmt.Errorf("start the acurl tap proxy: %w", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := pingTap(ctx, pf.addr)
		if err == nil {
			a.logf("started the acurl tap proxy in the background (pid %d, log %s)\n", info.PID, log)
			return info, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return tap.Info{}, fmt.Errorf("the acurl tap proxy did not start; see %s", log)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// startTap runs acurl with args as a detached background process, writing
// its output to log.
func (a *app) startTap(args []string, log string) error {
	if a.startTapProcess != nil {
		return a.startTapProcess(args, log)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	c := exec.Command(self, args...)
	c.Stdout, c.Stderr = f, f
	c.SysProcAttr = detachedProcess()
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

func tapLogPath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "acurl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "tap.log"), nil
}

var errTapNotRunning = errors.New("acurl tap is not running")

// pingTap asks the proxy on addr who it is.
func pingTap(ctx context.Context, addr string) (tap.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+tap.PingPath, nil)
	if err != nil {
		return tap.Info{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return tap.Info{}, errTapNotRunning
		}
		return tap.Info{}, err
	}
	defer resp.Body.Close()
	var info tap.Info
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&info) != nil || info.Name != "acurl tap" {
		return tap.Info{}, fmt.Errorf("something other than acurl tap is listening on %s; pick another --listen address", addr)
	}
	return info, nil
}

// watchTap prints exchanges from the proxy's event stream until ctx ends or
// the proxy stops.
func (a *app) watchTap(ctx context.Context, addr string, secrets []string, sink func(*tap.Exchange)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+tap.EventsPath, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("watch: proxy answered %s", resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 64<<20)
	for sc.Scan() {
		var ev tap.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return fmt.Errorf("watch: %w", err)
		}
		if ev.Dropped > 0 {
			a.logf("acurl tap: fell behind and missed %d exchange(s)\n", ev.Dropped)
		}
		if ev.Exchange != nil {
			tap.Redact(ev.Exchange, secrets)
			sink(ev.Exchange)
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	a.logf("acurl tap: the proxy stopped\n")
	return nil
}

// defaultShell guesses the user's shell from $SHELL, falling back to
// PowerShell on Windows and bash elsewhere.
func (a *app) defaultShell() string {
	if s := filepath.Base(a.getenv("SHELL")); s == "zsh" || s == "fish" || s == "bash" {
		return s
	}
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	return "bash"
}

// tapSink returns the callback that prints or saves each exchange.
func (a *app) tapSink(write string, bodies bool, filter tapProcessFilter) (func(*tap.Exchange), func(), error) {
	var (
		mu    sync.Mutex
		lines = a.stdout
		jsonl *json.Encoder
		file  *os.File
	)
	switch write {
	case "":
	case "-":
		jsonl, lines = json.NewEncoder(a.stdout), nil
	default:
		f, err := os.OpenFile(write, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, nil, err
		}
		file, jsonl = f, json.NewEncoder(f)
	}
	sink := func(x *tap.Exchange) {
		mu.Lock()
		defer mu.Unlock()
		matches := filter.matches(x)
		if jsonl != nil && (file != nil || matches) {
			if err := jsonl.Encode(x); err != nil {
				fmt.Fprintln(a.stderr, "acurl tap: write:", err)
			}
		}
		if lines != nil && matches {
			_ = tap.WriteLine(lines, x)
			if bodies {
				_ = tap.WriteBodies(lines, x)
			}
		}
	}
	closeSink := func() {
		if file != nil {
			_ = file.Close()
		}
	}
	return sink, closeSink, nil
}

// loopbackAddr rejects listen addresses other machines could reach: the
// proxy forwards requests with the user's credentials attached.
func loopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --listen %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("--listen %q must be a loopback address such as %s", addr, defaultTapAddr)
	}
	return nil
}

// serveUntilDone serves h on ln until ctx is cancelled. beforeShutdown, if
// set, runs first to end connections that would otherwise never finish.
func serveUntilDone(ctx context.Context, ln net.Listener, h http.Handler, beforeShutdown func()) error {
	hs := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	if p, ok := h.(*tap.Proxy); ok {
		hs.ConnContext = p.ConnContext
	}
	go func() {
		<-ctx.Done()
		if beforeShutdown != nil {
			beforeShutdown()
		}
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
