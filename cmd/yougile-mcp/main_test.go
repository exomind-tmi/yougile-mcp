package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/exomind-tmi/yougile-mcp/internal/accounts"
	"github.com/exomind-tmi/yougile-mcp/internal/fakeapi"
)

func run_(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestAccountCommandsEndToEnd(t *testing.T) {
	t.Setenv("YOUGILE_MCP_HOME", t.TempDir())
	fake := fakeapi.New()
	defer fake.Close()
	t.Setenv("YOUGILE_HOST", fake.URL)

	if code, out, _ := run_(t, ""); code != 2 || out != "" {
		t.Errorf("no command: code %d", code)
	}
	if code, out, _ := run_(t, "", "version"); code != 0 || strings.TrimSpace(out) == "" {
		t.Errorf("version: %d %q", code, out)
	}
	// login, password (read as a line when stdin is not a terminal), Enter = all, two default aliases
	code, _, errOut := run_(t, "me@example.com\nhunter2\n\n\n\n", "add")
	if code != 0 || strings.Contains(errOut, "hunter2") || strings.Contains(errOut, "issued-key") {
		t.Fatalf("add: %d %q", code, errOut)
	}
	code, out, _ := run_(t, "", "list")
	if code != 0 || !strings.Contains(out, "acme\tAcme") || !strings.Contains(out, "beta-corp\tBeta Corp") || strings.Contains(out, "issued-key") {
		t.Fatalf("list: %d %q", code, out)
	}
	if code, _, _ := run_(t, "", "remove", "acme"); code != 0 {
		t.Errorf("remove: %d", code)
	}
	if code, _, errOut := run_(t, "", "remove", "acme"); code != 1 || !strings.Contains(errOut, `no account "acme"`) {
		t.Errorf("remove again: %d %q", code, errOut)
	}
	if code, _, _ := run_(t, "", "bogus"); code != 2 {
		t.Errorf("unknown command: %d", code)
	}
	if code, _, _ := run_(t, "", "remove"); code != 2 {
		t.Errorf("remove without alias: %d", code)
	}
}

func TestRelativeHomeIsRefused(t *testing.T) {
	t.Setenv("YOUGILE_MCP_HOME", "relative/dir")
	if code, _, errOut := run_(t, "", "list"); code != 1 || !strings.Contains(errOut, "absolute path") {
		t.Errorf("%d %q", code, errOut)
	}
}

func TestTheRealBinarySpeaksMCPOverStdio(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "yougile-mcp")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	fake := fakeapi.New()
	defer fake.Close()
	state := t.TempDir()
	store := accounts.Store{Path: filepath.Join(state, "accounts.json")}
	store.Save(map[string]accounts.Account{"main": {Alias: "main", CompanyID: "c", Company: "C", Host: fake.URL, Key: fakeapi.Key}})

	cmd := exec.Command(bin, "stdio")
	cmd.Env = append(os.Environ(), "YOUGILE_MCP_HOME="+state)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_tasks","arguments":{"columnId":"c2"}}}`,
	} {
		io.WriteString(stdin, line+"\n")
	}
	var answers strings.Builder
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		answers.WriteString(scanner.Text() + "\n")
		if strings.Contains(scanner.Text(), `"id":2`) {
			break
		}
	}
	stdin.Close()
	cmd.Wait() // the process is gone, so stderr is complete and safe to read
	if !strings.Contains(answers.String(), "In column two") {
		t.Errorf("answers: %q stderr: %s", answers.String(), stderr.String())
	}
	if strings.Contains(answers.String()+stderr.String(), fakeapi.Key) {
		t.Error("the key leaked")
	}
}
