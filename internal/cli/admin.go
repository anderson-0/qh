package cli

import (
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/auth"
)

func workspaceCmd(with withApp) *cobra.Command {
	c := &cobra.Command{Use: "workspace", Short: "Manage workspaces (local store)"}
	c.AddCommand(
		&cobra.Command{Use: "create <name>", Short: "Create a workspace", Args: cobra.ExactArgs(1),
			RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
				if _, err := a.svc.CreateWorkspace(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "created workspace %s\n", args[0])
				return nil
			})},
		&cobra.Command{Use: "ls", Aliases: []string{"list"}, Short: "List workspaces", Args: cobra.NoArgs,
			RunE: with(func(cmd *cobra.Command, a *app, _ []string) error {
				list, err := a.svc.ListWorkspaces(cmd.Context())
				if err != nil {
					return err
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tCREATED")
				for _, ws := range list {
					fmt.Fprintf(w, "%s\t%s\n", ws.Name, ws.CreatedAt.Format(time.RFC3339))
				}
				return w.Flush()
			})},
		&cobra.Command{Use: "rm <name>", Aliases: []string{"remove"}, Short: "Delete a workspace with its connections and keys", Args: cobra.ExactArgs(1),
			RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
				if err := a.svc.DeleteWorkspace(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "deleted workspace %s\n", args[0])
				return nil
			})},
	)
	return c
}

func keyCmd(with withApp) *cobra.Command {
	c := &cobra.Command{Use: "key", Short: "Manage API keys for server mode (local store)"}
	var workspace, name, expires string
	var scopes []string
	create := &cobra.Command{Use: "create", Short: "Create an API key (shown once)", Args: cobra.NoArgs,
		RunE: with(func(cmd *cobra.Command, a *app, _ []string) error {
			exp, err := auth.ParseExpiry(expires)
			if err != nil {
				return err
			}
			ws := workspace
			if slices.Contains(scopes, auth.ScopeAdmin) && !cmd.Flags().Changed("workspace") {
				ws = ""
			}
			tok, info, err := auth.CreateKey(cmd.Context(), a.st, auth.KeyInput{Workspace: ws, Scopes: scopes, Name: name, ExpiresIn: exp})
			if err != nil {
				return err
			}
			owner := "workspace " + info.Workspace
			if info.Workspace == "" {
				owner = "admin key"
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "created key %s (%s, %s)\n%s\nStore it now; it will not be shown again.\n", info.ID, strings.Join(info.Scopes, ","), owner, tok)
			return nil
		})}
	create.Flags().StringVar(&workspace, "workspace", "default", "workspace the key belongs to (admin keys belong to none)")
	create.Flags().StringSliceVar(&scopes, "scopes", []string{auth.ScopeQuery}, "query, manage and/or admin")
	create.Flags().StringVar(&name, "name", "", "label for the key")
	create.Flags().StringVar(&expires, "expires", "", "lifetime such as 90d or 720h (default: never)")
	c.AddCommand(create,
		&cobra.Command{Use: "ls", Aliases: []string{"list"}, Short: "List API keys", Args: cobra.NoArgs,
			RunE: with(func(cmd *cobra.Command, a *app, _ []string) error {
				list, err := auth.ListKeys(cmd.Context(), a.st, "", true)
				if err != nil {
					return err
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tPREFIX\tWORKSPACE\tSCOPES\tNAME\tLAST USED\tEXPIRES\tCREATED BY")
				when := func(t *time.Time) string {
					if t == nil {
						return "-"
					}
					return t.Format(time.RFC3339)
				}
				for _, k := range list {
					ws := k.Workspace
					if ws == "" {
						ws = "(admin)"
					}
					by := k.CreatedBy
					if by == "" {
						by = "-"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Prefix, ws, strings.Join(k.Scopes, ","), k.Name, when(k.LastUsedAt), when(k.ExpiresAt), by)
				}
				return w.Flush()
			})},
		&cobra.Command{Use: "rm <id>", Aliases: []string{"revoke"}, Short: "Revoke an API key", Args: cobra.ExactArgs(1),
			RunE: with(func(cmd *cobra.Command, a *app, args []string) error {
				if err := auth.DeleteKey(cmd.Context(), a.st, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "revoked key %s\n", args[0])
				return nil
			})},
	)
	return c
}
