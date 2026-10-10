package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/secret"
)

func masterkeyCmd(home *string) *cobra.Command {
	c := &cobra.Command{Use: "masterkey", Short: "Manage the master key that seals connection strings"}
	var oldFile string
	rotate := &cobra.Command{
		Use:   "rotate",
		Short: "Re-seal every connection with the current master key (stop qh serve first)",
		Long: "Re-seals every stored connection with the current master key (QH_MASTER_KEY, or master.key in the data directory).\n" +
			"--old-key-file holds the previous key. Stop any running qh serve or qh mcp first, and restart it with the new key afterwards.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if oldFile == "" {
				return qerr.New(qerr.Invalid, "--old-key-file is required").WithHint("Give the file holding the previous base64 master key")
			}
			b, err := os.ReadFile(oldFile)
			if err != nil {
				return qerr.Wrap(qerr.Invalid, "cannot read --old-key-file", err)
			}
			old, err := secret.ParseKey(string(b))
			if err != nil {
				return qerr.New(qerr.Invalid, "--old-key-file does not hold a base64 32-byte key")
			}
			dir, err := homeDir(*home)
			if err != nil {
				return err
			}
			a, err := openAppWith(dir, core.Options{}, old)
			if err != nil {
				return err
			}
			defer a.Close()
			if secret.KeyID(old) == a.keys.PrimaryID() {
				return qerr.New(qerr.Invalid, "the old key is the current master key").
					WithHint("Set QH_MASTER_KEY (or master.key) to the new key, and pass the previous one as --old-key-file")
			}
			n, cur, err := a.svc.RotateMasterKey(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "re-sealed %d of %d connections with master key %08x\n", n, n+cur, a.keys.PrimaryID())
			return nil
		},
	}
	rotate.Flags().StringVar(&oldFile, "old-key-file", "", "file holding the previous base64 master key")
	c.AddCommand(rotate)
	return c
}
