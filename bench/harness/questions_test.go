package harness

import (
	"context"
	"strings"
	"testing"
)

func TestQuestionFiles(t *testing.T) {
	kinds := map[string]bool{"lookup": true, "join": true, "enum": true, "aggregate": true, "large": true, "misspell": true}
	for _, ds := range []string{"pagila", "sakila", "chinook"} {
		qs, err := LoadQuestions(ds)
		if err != nil {
			t.Fatal(err)
		}
		if len(qs) != 30 {
			t.Fatalf("%s: %d questions", ds, len(qs))
		}
		seen := map[string]bool{}
		got := map[string]bool{}
		for _, q := range qs {
			if seen[q.ID] || !strings.HasPrefix(q.ID, ds+"-") || !kinds[q.Kind] || q.SQL == "" || !strings.Contains(q.Question, "Return:") {
				t.Fatalf("%s: bad question %+v", ds, q)
			}
			seen[q.ID], got[q.Kind] = true, true
		}
		for _, k := range []string{"lookup", "join", "aggregate", "large", "misspell"} {
			if !got[k] {
				t.Fatalf("%s has no %s question", ds, k)
			}
		}
		if !strings.Contains(PromptFor(qs[0]), qs[0].Question) {
			t.Fatal("prompt does not carry the question")
		}
	}
	if _, err := LoadQuestions("nope"); err == nil {
		t.Fatal("unknown dataset loaded")
	}
}

func TestReferenceQueries(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the datasets (Docker)")
	}
	for _, name := range []string{"pagila", "sakila", "chinook"} {
		t.Run(name, func(t *testing.T) {
			ds, stop, err := StartDataset(context.Background(), name)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			qs, _ := LoadQuestions(name)
			for _, q := range qs {
				rows, err := Truth(context.Background(), ds.DB, q.SQL)
				if err != nil {
					t.Errorf("%s: %v", q.ID, err)
					continue
				}
				if len(rows) == 0 || len(rows[0]) == 0 || (len(rows) == 1 && len(rows[0]) == 1 && rows[0][0] == nil) {
					t.Errorf("%s: empty or null ground truth %v", q.ID, rows)
				}
				if !Equal(rows, rows, q.Ordered) {
					t.Errorf("%s: ground truth does not equal itself", q.ID)
				}
			}
		})
	}
}
