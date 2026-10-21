// Package cli implements the acurl command line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/tragent-io/acurl/internal/config"
	"github.com/tragent-io/acurl/internal/output"
	"github.com/tragent-io/acurl/pkg/acurl"
)

// BuildInfo describes the running binary.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// app holds what every command needs.
type app struct {
	info   BuildInfo
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string

	quiet   bool
	verbose bool

	// startTapProcess replaces starting a background `acurl tap serve` in tests.
	startTapProcess func(args []string, log string) error
}

// logf prints progress to stderr unless --quiet is set.
func (a *app) logf(format string, args ...any) {
	if !a.quiet {
		fmt.Fprintf(a.stderr, format, args...)
	}
}

// trace returns a per-request logger for --verbose, or nil.
func (a *app) trace() func(acurl.TraceEvent) {
	if !a.verbose {
		return nil
	}
	return func(e acurl.TraceEvent) {
		status := fmt.Sprint(e.Status)
		if e.Err != nil {
			status = "ERR " + e.Err.Error()
		}
		fmt.Fprintf(a.stderr, "  > %s %s [%s] %s %dms\n", e.Method, e.URL, e.Profile, status, e.Duration.Milliseconds())
	}
}

// userAgent identifies acurl itself when fetching auxiliary files.
func (a *app) userAgent() string {
	return "acurl/" + a.info.Version + " (+https://github.com/tragent-io/acurl)"
}

// Execute runs acurl with args and returns the process exit code.
func Execute(ctx context.Context, info BuildInfo, args []string, stdout, stderr io.Writer) int {
	a := &app{info: info, stdout: stdout, stderr: stderr, getenv: os.Getenv}
	output.Version = info.Version
	root := newRootCmd(a)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	err := root.ExecuteContext(ctx)
	var code exitError
	switch {
	case err == nil:
		return output.ExitOK
	case errors.As(err, &code):
		return int(code)
	default:
		fmt.Fprintln(stderr, "acurl:", err)
		return output.ExitError
	}
}

func newRootCmd(a *app) *cobra.Command {
	cmd := newCheckCmd(a)
	cmd.Version = fmt.Sprintf("%s (commit %s, built %s)", a.info.Version, a.info.Commit, a.info.Date)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.AddCommand(newProfilesCmd(a), newRobotsCmd(a), newBatchCmd(a), newInitCmd(a), newKeygenCmd(a), newReportCmd(a), newWatchCmd(a), newServeCmd(a), newTapCmd(a), newMCPCmd(a), newManCmd(a, cmd), newReferenceCmd(a, cmd), newVersionCmd(a, cmd))

	var configPath string
	cmd.PersistentFlags().StringVar(&configPath, "config", "", "config file (default ./"+config.FileName+" or the user config dir)")
	cmd.PersistentFlags().BoolVarP(&a.quiet, "quiet", "q", false, "only print the report")
	cmd.PersistentFlags().BoolVarP(&a.verbose, "verbose", "v", false, "log every request to stderr")
	cmd.PersistentPreRunE = func(c *cobra.Command, _ []string) error {
		if c.Name() == "init" || c.Name() == "man" || c.Name() == "keygen" || c.Name() == "reference" || c.Name() == "version" || c.HasParent() && c.Parent().Name() == "completion" {
			return nil
		}
		return applyConfig(cmd, c, configPath, a.getenv)
	}
	return cmd
}

// applyConfig loads the config file and applies it, plus ACURL_* variables,
// to the flags of the command being run.
func applyConfig(root, c *cobra.Command, path string, getenv func(string) string) error {
	file, err := config.Find(path)
	if err != nil {
		return err
	}
	cfg, err := config.Load(file)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		cmd.Flags().VisitAll(func(f *pflag.Flag) { known[f.Name] = true })
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
	if unknown := config.Unknown(cfg, known); len(unknown) > 0 {
		return fmt.Errorf("config %s: unknown settings %v", file, unknown)
	}
	return config.Apply(c.Flags(), cfg, getenv)
}

// exitError carries a non-zero exit code without an error message.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
