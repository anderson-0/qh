package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultDBHubVersion is the dbhub release benchmarked (pinned in Task 3 Step 1).
const DefaultDBHubVersion = "1.5.0"

// Conn is one connection registered with a server under test.
type Conn struct{ Name, Engine, DSN, Description string }

// ConnFor is the connection for a dataset, named after it.
func ConnFor(ds *Dataset) Conn {
	return Conn{Name: ds.Name, Engine: ds.Engine, DSN: ds.DSN, Description: ds.Description}
}

// Server is a database MCP server under test.
type Server interface {
	Name() string
	Tools() []*mcp.Tool
	// Instructions is the text the server sent in its initialize result; an
	// MCP client puts it in the model's system prompt.
	Instructions() string
	// Call runs a tool. Protocol errors (an unknown tool, bad arguments) come
	// back as error text, the way an agent client shows them to the model.
	Call(ctx context.Context, tool string, args json.RawMessage) (text string, isError bool)
	Close() error
}

type mcpServer struct {
	name    string
	session *mcp.ClientSession
	tools   []*mcp.Tool
	instr   string
	cleanup func()
}

func connect(ctx context.Context, name string, cmd *exec.Cmd, cleanup func()) (*mcpServer, error) {
	cmd.Stderr = nil                                       // keep server logs out of the benchmark output
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute) // npx may download on first use
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "qh-bench", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	var tools []*mcp.Tool
	for tl, err := range session.Tools(ctx, nil) {
		if err != nil {
			session.Close()
			cleanup()
			return nil, fmt.Errorf("%s: list tools: %w", name, err)
		}
		tools = append(tools, tl)
	}
	var instr string
	if res := session.InitializeResult(); res != nil {
		instr = res.Instructions
	}
	return &mcpServer{name: name, session: session, tools: tools, instr: instr, cleanup: cleanup}, nil
}

func (s *mcpServer) Name() string         { return s.name }
func (s *mcpServer) Tools() []*mcp.Tool   { return s.tools }
func (s *mcpServer) Instructions() string { return s.instr }

func (s *mcpServer) Call(ctx context.Context, tool string, args json.RawMessage) (string, bool) {
	var a map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "invalid tool arguments: " + err.Error(), true
		}
	}
	res, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: a})
	if err != nil {
		return "tool call failed: " + err.Error(), true
	}
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String(), res.IsError
}

func (s *mcpServer) Close() error {
	err := s.session.Close()
	s.cleanup()
	return err
}

// FindTool returns the first tool whose name starts with prefix, or "".
func FindTool(tools []*mcp.Tool, prefix string) string {
	for _, t := range tools {
		if strings.HasPrefix(t.Name, prefix) {
			return t.Name
		}
	}
	return ""
}

// repoRoot is the qh module root, the parent of bench/.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// BuildQH builds the qh binary from the parent module into a temp dir.
func BuildQH(ctx context.Context) (string, func(), error) {
	dir, err := os.MkdirTemp("", "qh-bench-bin-")
	if err != nil {
		return "", nil, err
	}
	bin := filepath.Join(dir, "qh")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/qh")
	cmd.Dir = repoRoot()
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("build qh: %v\n%s", err, out)
	}
	return bin, func() { os.RemoveAll(dir) }, nil
}

// StartQH registers conns in a fresh local store and serves MCP over stdio.
func StartQH(ctx context.Context, bin string, conns []Conn) (Server, error) {
	home, err := os.MkdirTemp("", "qh-bench-home-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { os.RemoveAll(home) }
	for _, c := range conns {
		add := exec.CommandContext(ctx, bin, "--home", home, "conn", "add", c.Name, "--engine", c.Engine, "--dsn", "-", "--description", c.Description)
		add.Stdin = strings.NewReader(c.DSN + "\n")
		add.Env = withoutEnv(os.Environ(), "QH_MASTER_KEY", "QH_SERVER", "QH_KEY", "QH_WORKSPACE")
		if out, err := add.CombinedOutput(); err != nil {
			cleanup()
			return nil, fmt.Errorf("qh conn add %s: %v: %s", c.Name, err, strings.TrimSpace(string(out)))
		}
	}
	cmd := exec.Command(bin, "--home", home, "mcp")
	cmd.Env = withoutEnv(os.Environ(), "QH_MASTER_KEY", "QH_SERVER", "QH_KEY", "QH_WORKSPACE")
	return connect(ctx, "qh", cmd, cleanup)
}

func withoutEnv(env []string, names ...string) []string {
	var out []string
	for _, kv := range env {
		keep := true
		for _, n := range names {
			if strings.HasPrefix(kv, n+"=") {
				keep = false
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func dbhubDSN(c Conn) string {
	if c.Engine == "sqlite" {
		return "sqlite://" + c.DSN // c.DSN is absolute, so this is sqlite:///path
	}
	return c.DSN
}

// StartDBHub writes a dbhub.toml for conns (read-only execute_sql plus
// search_objects, dbhub's own tools; dbhub's defaults otherwise) and serves
// dbhub over stdio through npx.
func StartDBHub(ctx context.Context, version string, conns []Conn) (Server, error) {
	if _, err := exec.LookPath("npx"); err != nil {
		return nil, errors.New("start dbhub: npx is not installed (dbhub needs Node 22.5 or later)")
	}
	dir, err := os.MkdirTemp("", "qh-bench-dbhub-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	var b strings.Builder
	for _, c := range conns {
		fmt.Fprintf(&b, "[[sources]]\nid = %s\ndsn = %s\ndescription = %s\n\n", tomlString(c.Name), tomlString(dbhubDSN(c)), tomlString(c.Description))
		// Once a source has any [[tools]] entry, dbhub exposes only the tools
		// listed, so search_objects (on by default) must be listed as well.
		fmt.Fprintf(&b, "[[tools]]\nname = \"execute_sql\"\nsource = %s\nreadonly = true\n\n", tomlString(c.Name))
		fmt.Fprintf(&b, "[[tools]]\nname = \"search_objects\"\nsource = %s\n\n", tomlString(c.Name))
	}
	path := filepath.Join(dir, "dbhub.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		cleanup()
		return nil, err
	}
	cmd := exec.Command("npx", "-y", "@bytebase/dbhub@"+version, "--transport", "stdio", "--config", path)
	cmd.Dir = dir
	return connect(ctx, "dbhub", cmd, cleanup)
}
