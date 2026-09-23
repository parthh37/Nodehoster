// Package cli is the command line's management commands (`nodehoster site
// list`, `deploy`, `logs`...). They talk to the running service over the
// local admin endpoint, as NodeHoster Manager does: the named pipe
// \\.\pipe\NodeHoster.Admin on Windows, which only elevated Administrators
// can open, so there is no sign-in (a Unix socket in the data directory
// elsewhere, for development and tests). Like appcmd.exe for IIS, they are
// thin: each is one or two API calls and the API's JSON is available as it
// is with --json, for scripts and the PowerShell module.
//
// Commands are entries in a table (see register): a new one is a new file
// with an init function, not an edit here.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/parthh37/nodehoster/internal/localapi"
)

// Exit codes, the same for every command.
const (
	ExitOK    = 0 // done
	ExitError = 1 // the command failed: the service refused it, a deployment failed, it is not running...
	ExitUsage = 2 // wrong arguments or flags
)

// Env is what a command runs with.
type Env struct {
	Stdout, Stderr io.Writer
	Stdin          io.Reader
	// JSON prints the API's JSON instead of tables and sentences.
	JSON   bool
	Client *localapi.Client
	Ctx    context.Context
	// Interactive reports whether Stdin is a terminal (confirmations).
	Interactive bool
}

// Runner runs a command with its positional arguments.
type Runner func(e *Env, args []string) error

// Command is one entry of the command table.
type Command struct {
	Name    string // its words: "site start"
	Args    string // positional arguments for the usage line: "<site>"
	Summary string
	// MinArgs and MaxArgs bound the positional arguments (MaxArgs -1: any).
	MinArgs, MaxArgs int
	// Setup declares the command's flags on fs and returns the function
	// that runs it, which reads them.
	Setup func(fs *flag.FlagSet) Runner
}

var commands []*Command

// register adds commands to the table; call it from an init function.
func register(cmds ...*Command) { commands = append(commands, cmds...) }

// groupOrder is the order of the help; groups not listed follow, in the
// order they were registered.
var groupOrder = []string{"site", "deploy", "rollback", "releases", "logs", "events", "cert", "backup", "restore"}

// Commands lists the table in usage order.
func Commands() []*Command {
	rank := func(c *Command) int {
		if i := slices.Index(groupOrder, strings.Fields(c.Name)[0]); i >= 0 {
			return i
		}
		return len(groupOrder)
	}
	out := slices.Clone(commands)
	slices.SortStableFunc(out, func(a, b *Command) int { return rank(a) - rank(b) })
	return out
}

// find returns the command whose words start args, preferring the longest
// ("site list" over "site"), and the remaining arguments.
func find(args []string) (*Command, []string) {
	var best *Command
	var n int
	for _, c := range commands {
		words := strings.Fields(c.Name)
		if len(words) > n && len(args) >= len(words) && slices.Equal(args[:len(words)], words) {
			best, n = c, len(words)
		}
	}
	if best == nil {
		return nil, args
	}
	return best, args[n:]
}

// Has reports whether args name a command of the table, or a group of
// them (such as "site" alone), so that main can hand them over.
func Has(args []string) bool {
	if len(args) == 0 {
		return false
	}
	for _, c := range commands {
		if strings.Fields(c.Name)[0] == args[0] {
			return true
		}
	}
	return false
}

// Usage is the command list for `nodehoster help`.
func Usage() string {
	var b strings.Builder
	tw := newTabWriter(&b)
	for _, c := range Commands() {
		fmt.Fprintf(tw, "  %s\t%s\n", strings.TrimSpace(c.Name+" "+c.Args), c.Summary)
	}
	tw.Flush()
	return b.String()
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return &usageError{fmt.Sprintf(format, a...)} }

// Main runs a management command against the service on this computer and
// returns the exit code.
func Main(args []string, dataDir string, jsonOut bool) int {
	// Ctrl+C ends a follow (logs -f) cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, _ := os.Stdin.Stat()
	e := &Env{
		Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, JSON: jsonOut,
		Client: localapi.Connect(localapi.Admin, dataDir), Ctx: ctx,
		Interactive: st != nil && st.Mode()&os.ModeCharDevice != 0,
	}
	return Run(e, args)
}

// Run runs the command args name.
func Run(e *Env, args []string) int {
	if e.Ctx == nil {
		e.Ctx = context.Background()
	}
	// --json may also come before the command (nodehoster --json site list).
	for len(args) > 0 && (args[0] == "--json" || args[0] == "-json") {
		e.JSON, args = true, args[1:]
	}
	c, rest := find(args)
	if c == nil {
		what := "unknown command"
		if Has(args) {
			what = "incomplete command"
		}
		fmt.Fprintf(e.Stderr, "error: %s %q\n\nCommands:\n%s", what, strings.Join(args, " "), groupUsage(args))
		return ExitUsage
	}
	fs := flag.NewFlagSet(c.Name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&e.JSON, "json", e.JSON, "print the API's JSON")
	run := c.Setup(fs)
	pos, err := parseInterspersed(fs, rest)
	if errors.Is(err, flag.ErrHelp) {
		printCommandUsage(e.Stdout, c, fs)
		return ExitOK
	}
	switch {
	case err != nil:
	case len(pos) < c.MinArgs:
		err = usagef("missing arguments: expected %s", c.Args)
	case c.MaxArgs >= 0 && len(pos) > c.MaxArgs:
		err = usagef("unexpected argument %q", pos[c.MaxArgs])
	}
	if err != nil {
		fmt.Fprintf(e.Stderr, "error: %v\n", err)
		printCommandUsage(e.Stderr, c, fs)
		return ExitUsage
	}
	if err := run(e, pos); err != nil {
		var ue *usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(e.Stderr, "error: %v\n", err)
			printCommandUsage(e.Stderr, c, fs)
			return ExitUsage
		}
		fmt.Fprintf(e.Stderr, "error: %s\n", describe(err))
		return ExitError
	}
	return ExitOK
}

// groupUsage lists the commands sharing the first word of args (all of
// them when there is none).
func groupUsage(args []string) string {
	var b strings.Builder
	tw := newTabWriter(&b)
	for _, c := range Commands() {
		if len(args) == 0 || strings.Fields(c.Name)[0] == args[0] || !Has(args) {
			fmt.Fprintf(tw, "  nodehoster %s\t%s\n", strings.TrimSpace(c.Name+" "+c.Args), c.Summary)
		}
	}
	tw.Flush()
	return b.String()
}

func printCommandUsage(w io.Writer, c *Command, fs *flag.FlagSet) {
	fmt.Fprintf(w, "Usage: nodehoster %s [flags]\n\n%s\n", strings.TrimSpace(c.Name+" "+c.Args), c.Summary)
	var flags []string
	fs.VisitAll(func(f *flag.Flag) {
		name := "-" + f.Name
		if len(f.Name) > 1 {
			name = "-" + name
		}
		if _, isBool := f.Value.(interface{ IsBoolFlag() bool }); !isBool {
			name += " value"
		}
		def := ""
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
			def = " (default " + f.DefValue + ")"
		}
		flags = append(flags, fmt.Sprintf("  %s\t%s%s", name, f.Usage, def))
	})
	fmt.Fprintln(w, "\nFlags:")
	tw := newTabWriter(w)
	for _, f := range flags {
		fmt.Fprintln(tw, f)
	}
	tw.Flush()
}

// parseInterspersed parses flags wherever they are among the arguments
// (`logs shop -f` as well as `logs -f shop`); everything after "--" is
// positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, tail = args[:i], args[i+1:]
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return append(pos, tail...), nil
		}
		pos, args = append(pos, args[0]), args[1:]
	}
}

// describe turns a failure into a sentence for an operator.
func describe(err error) string {
	var ae *localapi.Error
	switch {
	case errors.Is(err, localapi.ErrNotRunning):
		return "NodeHoster is not running on this computer. Start it with: nodehoster service start"
	case errors.Is(err, os.ErrPermission):
		return "access to the NodeHoster admin pipe was denied. Run this from an elevated prompt (Run as administrator)."
	case errors.As(err, &ae):
		if ae.Field != "" {
			return ae.Field + ": " + ae.Message
		}
		return ae.Message
	}
	return err.Error()
}

// ---- output

// printJSON writes v, or raw JSON as it came from the API, indented.
func (e *Env) printJSON(v any) error {
	if raw, ok := v.(json.RawMessage); ok {
		var buf bytes.Buffer
		if err := json.Indent(&buf, raw, "", "  "); err != nil {
			return err
		}
		buf.WriteByte('\n')
		_, err := e.Stdout.Write(buf.Bytes())
		return err
	}
	enc := json.NewEncoder(e.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// get fetches path. The raw JSON is kept for --json, and decoded into out
// for the human output.
func (e *Env) get(path string, out any) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := e.Client.Get(e.Ctx, path, &raw); err != nil {
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

// post is get for a POST without a body.
func (e *Env) post(path string, out any) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := e.Client.Post(e.Ctx, path, nil, &raw); err != nil {
		return nil, err
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func newTabWriter(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 8, 2, ' ', 0) }

// table prints rows under a header, in aligned columns.
func (e *Env) table(header []string, rows [][]string) {
	tw := newTabWriter(e.Stdout)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

// printf writes a human message (nothing with --json).
func (e *Env) printf(format string, a ...any) {
	if !e.JSON {
		fmt.Fprintf(e.Stdout, format, a...)
	}
}
