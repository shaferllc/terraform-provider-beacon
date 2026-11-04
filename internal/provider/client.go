package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client is a minimal Beacon REST API v1 client.
type Client struct {
	BaseURL   string // e.g. https://usebeacon.pro/api/v1
	Token     string
	UserAgent string
	HTTP      *http.Client
}

// APIError is the Beacon error envelope: {"message","code","errors"}.
type APIError struct {
	Status  int
	Code    string              `json:"code"`
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors"`
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("Beacon API %d %s: %s", e.Status, e.Code, e.Message)
	keys := make([]string, 0, len(e.Errors))
	for k := range e.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		msg += fmt.Sprintf("\n  %s: %s", k, strings.Join(e.Errors[k], "; "))
	}
	return msg
}

func isNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// envelope is a decoded response body; Data is unwrapped from {"data":…}.
type envelope struct {
	Data   json.RawMessage `json:"data"`
	Secret *string         `json:"secret"`
	Meta   struct {
		NextCursor *string `json:"next_cursor"`
	} `json:"meta"`
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*envelope, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.UserAgent)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		// Back off on rate limits, honouring Retry-After (max 3 retries).
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			select {
			case <-time.After(time.Duration(max(wait, 1)) * time.Second):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		if resp.StatusCode >= 400 {
			apiErr := &APIError{Status: resp.StatusCode}
			if json.Unmarshal(raw, apiErr) != nil || apiErr.Message == "" {
				apiErr.Message = strings.TrimSpace(string(raw))
			}
			return nil, apiErr
		}

		env := &envelope{}
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, env); err != nil {
				return nil, fmt.Errorf("decoding Beacon response: %w", err)
			}
		}
		return env, nil
	}
}

// object performs a request and returns the unwrapped "data" object. A top-level
// "secret" (only sent when a webhook is created) is merged into it.
func (c *Client) object(ctx context.Context, method, path string, body any) (map[string]any, error) {
	env, err := c.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	data := map[string]any{}
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return nil, err
		}
	}
	if env.Secret != nil {
		data["secret"] = *env.Secret
	}
	return data, nil
}

// list follows cursor pagination and returns every item.
func (c *Client) list(ctx context.Context, path string) ([]map[string]any, error) {
	var all []map[string]any
	cursor := ""
	for {
		q := url.Values{"per_page": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		env, err := c.do(ctx, http.MethodGet, path+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var page []map[string]any
		if err := json.Unmarshal(env.Data, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		if env.Meta.NextCursor == nil || *env.Meta.NextCursor == "" {
			return all, nil
		}
		cursor = *env.Meta.NextCursor
	}
}
