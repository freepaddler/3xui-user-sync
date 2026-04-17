package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chu/3xui-user-sync/internal/domain"
	"github.com/rs/zerolog"
)

const (
	defaultFlow   = "xtls-rprx-vision"
	defaultSub    = "ry8t37ga2gklqxjq"
	inboundsRoute = "/panel/api/inbounds"
)

type Client struct {
	httpClient *http.Client
	timeout    time.Duration
	log        zerolog.Logger

	mu           sync.Mutex
	authLoggedIn map[string]bool
}

func New(timeout time.Duration, log zerolog.Logger) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Client{
		httpClient: &http.Client{
			Jar: jar,
		},
		log:          log,
		timeout:      timeout,
		authLoggedIn: make(map[string]bool),
	}, nil
}

type ServerCredentials struct {
	BaseURL       string
	Username      string
	Password      string
	Subscription  string
	ServerID      int64
	ServerLabel   string
}

func (c *Client) ListInbounds(ctx context.Context, creds ServerCredentials) ([]domain.Inbound, error) {
	var result apiResponse[[]apiInbound]
	if err := c.doJSON(ctx, creds, http.MethodGet, inboundsRoute+"/list", nil, &result); err != nil {
		c.log.Debug().
			Str("server", creds.ServerLabel).
			Str("base_url", creds.BaseURL).
			Err(err).
			Msg("3x-ui list inbounds failed")
		return nil, err
	}
	if err := apiResultError("3x-ui list inbounds", result.Success, result.Msg); err != nil {
		return nil, err
	}

	out := make([]domain.Inbound, 0, len(result.Obj))
	for _, item := range result.Obj {
		clients, err := parseSettingsClients(item.Settings)
		if err != nil {
			return nil, fmt.Errorf("parse inbound %d clients: %w", item.ID, err)
		}
		stream := parseStreamSettings(item.StreamSettings)
		out = append(out, domain.Inbound{
			ID:       item.ID,
			Tag:      item.Tag,
			Remark:   item.Remark,
			Protocol: item.Protocol,
			Network:  stream.Network,
			Security: stream.Security,
			Clients:  clients,
		})
	}
	c.log.Debug().
		Str("server", creds.ServerLabel).
		Str("base_url", creds.BaseURL).
		Int("inbounds", len(out)).
		Msg("3x-ui list inbounds ok")
	return out, nil
}

func (c *Client) UpsertClient(ctx context.Context, creds ServerCredentials, inbound domain.Inbound, remote domain.RemoteClient) error {
	exists := false
	for _, current := range inbound.Clients {
		if current.UID == remote.UID {
			exists = true
			break
		}
	}

	if !exists {
		settingsJSON, err := json.Marshal(addClientSettings{
			Clients: []apiClient{toAPIClient(remote)},
		})
		if err != nil {
			return err
		}
		form := url.Values{}
		form.Set("id", strconv.FormatInt(inbound.ID, 10))
		form.Set("settings", string(settingsJSON))
		return c.doExpectSuccessForm(ctx, creds, http.MethodPost, inboundsRoute+"/addClient", form)
	}

	settingsJSON, err := json.Marshal(addClientSettings{
		Clients: []apiClient{toAPIClient(remote)},
	})
	if err != nil {
		return err
	}
	payload := updateClientRequest{
		ID:       inbound.ID,
		Settings: string(settingsJSON),
	}
	return c.doExpectSuccess(ctx, creds, http.MethodPost, inboundsRoute+"/updateClient/"+url.PathEscape(remote.UID), payload)
}

func (c *Client) DeleteClient(ctx context.Context, creds ServerCredentials, inboundID int64, uid string) error {
	return c.doExpectSuccess(ctx, creds, http.MethodPost, fmt.Sprintf("%s/%d/delClient/%s", inboundsRoute, inboundID, url.PathEscape(uid)), nil)
}

func (c *Client) doExpectSuccessForm(ctx context.Context, creds ServerCredentials, method, path string, form url.Values) error {
	var response apiResponse[json.RawMessage]
	if err := c.doForm(ctx, creds, method, path, form, &response); err != nil {
		return err
	}
	return apiResultError("3x-ui request failed", response.Success, response.Msg)
}

func (c *Client) doExpectSuccess(ctx context.Context, creds ServerCredentials, method, path string, payload any) error {
	var response apiResponse[json.RawMessage]
	if err := c.doJSON(ctx, creds, method, path, payload, &response); err != nil {
		return err
	}
	return apiResultError("3x-ui request failed", response.Success, response.Msg)
}

func (c *Client) doForm(ctx context.Context, creds ServerCredentials, method, path string, form url.Values, out any) error {
	if err := c.ensureLogin(ctx, creds); err != nil {
		return err
	}
	response, err := c.sendForm(ctx, creds, method, path, form, out)
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusUnauthorized {
		c.log.Debug().
			Str("server", creds.ServerLabel).
			Str("method", method).
			Str("path", path).
			Msg("3x-ui request unauthorized, retry login")
		c.mu.Lock()
		delete(c.authLoggedIn, creds.BaseURL)
		c.mu.Unlock()
		if err := c.ensureLogin(ctx, creds); err != nil {
			return err
		}
		_, err = c.sendForm(ctx, creds, method, path, form, out)
		return err
	}
	if response.StatusCode >= 400 {
		return response.error(method, fullURL(creds.BaseURL, path))
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, creds ServerCredentials, method, path string, payload any, out any) error {
	if err := c.ensureLogin(ctx, creds); err != nil {
		return err
	}
	response, err := c.sendJSON(ctx, creds, method, path, payload, out)
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusUnauthorized {
		c.log.Debug().
			Str("server", creds.ServerLabel).
			Str("method", method).
			Str("path", path).
			Msg("3x-ui request unauthorized, retry login")
		c.mu.Lock()
		delete(c.authLoggedIn, creds.BaseURL)
		c.mu.Unlock()
		if err := c.ensureLogin(ctx, creds); err != nil {
			return err
		}
		_, err = c.sendJSON(ctx, creds, method, path, payload, out)
		return err
	}
	if response.StatusCode >= 400 {
		return response.error(method, fullURL(creds.BaseURL, path))
	}
	return nil
}

func (c *Client) ensureLogin(ctx context.Context, creds ServerCredentials) error {
	c.mu.Lock()
	if c.authLoggedIn[creds.BaseURL] {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	loginPayload := url.Values{}
	loginPayload.Set("username", creds.Username)
	loginPayload.Set("password", creds.Password)
	var result apiResponse[json.RawMessage]
	loginPaths := []string{"/login", "/login/"}
	var lastErr error
	for _, loginPath := range loginPaths {
		c.log.Debug().
			Str("server", creds.ServerLabel).
			Str("base_url", creds.BaseURL).
			Str("path", loginPath).
			Msg("3x-ui login attempt")
		response, err := c.sendForm(ctx, creds, http.MethodPost, loginPath, loginPayload, &result)
		if err == nil && response.StatusCode < 400 && result.Success {
			c.mu.Lock()
			c.authLoggedIn[creds.BaseURL] = true
			c.mu.Unlock()
			c.log.Debug().
				Str("server", creds.ServerLabel).
				Str("base_url", creds.BaseURL).
				Str("path", loginPath).
				Int("status", response.StatusCode).
				Msg("3x-ui login ok")
			return nil
		}
		if err != nil {
			c.log.Debug().
				Str("server", creds.ServerLabel).
				Str("base_url", creds.BaseURL).
				Str("path", loginPath).
				Err(err).
				Msg("3x-ui login transport/decode error")
			lastErr = err
			continue
		}
		c.log.Debug().
			Str("server", creds.ServerLabel).
			Str("base_url", creds.BaseURL).
			Str("path", loginPath).
			Int("status", response.StatusCode).
			Bool("success", result.Success).
			Str("msg", result.Msg).
			Str("body", response.shortBody()).
			Msg("3x-ui login rejected")
		if response.StatusCode >= 400 {
			lastErr = response.error(http.MethodPost, fullURL(creds.BaseURL, loginPath))
		} else {
			lastErr = apiResultError("3x-ui login failed", result.Success, result.Msg)
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("3x-ui login failed")
}

func (c *Client) sendJSON(ctx context.Context, creds ServerCredentials, method, path string, payload any, out any) (responseMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return responseMeta{}, err
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(creds.BaseURL, "/")+path, body)
	if err != nil {
		return responseMeta{}, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.Debug().
			Str("server", creds.ServerLabel).
			Str("method", method).
			Str("url", fullURL(creds.BaseURL, path)).
			Err(err).
			Msg("3x-ui json request transport error")
		return responseMeta{}, err
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return responseMeta{StatusCode: resp.StatusCode}, err
	}
	if len(bytes.TrimSpace(rawBody)) == 0 || bytes.Equal(bytes.TrimSpace(rawBody), []byte(`""`)) {
		meta := responseMeta{StatusCode: resp.StatusCode, Body: string(rawBody)}
		c.log.Debug().
			Str("server", creds.ServerLabel).
			Str("method", method).
			Str("url", fullURL(creds.BaseURL, path)).
			Int("status", meta.StatusCode).
			Str("body", meta.shortBody()).
			Msg("3x-ui json response")
		return meta, nil
	}
	if out != nil {
		if err := json.Unmarshal(rawBody, out); err != nil {
			return responseMeta{StatusCode: resp.StatusCode, Body: string(rawBody)}, fmt.Errorf("decode 3x-ui response: %w; raw=%s", err, string(rawBody))
		}
	}
	meta := responseMeta{StatusCode: resp.StatusCode, Body: string(rawBody)}
	c.log.Debug().
		Str("server", creds.ServerLabel).
		Str("method", method).
		Str("url", fullURL(creds.BaseURL, path)).
		Int("status", meta.StatusCode).
		Str("body", meta.shortBody()).
		Msg("3x-ui json response")
	return meta, nil
}

func (c *Client) sendForm(ctx context.Context, creds ServerCredentials, method, path string, form url.Values, out any) (responseMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body := strings.NewReader(form.Encode())

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(creds.BaseURL, "/")+path, body)
	if err != nil {
		return responseMeta{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.Debug().
			Str("server", creds.ServerLabel).
			Str("method", method).
			Str("url", fullURL(creds.BaseURL, path)).
			Err(err).
			Msg("3x-ui form request transport error")
		return responseMeta{}, err
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return responseMeta{StatusCode: resp.StatusCode}, err
	}
	if len(bytes.TrimSpace(rawBody)) == 0 || bytes.Equal(bytes.TrimSpace(rawBody), []byte(`""`)) {
		meta := responseMeta{StatusCode: resp.StatusCode, Body: string(rawBody)}
		c.log.Debug().
			Str("server", creds.ServerLabel).
			Str("method", method).
			Str("url", fullURL(creds.BaseURL, path)).
			Int("status", meta.StatusCode).
			Str("body", meta.shortBody()).
			Msg("3x-ui form response")
		return meta, nil
	}
	if out != nil {
		if err := json.Unmarshal(rawBody, out); err != nil {
			return responseMeta{StatusCode: resp.StatusCode, Body: string(rawBody)}, fmt.Errorf("decode 3x-ui response: %w; raw=%s", err, string(rawBody))
		}
	}
	meta := responseMeta{StatusCode: resp.StatusCode, Body: string(rawBody)}
	c.log.Debug().
		Str("server", creds.ServerLabel).
		Str("method", method).
		Str("url", fullURL(creds.BaseURL, path)).
		Int("status", meta.StatusCode).
		Str("body", meta.shortBody()).
		Msg("3x-ui form response")
	return meta, nil
}

type apiResponse[T any] struct {
	Success bool   `json:"success"`
	Msg     string `json:"msg"`
	Obj     T      `json:"obj"`
}

type apiInbound struct {
	ID             int64  `json:"id"`
	Remark         string `json:"remark"`
	Tag            string `json:"tag"`
	Protocol       string `json:"protocol"`
	Settings       string `json:"settings"`
	StreamSettings string `json:"streamSettings"`
}

type streamSettings struct {
	Network  string `json:"network"`
	Security string `json:"security"`
}

type inboundSettings struct {
	Clients []apiClient `json:"clients"`
}

type apiClient struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	Enable     bool   `json:"enable"`
	Flow       string `json:"flow,omitempty"`
	SubID      string `json:"subId,omitempty"`
	TGID       any    `json:"tgId,omitempty"`
	Comment    string `json:"comment,omitempty"`
	Reset      int    `json:"reset,omitempty"`
	LimitIP    int    `json:"limitIp,omitempty"`
	TotalGB    int64  `json:"totalGB,omitempty"`
	ExpiryTime int64  `json:"expiryTime,omitempty"`
	CreatedAt  int64  `json:"created_at,omitempty"`
	UpdatedAt  int64  `json:"updated_at,omitempty"`
}

type addClientSettings struct {
	Clients []apiClient `json:"clients"`
}

type updateClientRequest struct {
	ID       int64  `json:"id"`
	Settings string `json:"settings"`
}

func parseSettingsClients(raw string) ([]domain.RemoteClient, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var settings inboundSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return nil, err
	}
	out := make([]domain.RemoteClient, 0, len(settings.Clients))
	for _, client := range settings.Clients {
		out = append(out, domain.RemoteClient{
			UID:        client.ID,
			Email:      client.Email,
			Flow:       client.Flow,
			Enable:     client.Enable,
			SubID:      client.SubID,
			TGID:       stringifyAny(client.TGID),
			Comment:    client.Comment,
			Reset:      client.Reset,
			LimitIP:    client.LimitIP,
			TotalGB:    client.TotalGB,
			ExpiryTime: client.ExpiryTime,
			CreatedAt:  client.CreatedAt,
			UpdatedAt:  client.UpdatedAt,
		})
	}
	return out, nil
}

func parseStreamSettings(raw string) streamSettings {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return streamSettings{}
	}
	var parsed streamSettings
	_ = json.Unmarshal([]byte(raw), &parsed)
	return parsed
}

func toAPIClient(c domain.RemoteClient) apiClient {
	return apiClient{
		ID:         c.UID,
		Email:      c.Email,
		Enable:     c.Enable,
		Flow:       fallback(c.Flow, defaultFlow),
		SubID:      fallback(c.SubID, defaultSub),
		TGID:       zeroToEmpty(c.TGID),
		Comment:    c.Comment,
		Reset:      c.Reset,
		LimitIP:    c.LimitIP,
		TotalGB:    c.TotalGB,
		ExpiryTime: c.ExpiryTime,
		CreatedAt:  c.CreatedAt,
		UpdatedAt:  c.UpdatedAt,
	}
}

func fallback(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

func ParseSubscriptionID(v string) string {
	v = strings.TrimSpace(v)
	if v != "" {
		return v
	}
	return defaultSub + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func stringifyAny(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case float64:
		if value == 0 {
			return ""
		}
		return strconv.FormatInt(int64(value), 10)
	case int64:
		if value == 0 {
			return ""
		}
		return strconv.FormatInt(value, 10)
	case int:
		if value == 0 {
			return ""
		}
		return strconv.Itoa(value)
	default:
		return fmt.Sprint(value)
	}
}

func zeroToEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return v
}

type responseMeta struct {
	StatusCode int
	Body       string
}

func (r responseMeta) shortBody() string {
	body := strings.TrimSpace(r.Body)
	if len(body) > 400 {
		return body[:400]
	}
	return body
}

func (r responseMeta) error(method, fullURL string) error {
	body := r.shortBody()
	if body == "" {
		return fmt.Errorf("%s %s returned HTTP %d", method, fullURL, r.StatusCode)
	}
	return fmt.Errorf("%s %s returned HTTP %d: %s", method, fullURL, r.StatusCode, body)
}

func fullURL(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + path
}

func apiResultError(prefix string, success bool, msg string) error {
	if success {
		return nil
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return fmt.Errorf("%s: success=false", prefix)
	}
	return fmt.Errorf("%s: %s", prefix, msg)
}
