package cli

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcmongodb "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestMongoDBEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	// mongo:8 (8.3.x) and mongo:8.0 refuse some Docker kernels; 8.2 runs.
	ctr, err := tcmongodb.Run(ctx, "mongo:8.2")
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	if err != nil {
		t.Fatal(err)
	}
	base, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/shop"
	dsn := u.String()
	client, err := mongo.Connect(options.Client().ApplyURI(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	var orders []any
	for i := 1; i <= 30; i++ {
		orders = append(orders, bson.D{{Key: "_id", Value: int32(i)}, {Key: "status", Value: []string{"paid", "pending"}[i%2]}, {Key: "total", Value: float64(i)}})
	}
	if _, err := client.Database("shop").Collection("orders").InsertMany(ctx, orders); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	if _, errOut, code := run(t, home, dsn+"\n", "conn", "add", "shop", "--engine", "mongodb", "--dsn", "-"); code != 0 {
		t.Fatal(errOut)
	}
	out, errOut, code := run(t, home, "", "query", "shop", `db.orders.countDocuments({status: "paid"})`)
	if code != 0 || !strings.Contains(out, "count\n15\n") {
		t.Fatalf("query: %d %q %q", code, out, errOut)
	}
	out, errOut, code = run(t, home, "", "schema", "shop")
	if code != 0 || !strings.Contains(out, "orders ~30: _id PK, status, total") {
		t.Fatalf("schema: %d %q %q", code, out, errOut)
	}
	_, errOut, code = run(t, home, "", "query", "shop", `db.ordrs.find({})`)
	if code == 0 || !strings.Contains(errOut, `collection "ordrs" does not exist`) || !strings.Contains(errOut, "orders") {
		t.Fatalf("unknown collection: %d %q", code, errOut)
	}
	_, errOut, code = run(t, home, "", "query", "shop", `db.orders.deleteMany({})`)
	if code == 0 || !strings.Contains(errOut, "read_only") {
		t.Fatalf("write on read-only: %d %q", code, errOut)
	}
}
