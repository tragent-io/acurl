package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newVersionCmd prints the same line as --version.
func newVersionCmd(a *app, root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the acurl version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := fmt.Fprintf(a.stdout, "%s version %s\n", root.Name(), root.Version)
			return err
		},
	}
}
