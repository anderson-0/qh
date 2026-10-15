package mongodb

import (
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Match the generated micro-benchmark documents, with a second case that
// exercises the values used to verify cross-collection references.
func BenchmarkInfer(b *testing.B) {
	for _, refs := range []bool{false, true} {
		b.Run(fmt.Sprintf("references=%v", refs), func(b *testing.B) {
			docs := make([]bson.Raw, sampleSize)
			for k := range docs {
				d := bson.D{{Key: "_id", Value: int32(k + 1)}, {Key: "name", Value: fmt.Sprintf("item %d", k)},
					{Key: "status", Value: []string{"new", "open", "closed"}[k%3]}, {Key: "amount", Value: float64(k) * 1.25},
					{Key: "created_at", Value: bson.NewDateTimeFromTime(time.Unix(int64(1700000000+k), 0))},
					{Key: "owner", Value: bson.D{{Key: "name", Value: "o"}, {Key: "email", Value: "o@example.com"}}}, {Key: "tags", Value: bson.A{"a", "b"}}}
				if refs {
					d = append(d, bson.E{Key: "t0001_id", Value: int32(k + 1)})
				}
				raw, err := bson.Marshal(d)
				if err != nil {
					b.Fatal(err)
				}
				docs[k] = raw
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				infer(docs)
			}
		})
	}
}
