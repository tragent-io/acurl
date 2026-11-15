package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newVersionCmd prints the same line as --version. It reads the root command's
// Version, so the string is built in one place.
func newVersionCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the acurl version",
		Long: `version prints the same line as --version.

To check a host that is literally named "version", give it a scheme: acurl https://version`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s version %s\n", root.Name(), root.Version)
			return err
		},
	}
}
