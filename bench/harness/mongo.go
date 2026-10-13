package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmongodb "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoImage is the MongoDB server for the document datasets. 8.2, not 8:
// mongo:8 (8.3) and mongo:8.0 refuse some Docker kernels (SERVER-121912).
const mongoImage = "mongo:8.2"

// docSource builds one collection: sql returns one text column holding each
// document as relaxed Extended JSON.
type docSource struct {
	coll    string
	sql     string
	indexes []string
}

// date renders a timestamp column as an Extended JSON date (NULL stays NULL).
func date(col string) string {
	return fmt.Sprintf(`CASE WHEN %[1]s IS NULL THEN NULL ELSE json_build_object('$date', to_char(%[1]s::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')) END`, col)
}

const pagilaAddress = `json_build_object('address', ad.address, 'district', ad.district, 'postal_code', ad.postal_code,
	'phone', ad.phone, 'city', ci.city, 'country', co.country)`

const pagilaAddressJoin = `JOIN address ad ON ad.address_id = x.address_id JOIN city ci ON ci.city_id = ad.city_id
	JOIN country co ON co.country_id = ci.country_id`

var pagilaDocs = []docSource{
	{"films", `SELECT json_build_object('_id', f.film_id, 'title', f.title, 'description', f.description,
		'release_year', f.release_year::int, 'language', trim(l.name), 'rental_duration', f.rental_duration,
		'rental_rate', f.rental_rate, 'length', f.length, 'replacement_cost', f.replacement_cost, 'rating', f.rating::text,
		'special_features', to_json(f.special_features),
		'actors', (SELECT coalesce(json_agg(json_build_object('actor_id', a.actor_id, 'first_name', a.first_name, 'last_name', a.last_name) ORDER BY a.actor_id), '[]')
			FROM film_actor fa JOIN actor a ON a.actor_id = fa.actor_id WHERE fa.film_id = f.film_id),
		'categories', (SELECT coalesce(json_agg(c.name ORDER BY c.name), '[]')
			FROM film_category fc JOIN category c ON c.category_id = fc.category_id WHERE fc.film_id = f.film_id))::text
		FROM film f JOIN language l ON l.language_id = f.language_id`, nil},
	{"actors", `SELECT json_build_object('_id', actor_id, 'first_name', first_name, 'last_name', last_name)::text FROM actor`, nil},
	{"customers", `SELECT json_build_object('_id', x.customer_id, 'store_id', x.store_id, 'first_name', x.first_name,
		'last_name', x.last_name, 'email', x.email, 'active', x.activebool, 'create_date', ` + date("x.create_date") + `,
		'address', ` + pagilaAddress + `)::text FROM customer x ` + pagilaAddressJoin, nil},
	{"staff", `SELECT json_build_object('_id', x.staff_id, 'first_name', x.first_name, 'last_name', x.last_name,
		'email', x.email, 'store_id', x.store_id, 'active', x.active, 'username', x.username,
		'address', ` + pagilaAddress + `)::text FROM staff x ` + pagilaAddressJoin, nil},
	{"stores", `SELECT json_build_object('_id', x.store_id, 'manager_staff_id', x.manager_staff_id,
		'address', ` + pagilaAddress + `)::text FROM store x ` + pagilaAddressJoin, nil},
	{"inventory", `SELECT json_build_object('_id', inventory_id, 'film_id', film_id, 'store_id', store_id)::text FROM inventory`, []string{"film_id"}},
	{"rentals", `SELECT json_build_object('_id', rental_id, 'rental_date', ` + date("rental_date") + `, 'inventory_id', inventory_id,
		'customer_id', customer_id, 'return_date', ` + date("return_date") + `, 'staff_id', staff_id)::text FROM rental`, []string{"customer_id"}},
	{"payments", `SELECT json_build_object('_id', payment_id, 'customer_id', customer_id, 'staff_id', staff_id,
		'rental_id', rental_id, 'amount', amount, 'payment_date', ` + date("payment_date") + `)::text FROM payment`, []string{"customer_id"}},
}

const lendcoResidence = `json_build_object('street_line', r.street_line, 'street_line2', r.street_line2, 'district_nm', r.district_nm,
	'postcode', r.postcode, 'phone_no', r.phone_no, 'locality_nm', lo.locality_nm, 'nation_nm', na.nation_nm)`

const lendcoResidenceJoin = `JOIN residence r ON r.residence_key = x.residence_key JOIN locality lo ON lo.locality_key = r.locality_key
	JOIN nation na ON na.nation_key = lo.nation_key`

var lendcoDocs = []docSource{
	{"catalog_item", `SELECT json_build_object('_id', c.item_key, 'item_title', c.item_title, 'synopsis', c.synopsis,
		'release_yr', c.release_yr, 'lang_label', l.lang_label, 'loan_days', c.loan_days, 'loan_fee', c.loan_fee,
		'runtime_min', c.runtime_min, 'replacement_fee', c.replacement_fee, 'audience', c.audience::text, 'extras', to_json(c.extras),
		'cast', (SELECT coalesce(json_agg(json_build_object('performer_key', p.performer_key, 'given_nm', p.given_nm, 'family_nm', p.family_nm) ORDER BY p.performer_key), '[]')
			FROM item_cast ic JOIN performer p ON p.performer_key = ic.performer_key WHERE ic.item_key = c.item_key),
		'genres', (SELECT coalesce(json_agg(g.genre_label ORDER BY g.genre_label), '[]')
			FROM item_genre ig JOIN genre g ON g.genre_key = ig.genre_key WHERE ig.item_key = c.item_key))::text
		FROM catalog_item c JOIN lang l ON l.lang_key = c.lang_key`, nil},
	{"performer", `SELECT json_build_object('_id', performer_key, 'given_nm', given_nm, 'family_nm', family_nm)::text FROM performer`, nil},
	{"member", `SELECT json_build_object('_id', x.member_key, 'branch_key', x.branch_key, 'given_nm', x.given_nm,
		'family_nm', x.family_nm, 'contact_email', x.contact_email, 'is_active', x.is_active, 'joined_on', ` + date("x.joined_on") + `,
		'residence', ` + lendcoResidence + `)::text FROM member x ` + lendcoResidenceJoin, nil},
	{"employee", `SELECT json_build_object('_id', x.employee_key, 'given_nm', x.given_nm, 'family_nm', x.family_nm,
		'contact_email', x.contact_email, 'branch_key', x.branch_key, 'is_active', x.is_active, 'login_nm', x.login_nm,
		'residence', ` + lendcoResidence + `)::text FROM employee x ` + lendcoResidenceJoin, nil},
	{"branch", `SELECT json_build_object('_id', x.branch_key, 'supervisor_key', x.supervisor_key,
		'residence', ` + lendcoResidence + `)::text FROM branch x ` + lendcoResidenceJoin, nil},
	{"stock_unit", `SELECT json_build_object('_id', unit_key, 'item_key', item_key, 'branch_key', branch_key)::text FROM stock_unit`, []string{"item_key"}},
	{"loan", `SELECT json_build_object('_id', loan_key, 'loaned_at', ` + date("loaned_at") + `, 'unit_key', unit_key,
		'member_key', member_key, 'returned_at', ` + date("returned_at") + `, 'employee_key', employee_key)::text FROM loan`, []string{"member_key"}},
	{"charge", `SELECT json_build_object('_id', charge_key, 'member_key', member_key, 'employee_key', employee_key,
		'loan_key', loan_key, 'amt', amt, 'charged_at', ` + date("charged_at") + `)::text FROM charge`, []string{"member_key"}},
}

// The document datasets carry no description: mongodb-mcp-server has no
// place for one, so neither server gets it.

// StartPagilaDocs loads Pagila as MongoDB documents.
func StartPagilaDocs(ctx context.Context) (*Dataset, func(), error) {
	return startDocs(ctx, "pagila-docs", "pagila", StartPagila, pagilaDocs)
}

// StartLendcoDocs loads Lendco as MongoDB documents.
func StartLendcoDocs(ctx context.Context) (*Dataset, func(), error) {
	return startDocs(ctx, "lendco-docs", "lendco", StartLendco, lendcoDocs)
}

// startDocs starts the Postgres dataset (kept for reference answers) and a
// MongoDB container, and copies the documents built by sources into db.
func startDocs(ctx context.Context, name, db string,
	start func(context.Context) (*Dataset, func(), error), sources []docSource) (*Dataset, func(), error) {
	pg, stopPG, err := start(ctx)
	if err != nil {
		return nil, nil, err
	}
	ctr, err := tcmongodb.Run(ctx, mongoImage)
	stop := func() { _ = testcontainers.TerminateContainer(ctr); stopPG() }
	if err != nil {
		stop()
		return nil, nil, err
	}
	base, err := ctr.ConnectionString(ctx)
	if err != nil {
		stop()
		return nil, nil, err
	}
	u, err := url.Parse(base)
	if err != nil {
		stop()
		return nil, nil, err
	}
	u.Path = "/" + db
	dsn := u.String()
	client, err := mongo.Connect(options.Client().ApplyURI(dsn))
	if err != nil {
		stop()
		return nil, nil, err
	}
	defer client.Disconnect(context.Background())
	for _, s := range sources {
		if err := copyDocs(ctx, pg, client.Database(db).Collection(s.coll), s); err != nil {
			stop()
			return nil, nil, fmt.Errorf("%s.%s: %w", name, s.coll, err)
		}
	}
	return &Dataset{Name: name, Engine: "mongodb", DSN: dsn, DB: pg.DB}, stop, nil
}

func copyDocs(ctx context.Context, pg *Dataset, coll *mongo.Collection, s docSource) error {
	rows, err := pg.DB.QueryContext(ctx, s.sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	var batch []any
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := coll.InsertMany(ctx, batch)
		batch = batch[:0]
		return err
	}
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return err
		}
		var d bson.D
		if err := bson.UnmarshalExtJSON([]byte(text), false, &d); err != nil {
			return fmt.Errorf("document %.80s: %w", text, err)
		}
		batch = append(batch, d)
		if len(batch) == 1000 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	for _, f := range s.indexes {
		if _, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: f, Value: 1}}}); err != nil {
			return err
		}
	}
	return nil
}

// RunTwin runs a question's MongoDB twin and returns its rows: each
// document's values in field order (the twin's final $project fixes the
// order and drops _id).
func RunTwin(ctx context.Context, dsn string, twin json.RawMessage) ([][]any, error) {
	var t struct {
		Collection string `bson:"collection"`
		Pipeline   bson.A `bson:"pipeline"`
	}
	if err := bson.UnmarshalExtJSON(twin, false, &t); err != nil {
		return nil, err
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	client, err := mongo.Connect(options.Client().ApplyURI(dsn))
	if err != nil {
		return nil, err
	}
	defer client.Disconnect(context.Background())
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cur, err := client.Database(u.Path[1:]).Collection(t.Collection).Aggregate(ctx, t.Pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out [][]any
	for cur.Next(ctx) {
		elems, err := cur.Current.Elements()
		if err != nil {
			return nil, err
		}
		row := make([]any, 0, len(elems))
		for _, e := range elems {
			row = append(row, plain(e.Value()))
		}
		out = append(out, row)
	}
	return out, cur.Err()
}

// plain converts a BSON value to what Equal compares: numbers as int64 or
// float64, decimals and strings as strings, dates as time.Time.
func plain(v bson.RawValue) any {
	switch v.Type {
	case bson.TypeInt32:
		return int64(v.Int32())
	case bson.TypeInt64:
		return v.Int64()
	case bson.TypeDouble:
		return v.Double()
	case bson.TypeDecimal128:
		return v.Decimal128().String()
	case bson.TypeString:
		return v.StringValue()
	case bson.TypeBoolean:
		return v.Boolean()
	case bson.TypeDateTime:
		return time.UnixMilli(v.DateTime()).UTC()
	case bson.TypeNull, bson.TypeUndefined:
		return nil
	}
	return v.String()
}
