package mongodb

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"

	"github.com/anderson-0/qh/internal/engine"
)

// concerns are the connection string's read preference, read concern and
// write concern. RunCommand and RunCommandCursor ignore the client's, so qh
// applies them to each command itself.
type concerns struct {
	rp *readpref.ReadPref
	rc *readconcern.ReadConcern
	wc *writeconcern.WriteConcern
}

func (c *concerns) setConcerns(opts *options.ClientOptions) {
	c.rp, c.rc, c.wc = opts.ReadPreference, opts.ReadConcern, opts.WriteConcern
}

// readConcernCommands are the commands that take a readConcern.
var readConcernCommands = []string{"find", "aggregate", "distinct", "count"}

// command returns op's command with the read concern (a read) or the write
// concern (a write or DDL) added, and the run options: a read goes by the
// read preference, a write always to the primary. op.Command is left alone,
// since explain wraps it.
func (c *concerns) command(op *Op) (bson.D, *options.RunCmdOptionsBuilder) {
	if op.Access == engine.Read {
		return c.readCmd(op.Command), c.readOpts()
	}
	return c.writeCmd(op.Command), options.RunCmd()
}

// readOpts are the run options of a read, explain or introspection command.
func (c *concerns) readOpts() *options.RunCmdOptionsBuilder {
	o := options.RunCmd()
	if c.rp != nil {
		o.SetReadPreference(c.rp)
	}
	return o
}

// explainOpts are the run options of an explain: the read preference for a
// read, the primary otherwise.
func (c *concerns) explainOpts(op *Op) *options.RunCmdOptionsBuilder {
	if op.Access == engine.Read {
		return c.readOpts()
	}
	return options.RunCmd()
}

// readCmd returns cmd with readConcern added when it is set and cmd takes one.
func (c *concerns) readCmd(cmd bson.D) bson.D {
	if c.rc == nil || c.rc.Level == "" || len(cmd) == 0 || !contains(readConcernCommands, cmd[0].Key) {
		return cmd
	}
	return append(cmd[:len(cmd):len(cmd)], bson.E{Key: "readConcern", Value: bson.D{{Key: "level", Value: c.rc.Level}}})
}

// writeCmd returns cmd with writeConcern added when it is set. The driver
// ignores wtimeoutMS, so it has none.
func (c *concerns) writeCmd(cmd bson.D) bson.D {
	if c.wc == nil {
		return cmd
	}
	var wc bson.D
	if c.wc.W != nil {
		wc = append(wc, bson.E{Key: "w", Value: c.wc.W})
	}
	if c.wc.Journal != nil {
		wc = append(wc, bson.E{Key: "j", Value: *c.wc.Journal})
	}
	if len(wc) == 0 {
		return cmd
	}
	return append(cmd[:len(cmd):len(cmd)], bson.E{Key: "writeConcern", Value: wc})
}
