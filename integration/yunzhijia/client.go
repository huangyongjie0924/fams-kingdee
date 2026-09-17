package yunzhijia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client 封装云之家（私有化部署）服务端接口：app 级 accessToken 的获取/缓存，以及 ticket 免登解析。
// 注意：所有错误信息都不携带 token/secret，避免日志泄漏凭证。
type Client struct {
	baseURL string
	appID   string
	secret  string
	http    *http.Client

	mu      sync.Mutex
	token   string
	tokenAt time.Time
}

// NewClient 创建客户端；timeout<=0 时默认 10 秒。
func NewClient(baseURL, appID, secret string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		appID:   appID,
		secret:  secret,
		http:    &http.Client{Timeout: timeout},
	}
}

// User 是 ticket 解析后的用户上下文，只取本系统需要的字段。
type User struct {
	Username string `json:"username"` // 姓名
	JobNo    string `json:"jobNo"`    // 工号
	EID      string `json:"eid"`
	OpenID   string `json:"openid"`
	UserID   string `json:"userid"`
}

// GetAppToken 获取 app 级 accessToken。文档称 6400s 有效、期内重复获取返回同一 token，
// 这里缓存 6000s 复用，避免每次免登都打一次授权接口。
func (c *Client) GetAppToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Since(c.tokenAt) < 6000*time.Second {
		return c.token, nil
	}
	token, err := c.fetchAppToken(ctx)
	if err != nil {
		return "", err
	}
	c.token = token
	c.tokenAt = time.Now()
	return c.token, nil
}

func (c *Client) fetchAppToken(ctx context.Context) (string, error) {
	body := struct {
		AppID     string `json:"appId"`
		Secret    string `json:"secret"`
		Timestamp int64  `json:"timestamp"`
		Scope     string `json:"scope"`
	}{
		AppID:     c.appID,
		Secret:    c.secret,
		Timestamp: time.Now().UnixMilli(),
		Scope:     "app",
	}
	var resp struct {
		Data struct {
			AccessToken string `json:"accessToken"`
			ExpireIn    int    `json:"expireIn"`
		} `json:"data"`
		ErrorCode int    `json:"errorCode"`
		Success   bool   `json:"success"`
		Error     string `json:"error"`
	}
	if err := c.doJSON(ctx, "/gateway/oauth2/token/getAccessToken", body, &resp); err != nil {
		return "", fmt.Errorf("yunzhijia access token: %w", err)
	}
	if !resp.Success || resp.Data.AccessToken == "" {
		return "", fmt.Errorf("yunzhijia access token denied: errorCode=%d error=%s", resp.ErrorCode, resp.Error)
	}
	return resp.Data.AccessToken, nil
}

// ResolveUser 用云之家客户端下发的 ticket 换取用户上下文。
func (c *Client) ResolveUser(ctx context.Context, ticket string) (*User, error) {
	token, err := c.GetAppToken(ctx)
	if err != nil {
		return nil, err
	}
	body := struct {
		AppID  string `json:"appid"`
		Ticket string `json:"ticket"`
	}{
		AppID:  c.appID,
		Ticket: ticket,
	}
	var resp struct {
		Data      User   `json:"data"`
		ErrorCode int    `json:"errorCode"`
		Success   bool   `json:"success"`
		Error     string `json:"error"`
	}
	path := "/gateway/ticket/user/acquirecontext?accessToken=" + token
	if err := c.doJSON(ctx, path, body, &resp); err != nil {
		return nil, fmt.Errorf("yunzhijia resolve user: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("yunzhijia resolve user denied: errorCode=%d error=%s", resp.ErrorCode, resp.Error)
	}
	return &resp.Data, nil
}

func (c *Client) doJSON(ctx context.Context, path string, body any, dst any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
