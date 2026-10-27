// Command yougile-mcp is a stdio MCP server for YouGile and the commands that manage its accounts.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"

	"github.com/exomind-tmi/mcp-plugin-kit/home"
	"github.com/exomind-tmi/yougile-mcp/internal/accounts"
	"github.com/exomind-tmi/yougile-mcp/internal/tools"
	"github.com/exomind-tmi/yougile-mcp/internal/yougile"
)

// version is set by scripts/build.sh via -ldflags "-X main.version=…".
var version = "v0.0.0-dev"

const usage = `usage: yougile-mcp <command>

  stdio          MCP server on stdin/stdout (what Claude launches)
  add            log in and save a key per chosen company under an alias
  list           show the saved accounts
  remove <alias> forget an account
  version        print the version`

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, usage)
		return 2
	}
	if args[0] == "version" {
		fmt.Fprintln(out, version)
		return 0
	}
	h, err := home.Resolve("exomind-tmi", "yougile-mcp")
	if err == nil {
		err = h.Ensure()
	}
	if err == nil {
		err = dispatch(args, h, in, out, errOut)
	}
	var bad usageError
	switch {
	case errors.As(err, &bad):
		fmt.Fprintln(errOut, usage)
		return 2
	case err != nil:
		fmt.Fprintln(errOut, "yougile-mcp:", err)
		return 1
	}
	return 0
}

type usageError struct{}

func (usageError) Error() string { return "usage" }

func dispatch(args []string, h home.Home, in io.Reader, out, errOut io.Writer) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	store := accounts.Store{Path: accounts.FileIn(h)}
	switch {
	case args[0] == "stdio" && len(args) == 1:
		server := mcp.NewServer(&mcp.Implementation{Name: "yougile", Version: version},
			&mcp.ServerOptions{Instructions: tools.Instructions()})
		tools.Register(server, tools.Deps{Home: h, UploadDirs: filepath.SplitList(os.Getenv("YOUGILE_MCP_UPLOAD_DIRS"))})
		return server.Run(ctx, &mcp.StdioTransport{})
	case args[0] == "add" && len(args) == 1:
		host := os.Getenv("YOUGILE_HOST")
		if host == "" {
			host = yougile.DefaultHost
		}
		return accounts.Add(ctx, store, host, &terminal{in: bufio.NewReader(in), out: errOut}, errOut)
	case args[0] == "list" && len(args) == 1:
		return accounts.List(store, out)
	case args[0] == "remove" && len(args) == 2:
		return accounts.Remove(store, args[1], errOut)
	}
	return usageError{}
}

// terminal asks on stderr (stdout is for data) and reads the password without echo.
type terminal struct {
	in  *bufio.Reader
	out io.Writer
}

func (t *terminal) Ask(prompt string) (string, error) {
	fmt.Fprint(t.out, prompt)
	line, err := t.in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (t *terminal) Secret(prompt string) (string, error) {
	fmt.Fprint(t.out, prompt)
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		secret, err := term.ReadPassword(fd)
		fmt.Fprintln(t.out)
		return string(secret), err
	}
	return t.Ask("")
}
