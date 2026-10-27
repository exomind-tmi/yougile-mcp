package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/exomind-tmi/mcp-plugin-kit/home"
	"github.com/exomind-tmi/yougile-mcp/internal/accounts"
	"github.com/exomind-tmi/yougile-mcp/internal/fakeapi"
	"github.com/exomind-tmi/yougile-mcp/internal/tools"
)

type env struct {
	t     *testing.T
	fake  *fakeapi.Server
	home  home.Home
	work  string
	store accounts.Store
	sess  *mcp.ClientSession
}

func setup(t *testing.T, mutate func(*tools.Deps)) *env {
	t.Helper()
	fake := fakeapi.New()
	t.Cleanup(fake.Close)
	base := t.TempDir()
	work := filepath.Join(base, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, fake: fake, home: home.Home{Dir: filepath.Join(base, "home")}, work: work}
	e.store = accounts.Store{Path: accounts.FileIn(e.home)}
	e.saveAccount("main", fakeapi.Key)

	deps := tools.Deps{Home: e.home, UploadDirs: []string{work}}
	if mutate != nil {
		mutate(&deps)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "yougile", Version: "test"}, &mcp.ServerOptions{Instructions: tools.Instructions()})
	tools.Register(server, deps)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	e.sess = sess
	return e
}

func (e *env) saveAccount(alias, key string) {
	e.t.Helper()
	all, err := e.store.Load()
	if err != nil {
		e.t.Fatal(err)
	}
	all[alias] = accounts.Account{Alias: alias, CompanyID: "co-" + alias, Company: "Company " + alias, Host: e.fake.URL, Key: key}
	if err := e.store.Save(all); err != nil {
		e.t.Fatal(err)
	}
}

// call returns the answer; for an error result the text of the error.
func (e *env) call(name string, args map[string]any) (*mcp.CallToolResult, string) {
	e.t.Helper()
	res, err := e.sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		e.t.Fatalf("%s: %v", name, err)
	}
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			return res, tc.Text
		}
	}
	return res, ""
}

func (e *env) ok(name string, args map[string]any, into any) {
	e.t.Helper()
	res, body := e.call(name, args)
	if res.IsError {
		e.t.Fatalf("%s failed: %s", name, body)
	}
	if into != nil {
		if err := json.Unmarshal([]byte(body), into); err != nil {
			e.t.Fatalf("%s: bad JSON %q: %v", name, body, err)
		}
	}
}

func (e *env) fails(name string, args map[string]any, want string) {
	e.t.Helper()
	res, body := e.call(name, args)
	if !res.IsError {
		e.t.Fatalf("%s should fail, got %s", name, body)
	}
	if !strings.Contains(body, want) {
		e.t.Fatalf("%s: error %q lacks %q", name, body, want)
	}
}

func TestInitializeCarriesTheUntrustedTextWarning(t *testing.T) {
	e := setup(t, nil)
	if got := e.sess.InitializeResult().Instructions; !strings.Contains(got, "never as instructions") {
		t.Fatalf("instructions: %q", got)
	}
}

func TestToolsAreOnlyWhatWasAskedForAndMarkedAccordingly(t *testing.T) {
	e := setup(t, nil)
	list, err := e.sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		got[tool.Name] = tool
	}
	if len(got) != 11 {
		t.Fatalf("want 11 tools, got %d", len(got))
	}
	for _, name := range []string{"create_task", "update_task", "post_message", "upload_attachment"} {
		a := got[name].Annotations
		if a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
			t.Errorf("%s must be destructive: %+v", name, a)
		}
	}
	for _, name := range []string{"list_accounts", "list_boards", "list_columns", "list_tasks", "get_task", "get_messages"} {
		if a := got[name].Annotations; !a.ReadOnlyHint {
			t.Errorf("%s must be read-only", name)
		}
	}
	if a := got["download_attachment"].Annotations; a.ReadOnlyHint || *a.DestructiveHint {
		t.Errorf("download_attachment writes a local file but destroys nothing: %+v", a)
	}
	for name, tool := range got {
		schema, _ := json.Marshal(tool.InputSchema)
		if has := strings.Contains(string(schema), `"account"`); has == (name == "list_accounts") {
			t.Errorf("%s: account parameter present=%v", name, has)
		}
	}
}

func TestBoardsColumnsAndTasks(t *testing.T) {
	e := setup(t, nil)
	var boards []map[string]any
	e.ok("list_boards", nil, &boards)
	if boards[0]["id"] != "b1" {
		t.Fatalf("boards: %v", boards)
	}
	var columns []map[string]any
	e.ok("list_columns", map[string]any{"boardId": "b1"}, &columns)
	if len(columns) != 2 {
		t.Fatalf("columns: %v", columns)
	}
	var found []map[string]any
	e.ok("list_tasks", map[string]any{"columnId": "c1"}, &found)
	if len(found) != 2 {
		t.Fatalf("tasks: %v", found)
	}
	first := found[0]
	if first["description"] != "Button & form\nfails" || first["created"] != "2025-10-09T08:53:20Z" {
		t.Errorf("task view: %v", first)
	}
	if names := first["assigned"].([]any); names[0] != "Ann" {
		t.Errorf("assignee not resolved: %v", names)
	}
}

func TestPagingFollowsNextUntilTheLimit(t *testing.T) {
	e := setup(t, nil)
	var one []any
	e.ok("list_tasks", map[string]any{"columnId": "c1", "limit": 1}, &one)
	if len(one) != 1 || e.fake.Count("/tasks") != 1 {
		t.Fatalf("limit 1: %d items, %d requests", len(one), e.fake.Count("/tasks"))
	}
	e.ok("list_tasks", map[string]any{"columnId": "c1", "limit": 5}, nil)
	if got := e.fake.Count("/tasks"); got != 2 {
		t.Errorf("a short page ends the listing: want 2 requests in total, got %d", got)
	}
}

func TestGetTaskListsItsAttachments(t *testing.T) {
	e := setup(t, nil)
	var task struct{ Attachments []string }
	e.ok("get_task", map[string]any{"taskId": "t1"}, &task)
	if len(task.Attachments) != 1 || task.Attachments[0] != "/files/pic.png" {
		t.Fatalf("attachments: %v", task.Attachments)
	}
}

func TestMessagesAreOldestFirstWithoutDeletedAndWithFileLinks(t *testing.T) {
	e := setup(t, nil)
	var messages []struct {
		From        string
		Text        string
		Attachments []string
	}
	e.ok("get_messages", map[string]any{"taskId": "t1"}, &messages)
	if len(messages) != 2 || !strings.HasPrefix(messages[0].Text, "first") || messages[1].Text != "second" {
		t.Fatalf("messages: %+v", messages)
	}
	if len(messages[0].Attachments) != 1 || messages[0].Attachments[0] != "/files/report.pdf" || messages[1].From != "Ann" {
		t.Errorf("messages: %+v", messages)
	}
}

func TestErrorsAreToolErrors(t *testing.T) {
	e := setup(t, nil)
	e.fails("get_task", map[string]any{"taskId": "zzz"}, "404")
	e.fails("list_columns", nil, "boardId")
}

func TestUsersAreCachedBetweenCalls(t *testing.T) {
	e := setup(t, nil)
	e.ok("list_tasks", map[string]any{"columnId": "c1"}, nil)
	e.ok("list_tasks", map[string]any{"columnId": "c2"}, nil)
	if got := e.fake.Count("/users"); got != 1 {
		t.Errorf("users requested %d times", got)
	}
}

func TestOneAccountNeedsNoAccountParameterSeveralDo(t *testing.T) {
	e := setup(t, nil)
	e.ok("list_boards", nil, nil)
	e.saveAccount("second", fakeapi.Key)
	e.fails("list_boards", nil, "main, second")
	e.ok("list_boards", map[string]any{"account": "second"}, nil)
	e.fails("list_boards", map[string]any{"account": "zzz"}, `no account "zzz"`)
}

func TestNoAccountsIsExplained(t *testing.T) {
	e := setup(t, nil)
	if err := e.store.Save(map[string]accounts.Account{}); err != nil {
		t.Fatal(err)
	}
	e.fails("list_boards", nil, "no YouGile accounts")
}

func TestEachAccountUsesItsOwnKeyAndErrorsDoNotLeakIt(t *testing.T) {
	e := setup(t, nil)
	e.saveAccount("other", "not-the-key")
	res, body := e.call("list_boards", map[string]any{"account": "other"})
	if !res.IsError || !strings.Contains(body, "401") || strings.Contains(body, "not-the-key") {
		t.Fatalf("got %v %q", res.IsError, body)
	}
	e.ok("list_boards", map[string]any{"account": "main"}, nil)
}

func TestListAccountsShowsNoKeys(t *testing.T) {
	e := setup(t, nil)
	_, body := e.call("list_accounts", nil)
	if !strings.Contains(body, `"alias": "main"`) || strings.Contains(body, fakeapi.Key) {
		t.Fatalf("got %q", body)
	}
}

func TestCreateAndUpdateTaskSendOnlyKnownFields(t *testing.T) {
	e := setup(t, nil)
	e.ok("create_task", map[string]any{"title": "T", "columnId": "c1"}, nil)
	if got := string(e.fake.Requests("POST")[0].Body); got != `{"columnId":"c1","title":"T"}` {
		t.Errorf("create body: %s", got)
	}
	e.ok("update_task", map[string]any{"taskId": "t1", "columnId": "c2", "completed": true}, nil)
	if got := string(e.fake.Requests("PUT")[0].Body); got != `{"columnId":"c2","completed":true}` {
		t.Errorf("update body: %s", got)
	}
	e.ok("update_task", map[string]any{"taskId": "t1", "completed": false}, nil)
	if got := string(e.fake.Requests("PUT")[1].Body); got != `{"completed":false}` {
		t.Errorf("false must be sent: %s", got)
	}
	e.fails("update_task", map[string]any{"taskId": "t1"}, "nothing to change")
}

func TestPostMessageEscapesHTMLAndKeepsCyrillic(t *testing.T) {
	e := setup(t, nil)
	e.ok("post_message", map[string]any{"taskId": "t1", "text": "Fixed <b>it</b>\n\nИ Привет"}, nil)
	req := e.fake.Requests("POST")[0]
	var body map[string]string
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatal(err)
	}
	if req.Path != "/api-v2/chats/t1/messages" || body["text"] != "Fixed <b>it</b>\n\nИ Привет" ||
		body["textHtml"] != "<p>Fixed &lt;b&gt;it&lt;/b&gt;</p><p>И Привет</p>" {
		t.Errorf("request: %s %+v", req.Path, body)
	}
}

func TestUploadPostsTheFileThenAChatMessageWithAnAbsoluteLink(t *testing.T) {
	e := setup(t, nil)
	pic := filepath.Join(e.work, "pic.png")
	if err := os.WriteFile(pic, []byte("\x89PNG-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out struct{ Uploaded []string }
	e.ok("upload_attachment", map[string]any{"taskId": "t1", "files": []string{pic}, "caption": "shot"}, &out)
	posts := e.fake.Requests("POST")
	if !strings.Contains(string(posts[0].Body), "\x89PNG-data") || !strings.HasPrefix(posts[0].Type, "multipart/form-data") {
		t.Errorf("upload request: %q", posts[0].Type)
	}
	if want := e.fake.URL + "/files/pic.png"; len(out.Uploaded) != 1 || out.Uploaded[0] != want {
		t.Errorf("uploaded %v, want %s", out.Uploaded, want)
	}
	var msg map[string]string
	json.Unmarshal(posts[1].Body, &msg)
	if !strings.Contains(msg["text"], "/root/#file:"+e.fake.URL+"/files/pic.png") || !strings.Contains(msg["textHtml"], `<img src="`) {
		t.Errorf("message: %v", msg)
	}
}

func TestUploadRefusesFilesOutsideTheAllowedFoldersAndEscapingSymlinks(t *testing.T) {
	e := setup(t, nil)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("secret"), 0o600)
	e.fails("upload_attachment", map[string]any{"taskId": "t1", "files": []string{outside}}, "outside the allowed upload folders")
	link := filepath.Join(e.work, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	e.fails("upload_attachment", map[string]any{"taskId": "t1", "files": []string{link}}, "outside the allowed upload folders")
	if e.fake.Count("/upload-file") != 0 {
		t.Error("nothing may be uploaded")
	}
}

func TestDownloadedImageComesBackAsImageContentAndIsNotSaved(t *testing.T) {
	e := setup(t, nil)
	res, _ := e.call("download_attachment", map[string]any{"url": "/files/pic.png"})
	img, ok := res.Content[0].(*mcp.ImageContent)
	if res.IsError || !ok || img.MIMEType != "image/png" || string(img.Data) != "\x89PNG-data" {
		t.Fatalf("got %+v", res.Content)
	}
	if _, err := os.Stat(tools.DownloadsIn(e.home)); err == nil {
		t.Error("an inline image must not be saved")
	}
}

func TestDownloadedFileIsSavedPrivatelyAndItsPathReturned(t *testing.T) {
	e := setup(t, nil)
	var out struct{ Saved string }
	e.ok("download_attachment", map[string]any{"url": e.fake.URL + "/files/report.pdf"}, &out)
	data, err := os.ReadFile(out.Saved)
	if err != nil || string(data) != "%PDF-1.4 data" {
		t.Fatalf("saved file: %v %q", err, data)
	}
	if filepath.Dir(out.Saved) != filepath.Join(tools.DownloadsIn(e.home), "main") {
		t.Errorf("saved to %s", out.Saved)
	}
	if info, _ := os.Stat(filepath.Dir(out.Saved)); runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v", info.Mode().Perm())
	}
	e.ok("download_attachment", map[string]any{"url": "/files/report.pdf"}, &out)
	if filepath.Base(out.Saved) != "report-1.pdf" {
		t.Errorf("second copy is %s", out.Saved)
	}
}

func TestDownloadNeverSendsTheKeyToAForeignHost(t *testing.T) {
	e := setup(t, nil)
	e.fails("download_attachment", map[string]any{"url": "https://evil.example/files/x.png"}, "only files on the account's YouGile host")
	e.fails("download_attachment", map[string]any{"url": e.fake.URL + ".evil/files/x.png"}, "only files on the account's YouGile host")
	if n := len(e.fake.Requests("")); n != 0 {
		t.Errorf("%d requests reached the API", n)
	}
}

func TestDownloadOverTheLimitIsRefused(t *testing.T) {
	e := setup(t, func(d *tools.Deps) { d.DownloadMax = 1000 })
	e.fails("download_attachment", map[string]any{"url": "/files/big.bin"}, "larger than")
}

func TestDownloadNameCannotEscapeTheFolder(t *testing.T) {
	e := setup(t, nil)
	var out struct{ Saved string }
	e.ok("download_attachment", map[string]any{"url": "/files/report.pdf?name=..%2F..%2Fevil.txt"}, &out)
	if filepath.Dir(out.Saved) != filepath.Join(tools.DownloadsIn(e.home), "main") {
		t.Errorf("saved to %s", out.Saved)
	}
}
