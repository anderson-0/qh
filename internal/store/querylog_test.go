package store

import (
	"context"
	"testing"
	"time"
)

func TestQueryLog(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.CreateWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	add := func(ws, conn string, at time.Time, cat string) {
		t.Helper()
		e := &QueryLogEntry{WorkspaceID: ws, ConnectionID: "id-" + conn, ConnectionName: conn, KeyID: "k1", Tool: "query",
			Query: "SELECT 1", RowCount: 1, DurationMS: 3, ErrorCategory: cat, At: at}
		if err := s.AppendQueryLog(ctx, e); err != nil || e.ID == 0 {
			t.Fatalf("append: %v id %d", err, e.ID)
		}
	}
	add(DefaultWorkspace, "shop", base, "")
	add(DefaultWorkspace, "shop", base.Add(time.Hour), "read_only")
	add(DefaultWorkspace, "crm", base.Add(2*time.Hour), "")
	add("team", "shop", base.Add(3*time.Hour), "")

	all, err := s.ListQueryLog(ctx, QueryLogFilter{WorkspaceID: DefaultWorkspace})
	if err != nil || len(all) != 3 || all[0].ConnectionName != "crm" || !all[2].At.Equal(base) {
		t.Fatalf("list: %v %+v", err, all)
	}
	if all[1].ErrorCategory != "read_only" || all[1].KeyID != "k1" || all[1].Tool != "query" || all[1].RowCount != 1 || all[1].DurationMS != 3 {
		t.Fatalf("fields: %+v", all[1])
	}
	shop, _ := s.ListQueryLog(ctx, QueryLogFilter{WorkspaceID: DefaultWorkspace, ConnectionName: "shop", Limit: 1})
	if len(shop) != 1 || !shop[0].At.Equal(base.Add(time.Hour)) {
		t.Fatalf("filter + limit: %+v", shop)
	}
	since, _ := s.ListQueryLog(ctx, QueryLogFilter{WorkspaceID: DefaultWorkspace, Since: base.Add(time.Hour)})
	if len(since) != 2 {
		t.Fatalf("since: %+v", since)
	}
	if n, err := s.PruneQueryLog(ctx, base.Add(90*time.Minute)); err != nil || n != 2 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if rest, _ := s.ListQueryLog(ctx, QueryLogFilter{WorkspaceID: DefaultWorkspace}); len(rest) != 1 {
		t.Fatalf("after prune: %+v", rest)
	}
	if err := s.DeleteWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if team, _ := s.ListQueryLog(ctx, QueryLogFilter{WorkspaceID: "team"}); len(team) != 0 {
		t.Fatalf("deleting a workspace kept its log: %+v", team)
	}
}

func TestConnectionTLSSummary(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	c := sample("tls")
	c.TLS = `{"mode":"verify-full","ca":true}`
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetConnection(ctx, DefaultWorkspace, "tls")
	if got.TLS != c.TLS {
		t.Fatalf("tls %q", got.TLS)
	}
	got.TLS = ""
	if err := s.UpdateConnection(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetConnection(ctx, DefaultWorkspace, "tls"); again.TLS != "" {
		t.Fatalf("cleared tls %q", again.TLS)
	}
}
