package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestDocsQuestionsMirrorSQL(t *testing.T) {
	for docs, sql := range map[string]string{"pagila-docs": "pagila", "lendco-docs": "lendco"} {
		dq, err := LoadQuestions(docs)
		if err != nil {
			t.Fatal(err)
		}
		sq, _ := LoadQuestions(sql)
		if len(dq) != len(sq) {
			t.Fatalf("%s: %d questions, %s has %d", docs, len(dq), sql, len(sq))
		}
		for i := range dq {
			d, s := dq[i], sq[i]
			if d.ID != docs+strings.TrimPrefix(s.ID, sql) || d.Kind != s.Kind || d.Question != s.Question || d.SQL != s.SQL || d.Ordered != s.Ordered {
				t.Errorf("%s does not mirror %s", d.ID, s.ID)
			}
			var twin struct {
				Collection string `json:"collection"`
				Pipeline   []any  `json:"pipeline"`
			}
			if err := json.Unmarshal(d.Mongo, &twin); err != nil || twin.Collection == "" || len(twin.Pipeline) == 0 {
				t.Errorf("%s: bad twin %s: %v", d.ID, d.Mongo, err)
			}
			var parsed bson.D
			if err := bson.UnmarshalExtJSON(d.Mongo, false, &parsed); err != nil {
				t.Errorf("%s: twin is not Extended JSON: %v", d.ID, err)
			}
		}
	}
}

// TestDocsTwinsMatchTruth proves every question is answerable from the
// documents: each twin pipeline returns the reference SQL's answer.
func TestDocsTwinsMatchTruth(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker and the Pagila download")
	}
	ctx := context.Background()
	for _, name := range []string{"pagila-docs", "lendco-docs"} {
		t.Run(name, func(t *testing.T) {
			ds, stop, err := StartDataset(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			if ds.Engine != "mongodb" || !strings.HasPrefix(ds.DSN, "mongodb://") {
				t.Fatalf("dataset %+v", ds)
			}
			qs, err := LoadQuestions(name)
			if err != nil || len(qs) == 0 {
				t.Fatalf("questions: %d, %v", len(qs), err)
			}
			for _, q := range qs {
				want, err := Truth(ctx, ds.DB, q.SQL)
				if err != nil {
					t.Fatalf("%s truth: %v", q.ID, err)
				}
				if len(want) == 0 {
					t.Fatalf("%s: the reference query returns no rows", q.ID)
				}
				got, err := RunTwin(ctx, ds.DSN, q.Mongo)
				if err != nil {
					t.Errorf("%s twin: %v", q.ID, err)
					continue
				}
				if !Equal(want, got, q.Ordered) {
					t.Errorf("%s: twin %v, truth %v", q.ID, got, want)
				}
			}
		})
	}
}

func TestRunTwinNeedsADatabase(t *testing.T) {
	twin := json.RawMessage(`{"collection": "film", "pipeline": [{"$count": "n"}]}`)
	for _, dsn := range []string{"mongodb://127.0.0.1:1", "mongodb://127.0.0.1:1/", "mongodb://127.0.0.1:1/?appName=x"} {
		if _, err := RunTwin(context.Background(), dsn, twin); err == nil || !strings.Contains(err.Error(), "names no database") {
			t.Errorf("%s: %v", dsn, err)
		}
	}
}
