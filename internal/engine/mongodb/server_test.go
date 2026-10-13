package mongodb

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmongodb "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

const testPassword = "mongo-secret-pw"

// mongo8 is the MongoDB 8 image the tests use. mongo:8 (8.3.x) and mongo:8.0
// refuse to start on Linux kernels 6.19 and newer (SERVER-121912), which
// Docker Desktop now ships; 8.2 starts.
const mongo8 = "mongo:8.2"

var (
	serversMu sync.Mutex
	servers   = map[string]string{} // image → base URI; one container per image per test binary
	dbSeq     atomic.Int64
)

// mongoURI returns mongodb://root:<pw>@host:port/?authSource=admin for a
// shared container running image. Containers stop when the process exits
// (testcontainers' reaper).
func mongoURI(t *testing.T, image string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	serversMu.Lock()
	defer serversMu.Unlock()
	if u, ok := servers[image]; ok {
		return u
	}
	ctr, err := tcmongodb.Run(context.Background(), image, tcmongodb.WithUsername("root"), tcmongodb.WithPassword(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	u, err := ctr.ConnectionString(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	pu.Path = "/"
	q := pu.Query()
	q.Set("authSource", "admin")
	pu.RawQuery = q.Encode()
	servers[image] = pu.String()
	return servers[image]
}

// withDB returns uri with db as its path.
func withDB(uri, db string) string {
	u, _ := url.Parse(uri)
	u.Path = "/" + db
	return u.String()
}

// loadFixture creates a fresh database with the shared fixture, mirroring
// testutil.SQLiteFixtureStatements: users (unique email), orders (250, with
// user_id), order_items (20, with order_id) and the paid_orders view.
func loadFixture(t *testing.T, uri string) string {
	t.Helper()
	return loadFixtureNamed(t, uri, fmt.Sprintf("fx_%d_%d", time.Now().UnixNano()%1e6, dbSeq.Add(1)))
}

// loadFixtureNamed loads the fixture into database db, dropping it first.
// Golden-file tests use a fixed name so the output is stable.
func loadFixtureNamed(t *testing.T, uri, db string) string {
	t.Helper()
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	d := client.Database(db)
	if err := d.Drop(ctx); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2025, 3, 14, 10, 22, 31, 0, time.UTC)
	users := []any{
		bson.D{{Key: "_id", Value: int32(1)}, {Key: "email", Value: "alice@example.com"}, {Key: "name", Value: "Alice"},
			{Key: "bio", Value: "line1\nline2\tx"}, {Key: "avatar", Value: bson.Binary{Data: []byte{0xde, 0xad, 0xbe, 0xef}}},
			{Key: "created_at", Value: bson.NewDateTimeFromTime(created)}},
		bson.D{{Key: "_id", Value: int32(2)}, {Key: "email", Value: "zoe@example.com"}, {Key: "name", Value: "Zoë 🚀"}},
		bson.D{{Key: "_id", Value: int32(3)}, {Key: "email", Value: "nobody@example.com"}, {Key: "name", Value: nil}},
	}
	if _, err := d.Collection("users").InsertMany(ctx, users); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Collection("users").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "email", Value: 1}}, Options: options.Index().SetUnique(true)}); err != nil {
		t.Fatal(err)
	}
	var orders []any
	for i := 1; i <= 250; i++ {
		status := "pending"
		if i%2 == 0 {
			status = "paid"
		}
		orders = append(orders, bson.D{{Key: "_id", Value: int32(i)}, {Key: "user_id", Value: int32(i%3 + 1)},
			{Key: "status", Value: status}, {Key: "total", Value: float64(i) * 1.5}})
	}
	if _, err := d.Collection("orders").InsertMany(ctx, orders); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Collection("orders").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}}}); err != nil {
		t.Fatal(err)
	}
	var items []any
	for i := 1; i <= 10; i++ {
		for k, sku := range []string{"sku-1", "sku-2"} {
			items = append(items, bson.D{{Key: "_id", Value: fmt.Sprintf("%d-%s", i, sku)}, {Key: "order_id", Value: int32(i)},
				{Key: "sku", Value: sku}, {Key: "qty", Value: int32(k + 1)}})
		}
	}
	if _, err := d.Collection("order_items").InsertMany(ctx, items); err != nil {
		t.Fatal(err)
	}
	view := bson.A{bson.D{{Key: "$match", Value: bson.D{{Key: "status", Value: "paid"}}}},
		bson.D{{Key: "$project", Value: bson.D{{Key: "user_id", Value: 1}, {Key: "total", Value: 1}}}}}
	if err := d.CreateView(ctx, "paid_orders", "orders", view); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := mongo.Connect(options.Client().ApplyURI(uri))
		if err == nil {
			_ = c.Database(db).Drop(context.Background())
			_ = c.Disconnect(context.Background())
		}
	})
	return db
}

// fixtureSpec returns a spec for a fresh fixture database on image.
func fixtureSpec(t *testing.T, image string, mode engine.Mode) engine.ConnSpec {
	t.Helper()
	uri := mongoURI(t, image)
	db := loadFixture(t, uri)
	return engine.ConnSpec{ID: "fx", Version: 1, Engine: "mongodb", DSN: withDB(uri, db), Mode: mode, PoolSize: 2}
}

func openSpec(t *testing.T, spec engine.ConnSpec) engine.Conn {
	t.Helper()
	c, err := Engine{}.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestOpenAndPing(t *testing.T) {
	for _, image := range []string{"mongo:7", mongo8} {
		t.Run(image, func(t *testing.T) {
			c := openSpec(t, fixtureSpec(t, image, engine.ModeRO))
			v, err := c.Ping(context.Background())
			if err != nil || !strings.HasPrefix(v, "MongoDB "+strings.TrimPrefix(image, "mongo:")) {
				t.Fatalf("Ping = %q, %v", v, err)
			}
		})
	}
}

func TestPasswordReplacesAndIsNeverShown(t *testing.T) {
	uri := mongoURI(t, mongo8)
	noPW := strings.Replace(uri, ":"+testPassword+"@", "@", 1)
	c := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: noPW, Password: testPassword, Mode: engine.ModeRO})
	if _, err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := Engine{}.Open(context.Background(), engine.ConnSpec{Engine: "mongodb", DSN: noPW + "&serverSelectionTimeoutMS=3000", Password: "wrong-pw-xyz", Mode: engine.ModeRO})
	if e := qerr.From(err); err == nil || e.Category != qerr.ConnectionFailed || strings.Contains(err.Error(), "wrong-pw-xyz") {
		t.Fatalf("wrong password: %v", err)
	}
}

func TestDialerSeesEveryHost(t *testing.T) {
	uri := mongoURI(t, mongo8)
	u, _ := url.Parse(uri)
	var mu sync.Mutex
	seen := map[string]bool{}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		seen[addr] = true
		mu.Unlock()
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	two := strings.Replace(uri, u.Host, u.Host+",127.0.0.1:1", 1) + "&serverSelectionTimeoutMS=2000"
	c, err := Engine{}.Open(context.Background(), engine.ConnSpec{Engine: "mongodb", DSN: two, Dial: dial, Mode: engine.ModeRO})
	if err == nil {
		c.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if !seen[u.Host] || !seen["127.0.0.1:1"] {
		t.Fatalf("dialled %v, want both %s and 127.0.0.1:1", seen, u.Host)
	}
}

func TestTLSSettingsApply(t *testing.T) {
	uri := mongoURI(t, mongo8)
	_, err := Engine{}.Open(context.Background(), engine.ConnSpec{Engine: "mongodb", DSN: uri + "&serverSelectionTimeoutMS=2000",
		TLS: &engine.TLS{Mode: engine.TLSRequire}, Mode: engine.ModeRO})
	if e := qerr.From(err); err == nil || e.Category != qerr.ConnectionFailed {
		t.Fatalf("TLS against a plain server: %v", err)
	}
}

func TestServerModeRefusesFileOptions(t *testing.T) {
	_, err := Engine{}.Open(context.Background(), engine.ConnSpec{Engine: "mongodb", DSN: "mongodb://h/db?tlsCAFile=/etc/passwd", Server: true})
	// The driver's own error for an unreadable CA file is invalid too, so
	// check that qh's guard refused it.
	if e := qerr.From(err); err == nil || e.Category != qerr.Invalid || !strings.Contains(e.Message, "does not accept the option tlsCAFile") {
		t.Fatalf("server mode with tlsCAFile: %v", err)
	}
}
