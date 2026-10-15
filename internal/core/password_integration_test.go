package core

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcmariadb "github.com/testcontainers/testcontainers-go/modules/mariadb"
	tcmongodb "github.com/testcontainers/testcontainers-go/modules/mongodb"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/anderson-0/qh/internal/engine"
	_ "github.com/anderson-0/qh/internal/engine/mongodb"
)

// Credentials are generated fixtures used only by these disposable databases.
// Failures deliberately avoid printing driver errors, SQL or connection strings.
const passwordAuthInitial = "fixture-initial-password"
const passwordAuthNext = "fixture-rotated-password"
const passwordAuthAdmin = "fixture-admin-password"

type passwordAuthFixture struct {
	engine, dsn, query, prefix string
	rotate                     func()
}

func TestPasswordPatchAuthentication(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	passwordPGEnvironment(t)
	for _, name := range []string{"postgres17", "mysql84", "mariadb114", "mongodb82"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			f := passwordAuthStart(t, ctx, name)
			h := newHarness(t)
			info, tr, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: f.engine, DSN: f.dsn, PoolSize: 1}, true)
			if err != nil || tr == nil {
				t.Fatal("initial authenticated connection failed")
			}
			passwordAuthQuery(t, ctx, h, f)
			before, _ := passwordRecord(t, h)
			target, err := h.svc.resolve(ctx, ws, "pg")
			if err != nil {
				t.Fatal("initial resolve failed")
			}
			old, release, err := h.svc.pools.Get(ctx, target.spec)
			if err != nil {
				t.Fatal("initial pool unavailable")
			}
			defer release()
			if target.spec.PoolSize != 1 || info.PoolSize != 1 {
				t.Fatal("fixture lost one-slot pool")
			}
			f.rotate()
			next := passwordAuthNext
			changed, tested, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{Password: &next}, true)
			if err != nil || tested == nil || changed.Version != before.Version+1 {
				t.Fatal("rotation did not test and save replacement credential")
			}
			rec, in := passwordRecord(t, h)
			eng, _ := engine.Get(f.engine)
			passwordWant(t, eng, in.DSN, next)
			// PostgreSQL's held one-slot pool must remain alive until it is released.
			if f.engine == "postgres" {
				if _, err := old.Ping(ctx); err != nil {
					t.Fatal("password edit closed in-flight PostgreSQL pool")
				}
			}
			passwordAuthQuery(t, ctx, h, f)
			updated, err := h.svc.resolve(ctx, ws, "pg")
			if err != nil || updated.spec.Version != changed.Version {
				t.Fatal("new acquisition used stale version")
			}
			newer, done, err := h.svc.pools.Get(ctx, updated.spec)
			if err != nil {
				t.Fatal("new version pool unavailable")
			}
			defer done()
			if newer == old {
				t.Fatal("credential rotation reused old version pool")
			}
			release()
			wrong := "fixture-wrong-password"
			if _, _, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{Password: &wrong}, true); err == nil {
				t.Fatal("wrong-password update succeeded")
			} else {
				passwordPrivate(t, err.Error(), wrong, passwordAuthNext, passwordAuthInitial)
			}
			passwordUnchanged(t, h, rec)
			passwordAuthQuery(t, ctx, h, f)
			passwordPrivate(t, changed, passwordAuthNext, passwordAuthInitial)
			t.Log("authenticated rotation, fresh version query and wrong-password rollback executed")
		})
	}
}
func passwordAuthQuery(t *testing.T, ctx context.Context, h *harness, f passwordAuthFixture) {
	t.Helper()
	out, err := h.svc.Query(ctx, ws, QueryInput{Source: "pg", Query: f.query})
	if err != nil || !strings.HasPrefix(out, f.prefix) {
		t.Fatal("authenticated service query failed")
	}
}
func passwordAuthStart(t *testing.T, ctx context.Context, name string) passwordAuthFixture {
	t.Helper()
	switch name {
	case "postgres17":
		ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine", tcpostgres.WithDatabase("app"), tcpostgres.WithUsername("admin"), tcpostgres.WithPassword(passwordAuthAdmin), tcpostgres.BasicWaitStrategies())
		if ctr != nil {
			t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
		}
		if err != nil {
			t.Fatal("PostgreSQL fixture startup failed")
		}
		dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			t.Fatal("PostgreSQL fixture address missing")
		}
		admin, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatal("PostgreSQL admin connect failed")
		}
		t.Cleanup(func() { _ = admin.Close(context.Background()) })
		if _, err := admin.Exec(ctx, "CREATE ROLE app LOGIN PASSWORD '"+passwordAuthInitial+"'"); err != nil {
			t.Fatal("PostgreSQL fixture account creation failed")
		}
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("PostgreSQL fixture address invalid")
		}
		u.User = url.UserPassword("app", passwordAuthInitial)
		return passwordAuthFixture{engine: "postgres", dsn: u.String(), query: "SELECT 1 AS n", prefix: "n\n1\n", rotate: func() {
			if _, err := admin.Exec(ctx, "ALTER ROLE app PASSWORD '"+passwordAuthNext+"'"); err != nil {
				t.Fatal("PostgreSQL administrative rotation failed")
			}
		}}
	case "mysql84", "mariadb114":
		var ctr testcontainers.Container
		var err error
		if name == "mysql84" {
			var mysql *tcmysql.MySQLContainer
			mysql, err = tcmysql.Run(ctx, "mysql:8.4", tcmysql.WithDatabase("app"), tcmysql.WithUsername("app"), tcmysql.WithPassword(passwordAuthInitial), testcontainers.WithEnv(map[string]string{"MYSQL_ROOT_HOST": "%"}))
			if mysql != nil {
				ctr = mysql
			}
		} else {
			var maria *tcmariadb.MariaDBContainer
			maria, err = tcmariadb.Run(ctx, "mariadb:11.4", tcmariadb.WithDatabase("app"), tcmariadb.WithUsername("app"), tcmariadb.WithPassword(passwordAuthInitial), testcontainers.WithEnv(map[string]string{"MARIADB_ROOT_HOST": "%"}))
			if maria != nil {
				ctr = maria
			}
		}
		if ctr != nil {
			t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
		}
		if err != nil {
			t.Fatal("MySQL family fixture startup failed")
		}
		addr, err := ctr.PortEndpoint(ctx, "3306/tcp", "")
		if err != nil {
			t.Fatal("MySQL family fixture address missing")
		}
		// These modules seed root with the fixture user password. The
		// independent root connection rotates only the app account.
		admin, err := sql.Open("mysql", "root:"+passwordAuthInitial+"@tcp("+addr+")/app?timeout=5s")
		if err != nil {
			t.Fatal("MySQL family admin setup failed")
		}
		t.Cleanup(func() { _ = admin.Close() })
		if err := admin.PingContext(ctx); err != nil {
			t.Fatal("MySQL family admin authentication failed")
		}
		return passwordAuthFixture{engine: "mysql", dsn: "app:" + passwordAuthInitial + "@tcp(" + addr + ")/app?timeout=5s", query: "SELECT 1 AS n", prefix: "n\n1\n", rotate: func() {
			if _, err := admin.ExecContext(ctx, "ALTER USER 'app'@'%' IDENTIFIED BY '"+passwordAuthNext+"'"); err != nil {
				t.Fatal("MySQL family administrative rotation failed")
			}
		}}
	case "mongodb82":
		ctr, err := tcmongodb.Run(ctx, "mongo:8.2", tcmongodb.WithUsername("admin"), tcmongodb.WithPassword(passwordAuthAdmin))
		if ctr != nil {
			t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
		}
		if err != nil {
			t.Fatal("MongoDB fixture startup failed")
		}
		uri, err := ctr.ConnectionString(ctx)
		if err != nil {
			t.Fatal("MongoDB fixture address missing")
		}
		admin, err := mongo.Connect(options.Client().ApplyURI(uri).SetServerSelectionTimeout(5 * time.Second))
		if err != nil {
			t.Fatal("MongoDB admin setup failed")
		}
		t.Cleanup(func() { _ = admin.Disconnect(context.Background()) })
		db := admin.Database("app")
		if err := db.RunCommand(ctx, bson.D{{Key: "createUser", Value: "app"}, {Key: "pwd", Value: passwordAuthInitial}, {Key: "roles", Value: bson.A{bson.D{{Key: "role", Value: "readWrite"}, {Key: "db", Value: "app"}}}}}).Err(); err != nil {
			t.Fatal("MongoDB fixture account creation failed")
		}
		if _, err := db.Collection("probe").InsertOne(ctx, bson.D{{Key: "n", Value: 1}}); err != nil {
			t.Fatal("MongoDB fixture insertion failed")
		}
		u, err := url.Parse(uri)
		if err != nil {
			t.Fatal("MongoDB fixture address invalid")
		}
		u.User = url.UserPassword("app", passwordAuthInitial)
		u.Path = "/app"
		u.RawQuery = "authSource=app&serverSelectionTimeoutMS=2000&connectTimeoutMS=2000"
		return passwordAuthFixture{engine: "mongodb", dsn: u.String(), query: "db.probe.countDocuments({})", prefix: "count\n1\n", rotate: func() {
			if err := db.RunCommand(ctx, bson.D{{Key: "updateUser", Value: "app"}, {Key: "pwd", Value: passwordAuthNext}}).Err(); err != nil {
				t.Fatal("MongoDB administrative rotation failed")
			}
		}}
	default:
		t.Fatal("unknown authentication fixture")
		return passwordAuthFixture{}
	}
}
