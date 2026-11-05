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
	t      *testing.T
	fake   *fakeapi.Server
	home   home.Home
	work   string
	store  accounts.Store
	server *mcp.Server
	sess   *mcp.ClientSession
	// answers is what the user puts into the forms of the server, in order; a nil entry declines
	answers []map[string]any
	asked   []*mcp.ElicitParams
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

	deps := tools.Deps{Home: e.home, Host: fake.URL, UploadDirs: []string{work}}
	if mutate != nil {
		mutate(&deps)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "yougile", Version: "test"}, &mcp.ServerOptions{Instructions: tools.Instructions()})
	tools.Register(server, deps)
	e.server = server
	e.sess = e.connect(&mcp.ClientOptions{ElicitationHandler: e.form})
	return e
}

// connect opens a client session; without an ElicitationHandler the client cannot show forms.
func (e *env) connect(opts *mcp.ClientOptions) *mcp.ClientSession {
	e.t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := e.server.Connect(ctx, serverTransport, nil); err != nil {
		e.t.Fatal(err)
	}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, opts).Connect(ctx, clientTransport, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { sess.Close() })
	return sess
}

func (e *env) form(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	e.asked = append(e.asked, req.Params)
	if len(e.answers) == 0 {
		return &mcp.ElicitResult{Action: "cancel"}, nil
	}
	answer := e.answers[0]
	e.answers = e.answers[1:]
	if answer == nil {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	return &mcp.ElicitResult{Action: "accept", Content: answer}, nil
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
	if len(got) != 16 {
		t.Fatalf("want 16 tools, got %d", len(got))
	}
	for _, name := range []string{"create_task", "update_task", "post_message", "upload_attachment", "delete_message",
		"manage_accounts"} {
		a := got[name].Annotations
		if a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
			t.Errorf("%s must be destructive: %+v", name, a)
		}
	}
	for _, name := range []string{"list_projects", "list_boards", "list_columns", "list_users",
		"list_stickers", "list_tasks", "find_tasks", "get_task", "get_messages"} {
		if a := got[name].Annotations; !a.ReadOnlyHint {
			t.Errorf("%s must be read-only", name)
		}
	}
	if a := got["download_attachment"].Annotations; a.ReadOnlyHint || *a.DestructiveHint {
		t.Errorf("download_attachment writes a local file but destroys nothing: %+v", a)
	}
	for name, tool := range got {
		schema, _ := json.Marshal(tool.InputSchema)
		if !strings.Contains(string(schema), `"account"`) {
			t.Errorf("%s: no account parameter", name)
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
	var page struct {
		More  bool
		Tasks []map[string]any
	}
	e.ok("list_tasks", map[string]any{"columnId": "c1"}, &page)
	if len(page.Tasks) != 2 || page.More {
		t.Fatalf("tasks: %+v", page)
	}
	first := page.Tasks[0]
	if first["description"] != "Button & form\nfails" || first["created"] != "2025-10-09T08:53:20Z" {
		t.Errorf("task view: %v", first)
	}
	if who := first["assigned"].([]any)[0].(map[string]any); who["id"] != "u1" || who["name"] != "Ann" {
		t.Errorf("assignee not resolved: %v", who)
	}
}

func TestPagingFollowsNextUntilTheLimit(t *testing.T) {
	e := setup(t, nil)
	var one struct {
		More  bool
		Tasks []any
	}
	e.ok("list_tasks", map[string]any{"columnId": "c1", "limit": 1}, &one)
	if len(one.Tasks) != 1 || !one.More || e.fake.Count("/task-list") != 1 {
		t.Fatalf("limit 1: %+v, %d requests", one, e.fake.Count("/task-list"))
	}
	e.ok("list_tasks", map[string]any{"columnId": "c1", "limit": 5}, nil)
	if got := e.fake.Count("/task-list"); got != 2 {
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
	var page struct {
		Messages []struct {
			From        string
			Text        string
			Attachments []string
		}
	}
	e.ok("get_messages", map[string]any{"taskId": "t1"}, &page)
	messages := page.Messages
	if len(messages) != 3 || !strings.HasPrefix(messages[0].Text, "first") || messages[1].Text != "second" {
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
	_, body := e.call("manage_accounts", map[string]any{"action": "list"})
	if !strings.Contains(body, `"account":"main"`) || strings.Contains(body, fakeapi.Key) {
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
	e.ok("update_task", map[string]any{"taskId": "t1", "description": "a\n\nb & c"}, nil)
	if got := string(e.fake.Requests("PUT")[1].Body); got != `{"description":"\u003cp\u003ea\u003c/p\u003e\u003cp\u003eb \u0026amp; c\u003c/p\u003e"}` {
		t.Errorf("description must go as HTML: %s", got)
	}
	e.ok("update_task", map[string]any{"taskId": "t1", "completed": false}, nil)
	if got := string(e.fake.Requests("PUT")[2].Body); got != `{"completed":false}` {
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

func TestUploadPostsTheFileThenAChatMessageWithItsToken(t *testing.T) {
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
	if html, sent := msg["textHtml"]; msg["text"] != "shot\n/root/#file:/files/pic.png" || !sent || html != "" {
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
	e.fails("download_attachment", map[string]any{"url": "https://evil.example/files/x.png"}, "only files on the account's YouGile site")
	e.fails("download_attachment", map[string]any{"url": e.fake.URL + ".evil/files/x.png"}, "only files on the account's YouGile site")
	if n := len(e.fake.Requests("")); n != 0 {
		t.Errorf("%d requests reached the API", n)
	}
}

func TestDownloadFollowsTheRedirectOfAFileLinkWithoutTheKey(t *testing.T) {
	e := setup(t, nil)
	var out struct{ Saved string }
	e.ok("download_attachment", map[string]any{"url": "/user-data/report.pdf"}, &out)
	if data, _ := os.ReadFile(out.Saved); string(data) != "%PDF-1.4 data" {
		t.Fatalf("saved %q", data)
	}
	for _, r := range e.fake.Requests("GET") {
		if r.Auth != "" {
			t.Errorf("%s got the key", r.Path)
		}
	}
	e.fails("download_attachment", map[string]any{"url": "/user-data/away/x.png"}, "off the account's YouGile site")
	e.ok("download_attachment", map[string]any{"url": "/user-data/Build%2520log.txt"}, &out)
	if filepath.Base(out.Saved) != "Build log.txt" {
		t.Errorf("saved as %s", out.Saved)
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

func TestTaskShowsCodesStickersDeadlineAndSubtasks(t *testing.T) {
	e := setup(t, nil)
	var task struct {
		Code, CommonCode, Start, Deadline string
		Stickers                          map[string]any
		Subtasks                          []string
	}
	e.ok("get_task", map[string]any{"taskId": "t1"}, &task)
	if task.Code != "DEV-7" || task.CommonCode != "ID-107" || task.Deadline != "2025-10-10T12:40:00Z" ||
		task.Start != "2025-10-09T08:53:20Z" || len(task.Subtasks) != 1 {
		t.Errorf("task: %+v", task)
	}
	got, _ := json.Marshal(task.Stickers)
	if string(got) != `{"Order":"ORB-42 order","Priority":"High","Sprint":["Sprint 1"]}` {
		t.Errorf("stickers: %s", got)
	}
	e.ok("get_task", map[string]any{"taskId": "t2"}, nil)
	if got := e.fake.Count("/string-stickers"); got != 1 {
		t.Errorf("stickers requested %d times", got)
	}
}

func TestFindTasks(t *testing.T) {
	e := setup(t, nil)
	type found struct {
		Tasks []struct{ ID string }
		More  bool
	}
	ids := func(f found) string {
		var out []string
		for _, tk := range f.Tasks {
			out = append(out, tk.ID)
		}
		return strings.Join(out, ",")
	}
	var f found
	e.ok("find_tasks", map[string]any{"title": "login"}, &f)
	if ids(f) != "t1,t2" || !strings.Contains(e.fake.Requests("GET")[0].Query, "title=login") {
		t.Errorf("by title: %v", f)
	}
	e.ok("find_tasks", map[string]any{"code": "dev-9"}, &f)
	if ids(f) != "t3" {
		t.Errorf("by code: %v", f)
	}
	e.ok("find_tasks", map[string]any{"code": "ID-108"}, &f)
	if ids(f) != "t2" {
		t.Errorf("by common code: %v", f)
	}
	e.ok("find_tasks", map[string]any{"stickerId": "s2", "stickerValue": "orb-43"}, &f)
	if ids(f) != "t2" {
		t.Errorf("by sticker value: %v", f)
	}
	e.ok("find_tasks", map[string]any{"stickerId": "s1", "stickerValue": "high"}, &f)
	if ids(f) != "t1" {
		t.Errorf("by state name: %v", f)
	}
	e.ok("find_tasks", map[string]any{"assignee": "u1"}, &f)
	if ids(f) != "t1" {
		t.Errorf("by assignee: %v", f)
	}
	before := e.fake.Count("/task-list")
	e.ok("find_tasks", map[string]any{"boardId": "b1"}, &f)
	if ids(f) != "t1,t2,t3" || e.fake.Count("/task-list")-before != 2 {
		t.Errorf("by board: %v", f)
	}
	e.ok("find_tasks", map[string]any{"limit": 1}, &f)
	if ids(f) != "t1" || !f.More {
		t.Errorf("limit: %v", f)
	}
	e.ok("find_tasks", map[string]any{"stickerId": "s1", "stickerState": "st1"}, &f)
	if ids(f) != "t1" || !strings.Contains(e.fake.Requests("GET")[len(e.fake.Requests("GET"))-1].Query, "stickerStateId=st1") {
		t.Errorf("by sticker state: %v", f)
	}
	e.fails("find_tasks", map[string]any{"stickerValue": "x"}, "need stickerId")
	e.fails("find_tasks", map[string]any{"boardId": "b1", "columnId": "c1"}, "not both")
}

func TestFindTasksSaysMoreOnlyWhenThereMayBeMore(t *testing.T) {
	e := setup(t, nil)
	var f struct {
		More  bool
		Hint  string
		Tasks []any
	}
	e.ok("find_tasks", map[string]any{"columnId": "c1", "limit": 2}, &f)
	if len(f.Tasks) != 2 || f.More {
		t.Errorf("exactly the limit, list read to the end: %+v", f)
	}
	e.ok("find_tasks", map[string]any{"boardId": "b1", "limit": 2}, &f)
	if len(f.Tasks) != 2 || !f.More || !strings.Contains(f.Hint, "search one column") {
		t.Errorf("a column is left: %+v", f)
	}
	e.ok("find_tasks", map[string]any{"boardId": "b1", "limit": 3}, &f)
	if len(f.Tasks) != 3 || f.More {
		t.Errorf("all read: %+v", f)
	}
}

func TestFindByCodeStopsAtTheFirstMatch(t *testing.T) {
	e := setup(t, nil)
	var f struct{ Tasks []struct{ ID string } }
	e.ok("find_tasks", map[string]any{"code": "DEV-7", "limit": 5}, &f)
	if len(f.Tasks) != 1 || f.Tasks[0].ID != "t1" || e.fake.Count("/task-list") != 1 {
		t.Errorf("tasks %v, %d requests", f.Tasks, e.fake.Count("/task-list"))
	}
}

func TestTaskIsShownWithIDsWhenNamesCannotBeRead(t *testing.T) {
	e := setup(t, nil)
	e.fake.Fail("/sprint-stickers", 403)
	e.fake.Fail("/users", 403)
	var task struct {
		Title    string
		Stickers map[string]any
		Assigned []struct{ ID, Name string }
	}
	e.ok("get_task", map[string]any{"taskId": "t1"}, &task)
	if task.Title == "" || task.Stickers["s1"] != "st1" || task.Assigned[0].Name != "u1" {
		t.Errorf("task: %+v", task)
	}
}

func TestListProjectsUsersAndStickers(t *testing.T) {
	e := setup(t, nil)
	var projects []struct{ ID, Title string }
	e.ok("list_projects", map[string]any{"title": "Pro"}, &projects)
	if len(projects) != 1 || !strings.Contains(e.fake.Requests("GET")[0].Query, "title=Pro") {
		t.Errorf("projects: %v", projects)
	}
	var users []struct{ ID, Name, Email string }
	e.ok("list_users", map[string]any{"projectId": "p1"}, &users)
	if len(users) != 2 || users[0].Name != "Ann" || !strings.Contains(e.fake.Requests("GET")[1].Query, "projectId=p1") {
		t.Errorf("users: %v", users)
	}
	var stickers []struct {
		ID, Name, Kind string
		States         []struct{ ID, Name string }
	}
	e.ok("list_stickers", map[string]any{"boardId": "b1"}, &stickers)
	if len(stickers) != 3 || stickers[0].Kind != "string" || stickers[1].States == nil || stickers[2].Kind != "sprint" {
		t.Errorf("stickers: %+v", stickers)
	}
}

func TestCreateAndUpdateSendAssigneesAndStickers(t *testing.T) {
	e := setup(t, nil)
	e.ok("create_task", map[string]any{"title": "T", "columnId": "c1", "assigned": []string{"u1"},
		"stickers": map[string]any{"s1": "st1", "s3": []string{"sp1"}}}, nil)
	if got := string(e.fake.Requests("POST")[0].Body); got != `{"assigned":["u1"],"columnId":"c1","stickers":{"s1":"st1","s3":["sp1"]},"title":"T"}` {
		t.Errorf("create body: %s", got)
	}
	e.ok("update_task", map[string]any{"taskId": "t1", "assigned": []string{}}, nil)
	if got := string(e.fake.Requests("PUT")[0].Body); got != `{"assigned":[]}` {
		t.Errorf("[] must clear the assignees: %s", got)
	}
	e.ok("update_task", map[string]any{"taskId": "t1", "stickers": map[string]any{"s2": "-"}}, nil)
	if got := string(e.fake.Requests("PUT")[1].Body); got != `{"stickers":{"s2":"-"}}` {
		t.Errorf("update body: %s", got)
	}
	e.fails("update_task", map[string]any{"taskId": "t1", "stickers": map[string]any{"s1": 5}}, "sticker s1")
	if n := len(e.fake.Requests("PUT")); n != 2 {
		t.Errorf("a bad sticker must not be sent: %d PUTs", n)
	}
}

func TestMessagesComeInPages(t *testing.T) {
	e := setup(t, nil)
	var page struct {
		Messages   []struct{ ID int64 }
		Next       bool
		NextOffset int
	}
	e.ok("get_messages", map[string]any{"taskId": "t1", "limit": 2}, &page)
	if len(page.Messages) != 2 || !page.Next || page.NextOffset != 2 {
		t.Fatalf("first page: %+v", page)
	}
	page.NextOffset = 0
	e.ok("get_messages", map[string]any{"taskId": "t1", "limit": 2, "offset": 2}, &page)
	if len(page.Messages) != 1 || page.Next || page.NextOffset != 0 {
		t.Errorf("second page (the deleted one is skipped): %+v", page)
	}
	_, body := e.call("get_messages", map[string]any{"taskId": "t1", "limit": 1})
	if !strings.HasPrefix(body, `{"next":true,"nextOffset":1,`) {
		t.Errorf("paging must come first, a cut answer keeps it: %.80s", body)
	}
	e.ok("get_messages", map[string]any{"taskId": "t1", "text": "mine"}, nil)
	if q := e.fake.Requests("GET"); !strings.Contains(q[len(q)-1].Query, "text=mine") {
		t.Errorf("text filter not sent: %v", q)
	}
}

func TestDeleteMessageOnlyOwn(t *testing.T) {
	e := setup(t, nil)
	e.fails("delete_message", map[string]any{"taskId": "t1", "messageId": 1760000001000}, "someone else")
	if n := len(e.fake.Requests("PUT")); n != 0 {
		t.Fatalf("a message of someone else was changed: %d PUTs", n)
	}
	e.ok("delete_message", map[string]any{"taskId": "t1", "messageId": 1760000004000}, nil)
	put := e.fake.Requests("PUT")
	if len(put) != 1 || put[0].Path != "/api-v2/chats/t1/messages/1760000004000" || string(put[0].Body) != `{"deleted":true}` {
		t.Errorf("delete: %+v", put)
	}
	if got := e.fake.Count("/users/me"); got != 1 {
		t.Errorf("me requested %d times", got)
	}
}

func TestPostMessageReturnsTheID(t *testing.T) {
	e := setup(t, nil)
	var out struct{ ID int64 }
	e.ok("post_message", map[string]any{"taskId": "t1", "text": "hi"}, &out)
	if out.ID != 1 {
		t.Errorf("id: %d", out.ID)
	}
}

func TestAddAccountAsksForTheLoginInAFormAndSavesTheNamedCompany(t *testing.T) {
	e := setup(t, nil)
	e.answers = []map[string]any{{"login": "me@example.com", "password": "pw"}}
	var out []struct{ Status, Account, Company string }
	e.ok("manage_accounts", map[string]any{"action": "add", "company": "beta"}, &out)
	if len(out) != 1 || out[0].Status != "added" || out[0].Account != "beta-corp" || out[0].Company != "Beta Corp" {
		t.Fatalf("got %+v", out)
	}
	all, _ := e.store.Load()
	if a := all["beta-corp"]; a.Key != "issued-key" || a.CompanyID != "co2" || a.Host != e.fake.URL {
		t.Errorf("saved %+v", a)
	}
	if len(e.asked) != 1 || !strings.Contains(e.asked[0].Message, "not stored") {
		t.Errorf("forms: %+v", e.asked)
	}
	raw, _ := os.ReadFile(e.store.Path)
	if strings.Contains(string(raw), `"pw"`) {
		t.Error("the password was stored")
	}
	_, body := e.call("manage_accounts", map[string]any{"action": "list"})
	if !strings.Contains(body, "beta-corp") {
		t.Errorf("list: %s", body)
	}
}

func TestAddAccountWithoutACompanySavesEveryCompanyAndAgainReportsThemAsThere(t *testing.T) {
	e := setup(t, nil)
	type row struct{ Status, Account string }
	var out []row
	e.answers = []map[string]any{{"login": "me", "password": "pw"}}
	e.ok("manage_accounts", map[string]any{"action": "add"}, &out)
	if len(out) != 2 || out[0] != (row{"added", "acme"}) || out[1] != (row{"added", "beta-corp"}) {
		t.Errorf("first: %+v", out)
	}
	e.answers = []map[string]any{{"login": "me", "password": "pw"}}
	e.ok("manage_accounts", map[string]any{"action": "add"}, &out)
	if len(out) != 2 || out[0] != (row{"exists", "acme"}) || out[1] != (row{"exists", "beta-corp"}) {
		t.Errorf("again: %+v", out)
	}
	e.answers = []map[string]any{{"login": "me", "password": "pw"}}
	e.fails("manage_accounts", map[string]any{"action": "add", "account": "one"}, "pass company as well (Acme, Beta Corp)")
}

func TestAddAccountTakesTheGivenAliasAndReportsATakenOne(t *testing.T) {
	e := setup(t, nil)
	var out []struct{ Status, Account, Error string }
	e.answers = []map[string]any{{"login": "me", "password": "pw"}}
	e.ok("manage_accounts", map[string]any{"action": "add", "account": "acme2", "company": "Acme"}, &out)
	if len(out) != 1 || out[0].Status != "added" || out[0].Account != "acme2" {
		t.Errorf("added: %+v", out)
	}
	e.answers = []map[string]any{{"login": "me", "password": "pw"}}
	e.ok("manage_accounts", map[string]any{"action": "add", "account": "acme2", "company": "Beta"}, &out)
	if len(out) != 1 || out[0].Status != "failed" || !strings.Contains(out[0].Error, `alias "acme2" exists`) {
		t.Errorf("taken: %+v", out)
	}
	e.answers = []map[string]any{{"login": "me", "password": "pw"}}
	e.fails("manage_accounts", map[string]any{"action": "add", "company": "Zeta"}, "Acme, Beta Corp")
}

func TestAddAccountSendsThePasswordOnlyToYouGile(t *testing.T) {
	e := setup(t, nil)
	e.answers = []map[string]any{{"login": "me", "password": "pw"}}
	e.fails("manage_accounts", map[string]any{"action": "add", "host": "https://yougile-login.example"}, "host must be")
	if len(e.asked) != 0 {
		t.Error("the form was shown for a foreign host")
	}
}

func TestAddAccountDeclinedSavesNothing(t *testing.T) {
	e := setup(t, nil)
	e.answers = []map[string]any{nil}
	var out []struct{ Status string }
	e.ok("manage_accounts", map[string]any{"action": "add"}, &out)
	all, _ := e.store.Load()
	if len(out) != 1 || out[0].Status != "declined" || len(all) != 1 || e.fake.Count("/auth/companies") != 0 {
		t.Errorf("out %v, %d accounts, %d logins", out, len(all), e.fake.Count("/auth/companies"))
	}
}

func TestAddAccountWithoutFormsPointsToTheTerminal(t *testing.T) {
	e := setup(t, nil)
	e.sess = e.connect(nil)
	e.fails("manage_accounts", map[string]any{"action": "add"}, "add` in a terminal")
}

func TestManageAccountsNeedsAKnownAction(t *testing.T) {
	e := setup(t, nil)
	e.fails("manage_accounts", map[string]any{"action": "drop"}, "use list, add or remove")
}

func TestRemoveAccount(t *testing.T) {
	e := setup(t, nil)
	e.saveAccount("second", fakeapi.Key)
	e.ok("manage_accounts", map[string]any{"action": "remove", "account": "second"}, nil)
	all, _ := e.store.Load()
	if _, ok := all["second"]; ok || len(all) != 1 {
		t.Errorf("accounts: %v", all)
	}
	e.fails("manage_accounts", map[string]any{"action": "remove", "account": "second"}, `no account "second"`)
}

func TestListsCutTheDescriptionGetTaskGivesItWhole(t *testing.T) {
	e := setup(t, nil)
	var list struct {
		Tasks []struct{ Description string }
	}
	e.ok("list_tasks", map[string]any{"columnId": "c2"}, &list)
	if d := list.Tasks[0].Description; len([]rune(d)) > 340 || !strings.Contains(d, "[truncated, 499 chars]") {
		t.Errorf("list description: %d runes %.40q", len([]rune(d)), d)
	}
	var task struct{ Description string }
	e.ok("get_task", map[string]any{"taskId": "t3"}, &task)
	if len(task.Description) != 499 {
		t.Errorf("get_task description: %d", len(task.Description))
	}
}

func TestAListStopsAtItsSizeBudgetAndSaysSo(t *testing.T) {
	e := setup(t, nil)
	e.fake.ManyTasks("c9", 300, strings.Repeat("я", 300))
	var page struct {
		More       bool
		Hint       string
		NextOffset int
		Tasks      []any
	}
	_, body := e.call("list_tasks", map[string]any{"columnId": "c9", "limit": 200})
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("not JSON (%v): %.80s", err, body)
	}
	if !page.More || !strings.Contains(page.Hint, "size limit") || len(page.Tasks) == 0 || len(page.Tasks) == 200 || len(body) > 90_000 {
		t.Errorf("more=%v hint=%q tasks=%d bytes=%d", page.More, page.Hint, len(page.Tasks), len(body))
	}
	if page.NextOffset != len(page.Tasks) {
		t.Errorf("a cut list must go on after its last task: next=%d, tasks=%d", page.NextOffset, len(page.Tasks))
	}
	page.NextOffset = 0
	e.ok("find_tasks", map[string]any{"columnId": "c9", "limit": 200}, &page)
	if !page.More || page.NextOffset != len(page.Tasks) {
		t.Errorf("find cut: next=%d, tasks=%d", page.NextOffset, len(page.Tasks))
	}
	page.NextOffset = 0
	e.fake.ManyTasks("c2", 300, strings.Repeat("я", 300))
	e.ok("find_tasks", map[string]any{"boardId": "b1", "limit": 200}, &page)
	if !page.More || page.NextOffset != 0 || !strings.Contains(page.Hint, "search one column") {
		t.Errorf("board cut: next=%d hint=%q", page.NextOffset, page.Hint)
	}
}

func TestUploadTakesARelativePathFromTheWorkingDirectory(t *testing.T) {
	e := setup(t, nil)
	t.Chdir(e.work)
	if err := os.WriteFile(filepath.Join(e.work, "note.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out struct{ Uploaded []string }
	e.ok("upload_attachment", map[string]any{"taskId": "t1", "files": []string{"note.txt"}}, &out)
	if len(out.Uploaded) != 1 {
		t.Errorf("uploaded %v", out.Uploaded)
	}
}

func TestUploadNeverTakesTheStateFolderWithTheKeys(t *testing.T) {
	e := setup(t, func(d *tools.Deps) { d.UploadDirs = []string{filepath.Dir(d.Home.Dir)} })
	e.fails("upload_attachment", map[string]any{"taskId": "t1", "files": []string{e.store.Path}}, "holds the keys")
	if e.fake.Count("/upload-file") != 0 {
		t.Error("the keys were uploaded")
	}
}

func TestListTasksGoesOnWithNextOffset(t *testing.T) {
	e := setup(t, nil)
	e.fake.ManyTasks("c9", 120, "short")
	var page struct {
		More       bool
		NextOffset int
		Tasks      []struct{ ID string }
	}
	e.ok("list_tasks", map[string]any{"columnId": "c9", "limit": 50}, &page)
	if len(page.Tasks) != 50 || !page.More || page.NextOffset != 50 {
		t.Fatalf("first: more=%v next=%d tasks=%d", page.More, page.NextOffset, len(page.Tasks))
	}
	page.NextOffset = 0
	e.ok("list_tasks", map[string]any{"columnId": "c9", "limit": 50, "offset": 100}, &page)
	if len(page.Tasks) != 20 || page.More || page.NextOffset != 0 || page.Tasks[0].ID != "m100" {
		t.Errorf("last: more=%v next=%d tasks=%d", page.More, page.NextOffset, len(page.Tasks))
	}
}

func TestFindTasksGoesOnWithNextOffsetInOneList(t *testing.T) {
	e := setup(t, nil)
	var f struct {
		More       bool
		NextOffset int
		Tasks      []struct{ ID string }
	}
	e.ok("find_tasks", map[string]any{"title": "login", "limit": 1}, &f)
	if len(f.Tasks) != 1 || f.Tasks[0].ID != "t1" || !f.More || f.NextOffset != 1 {
		t.Fatalf("first: %+v", f)
	}
	f.NextOffset = 0
	e.ok("find_tasks", map[string]any{"title": "login", "limit": 1, "offset": 1}, &f)
	if len(f.Tasks) != 1 || f.Tasks[0].ID != "t2" || f.More {
		t.Errorf("second: %+v", f)
	}
	e.fails("find_tasks", map[string]any{"boardId": "b1", "offset": 2}, "offset needs columnId")
}

func TestFindByCodeStopsAtTheMatchAndSaysNoMore(t *testing.T) {
	e := setup(t, nil)
	e.fake.ManyTasks("c1", 1500, "")
	var f struct {
		More  bool
		Tasks []struct{ ID string }
	}
	e.ok("find_tasks", map[string]any{"code": "DEV-8"}, &f)
	if len(f.Tasks) != 1 || f.Tasks[0].ID != "t2" || f.More || e.fake.Count("/task-list") != 1 {
		t.Errorf("tasks %v more=%v, %d requests of two pages", f.Tasks, f.More, e.fake.Count("/task-list"))
	}
}

func TestFindByCodeSaysWhereItStoppedAtTheScanCap(t *testing.T) {
	e := setup(t, nil)
	e.fake.ManyTasks("c1", 10_050, "")
	var f struct {
		More       bool
		Hint       string
		Scanned    int
		NextOffset int
	}
	e.ok("find_tasks", map[string]any{"code": "NOPE-1"}, &f)
	if !f.More || f.Scanned != 10_000 || f.NextOffset != 10_000 || !strings.Contains(f.Hint, "10000 tasks were searched") {
		t.Errorf("%+v", f)
	}
}

func TestNamesAreAskedForOncePerAnswerEvenWhenTheyFail(t *testing.T) {
	e := setup(t, nil)
	e.fake.Fail("/users", 403, 403, 403)
	e.ok("list_tasks", map[string]any{"columnId": "c1"}, nil)
	if got := e.fake.Count("/users"); got != 1 {
		t.Errorf("users asked %d times for one answer", got)
	}
}
