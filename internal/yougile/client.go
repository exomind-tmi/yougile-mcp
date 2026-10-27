// Package yougile is a small client of the YouGile REST API v2: key auth, paging, retries,
// multipart upload and download. It keeps no state.
package yougile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultHost serves the API; https://ru.yougile.com works too.
	DefaultHost = "https://yougile.com"
	retries     = 3
)

// pageSize is how many items one list request asks for.
var pageSize = 1000

// APIError is safe to show to the model: it never carries the key.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// Client talks to one YouGile host with one key (empty for the login calls).
type Client struct {
	base, origin, key string
	http              *http.Client
	Sleep             func(time.Duration)
}

// New checks the host (https, or loopback for tests) and makes a client.
func New(host, key string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(host, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("YouGile host %q is not a valid address", host)
	}
	if u.Scheme != "https" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
		return nil, errors.New("YouGile host must be an https:// address")
	}
	origin := u.Scheme + "://" + u.Host
	return &Client{
		base: origin + "/api-v2", origin: origin, key: key, Sleep: time.Sleep,
		http: &http.Client{
			Timeout: 60 * time.Second,
			// the key must not follow a redirect to another host
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *Client) do(ctx context.Context, method, rawURL string, body []byte, contentType string, limit int64) ([]byte, http.Header, error) {
	var last error
	for attempt := 0; attempt <= retries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Accept", "application/json")
		if c.key != "" {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			last = fmt.Errorf("YouGile is unreachable: %v", err)
			c.Sleep(backoff(attempt, ""))
			continue
		}
		data, readErr := readLimited(resp.Body, limit)
		resp.Body.Close()
		switch {
		case resp.StatusCode == 429 || resp.StatusCode >= 500:
			last = describe(resp.StatusCode, data)
			if attempt < retries {
				c.Sleep(backoff(attempt, resp.Header.Get("Retry-After")))
				continue
			}
			return nil, nil, last
		case resp.StatusCode >= 300:
			return nil, nil, describe(resp.StatusCode, data)
		case readErr != nil:
			return nil, nil, readErr
		}
		return data, resp.Header, nil
	}
	return nil, nil, last
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r)
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, fmt.Errorf("the file is larger than %d MB", limit/1_000_000)
	}
	return data, err
}

func backoff(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.ParseFloat(retryAfter, 64); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, 30) * float64(time.Second))
	}
	return time.Duration(1<<attempt) * 100 * time.Millisecond
}

func describe(status int, body []byte) *APIError {
	hints := map[int]string{
		401: " (the key is invalid or expired: remove the account and add it again with `yougile-mcp add`)",
		403: " (the key's user has no access to this object)",
		404: " (not found)", 429: " (rate limited)",
	}
	msg := fmt.Sprintf("YouGile answered HTTP %d%s", status, hints[status])
	if status >= 300 && status < 400 {
		msg = fmt.Sprintf("YouGile answered a redirect (HTTP %d), which is not followed", status)
	}
	var detail struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &detail) == nil && detail.Error != "" {
		msg += ": " + detail.Error
	}
	return &APIError{Status: status, Message: msg}
}

// Call sends a JSON request to an API path and decodes the JSON answer into out (may be nil).
func (c *Client) Call(ctx context.Context, method, path string, query url.Values, in, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	data, _, err := c.do(ctx, method, u, body, ifNotEmpty(in != nil, "application/json"), 0)
	if err != nil || out == nil || len(data) == 0 {
		return err
	}
	return json.Unmarshal(data, out)
}

func ifNotEmpty(ok bool, s string) string {
	if ok {
		return s
	}
	return ""
}

// Paged reads up to limit items of a list endpoint, following offset paging.
func Paged[T any](ctx context.Context, c *Client, path string, limit int, query url.Values) ([]T, error) {
	var items []T
	for len(items) < limit {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("limit", strconv.Itoa(min(pageSize, limit-len(items))))
		q.Set("offset", strconv.Itoa(len(items)))
		var page struct {
			Content []T `json:"content"`
			Paging  struct {
				Next bool `json:"next"`
			} `json:"paging"`
		}
		if err := c.Call(ctx, http.MethodGet, path, q, nil, &page); err != nil {
			return nil, err
		}
		items = append(items, page.Content...)
		if len(page.Content) == 0 || !page.Paging.Next {
			break
		}
	}
	return items, nil
}

// Upload stores a file and returns its absolute address.
func (c *Client) Upload(ctx context.Context, name, mime string, data []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, strings.ReplaceAll(name, `"`, "")))
	h.Set("Content-Type", mime)
	part, err := w.CreatePart(h)
	if err != nil {
		return "", err
	}
	part.Write(data)
	w.Close()
	raw, _, err := c.do(ctx, http.MethodPost, c.base+"/upload-file", buf.Bytes(), w.FormDataContentType(), 0)
	if err != nil {
		return "", err
	}
	var out struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &out) != nil || out.URL == "" {
		return "", errors.New("YouGile did not return the uploaded file address")
	}
	return c.Absolute(out.URL), nil
}

// Absolute turns a path on the host into a full address.
func (c *Client) Absolute(ref string) string {
	if strings.HasPrefix(ref, "/") {
		return c.origin + ref
	}
	return ref
}

// Fetch downloads a file from the client's own host (the key goes nowhere else) and returns it
// with its content type. Files over limit bytes are refused.
func (c *Client) Fetch(ctx context.Context, ref string, limit int64) ([]byte, string, error) {
	u := c.Absolute(ref)
	if !strings.HasPrefix(u, c.origin+"/") {
		return nil, "", errors.New("only files on the account's YouGile host can be downloaded")
	}
	data, header, err := c.do(ctx, http.MethodGet, u, nil, "", limit)
	if err != nil {
		return nil, "", err
	}
	return data, header.Get("Content-Type"), nil
}

// Company is one company a login belongs to.
type Company struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Companies lists the companies of a login (the call is made without a key).
func (c *Client) Companies(ctx context.Context, login, password string) ([]Company, error) {
	var out struct {
		Content []Company `json:"content"`
	}
	err := c.Call(ctx, http.MethodPost, "/auth/companies", nil, map[string]string{"login": login, "password": password}, &out)
	return out.Content, err
}

// NewKey makes an API key for one company of a login.
func (c *Client) NewKey(ctx context.Context, login, password, companyID string) (string, error) {
	var out struct {
		Key string `json:"key"`
	}
	err := c.Call(ctx, http.MethodPost, "/auth/keys", nil,
		map[string]string{"login": login, "password": password, "companyId": companyID}, &out)
	if err == nil && out.Key == "" {
		err = errors.New("YouGile did not return a key")
	}
	return out.Key, err
}
