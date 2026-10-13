package mongodb

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func docCursor(t *testing.T, docs ...bson.D) *mongo.Cursor {
	t.Helper()
	in := make([]any, len(docs))
	for i, d := range docs {
		in[i] = d
	}
	cur, err := mongo.NewCursorFromDocuments(in, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

func TestReadDocsByteBudget(t *testing.T) {
	var docs []bson.D
	for i := 0; i < 5; i++ {
		docs = append(docs, bson.D{{Key: "_id", Value: int32(i)}, {Key: "s", Value: strings.Repeat("x", 100)}})
	}
	size := len(rawDoc(t, docs[0]))
	for _, tc := range []struct {
		limit, budget int
		want          int
		over          bool
	}{
		{10, 1 << 20, 5, false},
		{3, 1 << 20, 3, false},
		{10, 2*size + size/2, 2, true}, // the third would pass the budget
		{10, 2 * size, 2, true},
		{10, 1, 1, true}, // one document is always kept
	} {
		got, over, err := readDocs(context.Background(), docCursor(t, docs...), tc.limit, tc.budget, nil)
		if err != nil || len(got) != tc.want || over != tc.over {
			t.Errorf("limit %d budget %d: %d docs, over %v, err %v; want %d, %v", tc.limit, tc.budget, len(got), over, err, tc.want, tc.over)
		}
	}
	// Skipped documents count against neither the limit nor the budget.
	skipOdd := func(d bson.Raw) bool { return d.Lookup("_id").Int32()%2 == 1 }
	got, over, err := readDocs(context.Background(), docCursor(t, docs...), 10, 3*size, skipOdd)
	if err != nil || len(got) != 3 || over {
		t.Errorf("skip: %d docs, over %v, err %v", len(got), over, err)
	}
}

func TestRelaxedJSONCapsHugeValues(t *testing.T) {
	arr := make(bson.A, 20000)
	for i := range arr {
		arr[i] = int32(i)
	}
	big := strings.Repeat("y", 100<<10)
	doc := rawDoc(t, bson.D{{Key: "arr", Value: arr}, {Key: "obj", Value: bson.D{{Key: "a\"b", Value: int32(1)}, {Key: "s", Value: big}, {Key: "t", Value: int32(2)}}},
		{Key: "small", Value: bson.D{{Key: "a", Value: bson.A{int32(1), "x"}}}}})
	got := relaxedJSON(doc.Lookup("arr"))
	if len(got) > 4096 || !strings.HasPrefix(got, "[0,1,2,3,") || !strings.Contains(got, "…") || !strings.HasSuffix(got, "]") {
		t.Errorf("array: %d bytes: %.120s … %s", len(got), got, got[max(0, len(got)-60):])
	}
	got = relaxedJSON(doc.Lookup("obj"))
	if len(got) > 4096 || !strings.HasPrefix(got, `{"a\"b":1,"s":"yyy`) || !strings.Contains(got, "…") || !strings.HasSuffix(got, "}") {
		t.Errorf("object: %d bytes: %.120s … %s", len(got), got, got[max(0, len(got)-60):])
	}
	if got := relaxedJSON(doc.Lookup("small")); got != `{"a":[1,"x"]}` {
		t.Errorf("small: %s", got)
	}
}
