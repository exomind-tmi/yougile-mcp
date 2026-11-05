package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/exomind-tmi/yougile-mcp/internal/yougile"
)

// scanLimit caps how many tasks find_tasks reads when a filter runs here rather than in YouGile.
const scanLimit = 10_000

type projectsIn struct {
	Account string `json:"account,omitempty" jsonschema:"account alias; optional when only one account is saved"`
	Title   string `json:"title,omitempty" jsonschema:"project title"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of items"`
}

func (t *server) listProjects(ctx context.Context, _ *mcp.CallToolRequest, in projectsIn) (result, any, error) {
	s, err := t.session(in.Account)
	if err != nil {
		return nil, nil, err
	}
	q := url.Values{}
	if in.Title != "" {
		q.Set("title", in.Title)
	}
	list, err := yougile.Paged[struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}](ctx, s.client, "/projects", limitOf(in.Limit, 200, 1000), q)
	if err != nil {
		return nil, nil, err
	}
	return text(list)
}

type usersIn struct {
	Account   string `json:"account,omitempty" jsonschema:"account alias; optional when only one account is saved"`
	ProjectID string `json:"projectId,omitempty" jsonschema:"only the people of this project"`
	Email     string `json:"email,omitempty"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum number of items"`
}

func (t *server) listUsers(ctx context.Context, _ *mcp.CallToolRequest, in usersIn) (result, any, error) {
	s, err := t.session(in.Account)
	if err != nil {
		return nil, nil, err
	}
	q := url.Values{}
	if in.ProjectID != "" {
		q.Set("projectId", in.ProjectID)
	}
	if in.Email != "" {
		q.Set("email", in.Email)
	}
	list, err := yougile.Paged[apiUser](ctx, s.client, "/users", limitOf(in.Limit, 200, 1000), q)
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email,omitempty"`
	}
	rows := []row{}
	for _, u := range list {
		rows = append(rows, row{u.ID, u.RealName, u.Email})
	}
	return text(rows)
}

type stickersIn struct {
	Account string `json:"account,omitempty" jsonschema:"account alias; optional when only one account is saved"`
	BoardID string `json:"boardId,omitempty" jsonschema:"only the stickers of this board"`
	Name    string `json:"name,omitempty" jsonschema:"sticker name"`
}

func (t *server) listStickers(ctx context.Context, _ *mcp.CallToolRequest, in stickersIn) (result, any, error) {
	s, err := t.session(in.Account)
	if err != nil {
		return nil, nil, err
	}
	q := url.Values{}
	if in.BoardID != "" {
		q.Set("boardId", in.BoardID)
	}
	if in.Name != "" {
		q.Set("name", in.Name)
	}
	list, err := fetchStickers(ctx, s.client, q)
	if err != nil {
		return nil, nil, err
	}
	return text(list)
}

type findIn struct {
	Account      string `json:"account,omitempty" jsonschema:"account alias; optional when only one account is saved"`
	Title        string `json:"title,omitempty" jsonschema:"text in the task title (searched by YouGile)"`
	Code         string `json:"code,omitempty" jsonschema:"task code such as DEV-123 or ID-456"`
	BoardID      string `json:"boardId,omitempty" jsonschema:"only tasks of this board"`
	ColumnID     string `json:"columnId,omitempty" jsonschema:"only tasks of this column"`
	Assignee     string `json:"assignee,omitempty" jsonschema:"user id from list_users"`
	StickerID    string `json:"stickerId,omitempty" jsonschema:"sticker id from list_stickers"`
	StickerState string `json:"stickerState,omitempty" jsonschema:"state id of stickerId"`
	StickerValue string `json:"stickerValue,omitempty" jsonschema:"text in the value of stickerId: a free field or a state name"`
	Limit        int    `json:"limit,omitempty" jsonschema:"maximum number of tasks (default 30, at most 200)"`
	Offset       int    `json:"offset,omitempty" jsonschema:"where to go on in the list: nextOffset of the previous answer; not with boardId"`
}

func (t *server) findTasks(ctx context.Context, _ *mcp.CallToolRequest, in findIn) (result, any, error) {
	if (in.StickerState != "" || in.StickerValue != "") && in.StickerID == "" {
		return nil, nil, errors.New("stickerState and stickerValue need stickerId")
	}
	if in.BoardID != "" && in.ColumnID != "" {
		return nil, nil, errors.New("give boardId or columnId, not both")
	}
	s, err := t.session(in.Account)
	if err != nil {
		return nil, nil, err
	}
	q := url.Values{"includeDeleted": {"false"}}
	for name, v := range map[string]string{"title": in.Title, "assignedTo": in.Assignee, "stickerId": in.StickerID,
		"stickerStateId": in.StickerState} {
		if v != "" {
			q.Set(name, v)
		}
	}
	columns := []string{in.ColumnID}
	if in.BoardID != "" {
		list, err := yougile.Paged[struct {
			ID string `json:"id"`
		}](ctx, s.client, "/columns", 1000, url.Values{"boardId": {in.BoardID}})
		if err != nil {
			return nil, nil, err
		}
		columns = columns[:0]
		for _, c := range list {
			columns = append(columns, c.ID)
		}
	}
	var catalog map[string]apiSticker
	if in.StickerValue != "" {
		if catalog, err = s.stickers(ctx); err != nil {
			return nil, nil, err
		}
	}
	needle := strings.ToLower(in.StickerValue)
	match := func(tk apiTask) bool {
		if in.Code != "" && !strings.EqualFold(tk.Code, in.Code) && !strings.EqualFold(tk.CommonCode, in.Code) {
			return false
		}
		if in.StickerValue != "" {
			var values []string
			switch v := tk.Stickers[in.StickerID].(type) {
			case string:
				values = []string{stateName(catalog[in.StickerID], v)}
			case []any:
				values = stateNames(catalog[in.StickerID], v)
			}
			return slices.ContainsFunc(values, func(v string) bool { return strings.Contains(strings.ToLower(v), needle) })
		}
		return true
	}
	limit := limitOf(in.Limit, 30, 200)
	scan := limit
	if in.Code != "" || in.StickerValue != "" {
		scan = scanLimit // matched here, not by YouGile
	}
	if in.Code != "" {
		limit = 1 // a code names one task
	}
	offset := max(in.Offset, 0)
	if offset > 0 && len(columns) > 1 {
		return nil, nil, errors.New("offset needs columnId, not boardId")
	}
	// at holds the position in the list after each task found, for nextOffset
	found, at, scanned, more := []apiTask{}, []int{}, 0, false
	pos := offset
	for i, column := range columns {
		if column != "" {
			q.Set("columnId", column)
		}
		left := false // the page that filled the limit had tasks after it
		rest, err := yougile.Each(ctx, s.client, "/task-list", offset, scan-scanned, q, func(page []apiTask) bool {
			for j, tk := range page {
				scanned++
				pos++
				if match(tk) {
					found, at = append(found, tk), append(at, pos)
					if len(found) == limit {
						left = j < len(page)-1
						return false
					}
				}
			}
			return true
		})
		if err != nil {
			return nil, nil, err
		}
		if len(found) == limit || scanned >= scan {
			if in.Code != "" {
				more = len(found) == 0 && (rest || i < len(columns)-1) // the code may still come
			} else {
				more = rest || left || i < len(columns)-1
			}
			break
		}
	}
	answer := taskPage{More: more, Scanned: scanned}
	var cut bool
	answer.Tasks, cut = s.views(ctx, found)
	if cut {
		answer.More = true
	}
	if answer.More && len(columns) == 1 {
		answer.NextOffset = pos
		if cut {
			answer.NextOffset = offset
			if n := len(answer.Tasks); n > 0 {
				answer.NextOffset = at[n-1]
			}
		}
	}
	switch {
	case answer.More && len(columns) > 1:
		answer.Hint = "more tasks may match: narrow the filters or search one column, which can go on with offset"
	case cut:
		answer.Hint = cutHint
	case more && scan == scanLimit && scanned >= scan:
		answer.Hint = fmt.Sprintf("%d tasks were searched: call again with offset=nextOffset, or narrow with boardId or columnId", scanLimit)
	case more:
		answer.Hint = "more tasks may match: call again with offset=nextOffset"
	}
	return text(answer)
}
