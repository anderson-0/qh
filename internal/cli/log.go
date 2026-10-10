package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anderson-0/qh/internal/core"
)

func logCmd(with withBackend) *cobra.Command {
	var connection, since string
	var limit int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show logged query calls, newest first",
		Args:  cobra.NoArgs,
		RunE: with(func(cmd *cobra.Command, b backend, _ []string) error {
			f := core.QueryLogFilter{Connection: connection, Limit: limit}
			if since != "" {
				t, err := core.ParseSince(since, time.Now())
				if err != nil {
					return err
				}
				f.Since = t
			}
			entries, err := b.QueryLog(cmd.Context(), f)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(entries) == 0 {
				fmt.Fprintln(out, "no logged queries")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "AT\tCONNECTION\tTOOL\tROWS\tMS\tERROR\tKEY\tQUERY")
			dash := func(s string) string {
				if s == "" {
					return "-"
				}
				return s
			}
			for _, e := range entries {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\n", e.At.Local().Format(time.RFC3339), e.Connection, e.Tool,
					e.Rows, e.Duration.Milliseconds(), dash(e.ErrorCategory), dash(e.KeyID), strings.Join(strings.Fields(e.Query), " "))
			}
			return w.Flush()
		}),
	}
	cmd.Flags().StringVar(&connection, "connection", "", "only this connection")
	cmd.Flags().StringVar(&since, "since", "", "only calls since this RFC 3339 time, or this long ago (24h, 7d)")
	cmd.Flags().IntVar(&limit, "limit", 0, "at most this many entries (default 100, max 1000)")
	return cmd
}
