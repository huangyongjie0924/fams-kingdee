package kingdee

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	tokenEndpoint = "/oauth2/getToken"
	language      = "zh_CN"
)

// Client 封装金蝶星瀚 OpenAPI 调用：token 缓存/刷新、分页、超时、有限重试。
type Client struct {
	http        *http.Client
	cfg         Config
	baseURL     *url.URL
	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
}

// NewClient 根据配置创建客户端。配置中 RequestTimeout 为 0 时不会设置超时，调用方应保证配置合理。
func NewClient(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("kingdee base_url is required")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse kingdee base_url: %w", err)
	}
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 30
	}
	pageSize := cfg.PageSize
	if pageSize <= 0 {
		pageSize = 200
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	maxRetries := cfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 3
	}
	cfg.RequestTimeout = timeout
	cfg.PageSize = pageSize
	cfg.MaxRetries = maxRetries
	return &Client{
		http: &http.Client{
			Timeout: time.Duration(timeout) * time.Second,
		},
		cfg:     cfg,
		baseURL: u,
	}, nil
}

// SelectAssetCards 分页拉取全部资产卡；filter 为可选的业务过滤字符串（如 modifytime 增量条件）。
// 资产卡查询接口要求 data 中必须包含 assetname、billno 字段，这里固定传空字符串。
func (c *Client) SelectAssetCards(ctx context.Context, filter string) ([]AssetCard, error) {
	var out []AssetCard
	pageNo := 1
	for {
		page, err := c.fetchAssetCardPage(ctx, filter, pageNo)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Rows...)
		if page.LastPage {
			break
		}
		pageNo++
		if pageNo > 100000 {
			return nil, errors.New("分页超过安全上限，可能陷入死循环")
		}
	}
	return out, nil
}

type selectAssetCardReq struct {
	Data     selectAssetCardData `json:"data"`
	PageNo   int                 `json:"pageNo"`
	PageSize int                 `json:"pageSize"`
}

type selectAssetCardData struct {
	AssetName string `json:"assetname"`
	BillNo    string `json:"billno"`
	Filter    string `json:"filter"`
}

func (c *Client) fetchAssetCardPage(ctx context.Context, filter string, pageNo int) (*AssetPage, error) {
	reqBody := selectAssetCardReq{
		Data: selectAssetCardData{
			// 资产卡查询接口要求这两个字段非空，但经确认不用于过滤
			AssetName: "1",
			BillNo:    "1",
			Filter:    filter,
		},
		PageNo:   pageNo,
		PageSize: c.cfg.PageSize,
	}
	var resp apiResponse[AssetPage]
	if err := c.postJSON(ctx, joinURL(c.cfg.BaseURL, c.cfg.QueryPath), reqBody, &resp); err != nil {
		return nil, err
	}
	if !resp.Status {
		return nil, fmt.Errorf("kingdee asset api error: code=%s message=%s", resp.ErrorCode, resp.Message)
	}
	return &resp.Data, nil
}

// TestConnect 尝试获取一次 token，用于管理后台验证连通性。
func (c *Client) TestConnect(ctx context.Context) error {
	_, err := c.ensureToken(ctx)
	return err
}

func (c *Client) ensureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Until(c.tokenExpiry) > 60*time.Second {
		return c.token, nil
	}
	token, err := c.getToken(ctx)
	if err != nil {
		return "", err
	}
	c.token = token
	// 金蝶 token 生命周期未在文档明确，保守缓存 55 分钟
	c.tokenExpiry = time.Now().Add(55 * time.Minute)
	return c.token, nil
}

func (c *Client) invalidateToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	c.tokenExpiry = time.Time{}
}

type tokenReq struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Username     string `json:"username"`
	AccountID    string `json:"accountId"`
	Nonce        string `json:"nonce"`
	Timestamp    string `json:"timestamp"`
	Language     string `json:"language"`
}

func (c *Client) getToken(ctx context.Context) (string, error) {
	body := tokenReq{
		ClientID:     c.cfg.ClientID,
		ClientSecret: c.cfg.ClientSecret,
		Username:     c.cfg.Username,
		AccountID:    c.cfg.AccountID,
		Nonce:        randomHex(16),
		Timestamp:    time.Now().Format("2006-01-02 15:04:05"),
		Language:     language,
	}
	var resp apiResponse[tokenData]
	if err := c.postJSONNoAuth(ctx, joinURL(c.cfg.BaseURL, tokenEndpoint), body, &resp); err != nil {
		return "", fmt.Errorf("kingdee token: %w", err)
	}
	if !resp.Status || resp.Data.AccessToken == "" {
		return "", fmt.Errorf("kingdee token denied: code=%s message=%s", resp.ErrorCode, resp.Message)
	}
	return resp.Data.AccessToken, nil
}

func (c *Client) postJSON(ctx context.Context, urlStr string, body any, dst any) error {
	return c.doJSON(ctx, urlStr, body, true, dst)
}

func (c *Client) postJSONNoAuth(ctx context.Context, urlStr string, body any, dst any) error {
	return c.doJSON(ctx, urlStr, body, false, dst)
}

func (c *Client) doJSON(ctx context.Context, urlStr string, body any, withToken bool, dst any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	maxRetries := c.cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 1
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		var token string
		if withToken {
			var err error
			token, err = c.ensureToken(ctx)
			if err != nil {
				lastErr = err
				continue
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if withToken {
			req.Header.Set("accessToken", token)
		}
		req.Header.Set("Idempotency-Key", randomHex(16))

		httpResp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		bodyBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, 10<<20))
		httpResp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		switch {
		case withToken && httpResp.StatusCode == http.StatusUnauthorized:
			c.invalidateToken()
			lastErr = fmt.Errorf("unauthorized")
			continue
		case httpResp.StatusCode == http.StatusTooManyRequests:
			lastErr = fmt.Errorf("rate limited")
			if sec, _ := strconv.Atoi(httpResp.Header.Get("Retry-After")); sec > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(sec) * time.Second):
				}
			}
			continue
		case httpResp.StatusCode >= 500:
			lastErr = fmt.Errorf("server error %d", httpResp.StatusCode)
			continue
		case httpResp.StatusCode >= 400:
			return fmt.Errorf("request error %d: %s", httpResp.StatusCode, truncate(string(bodyBytes), 200))
		}

		dec := json.NewDecoder(bytes.NewReader(bodyBytes))
		dec.UseNumber()
		if err := dec.Decode(dst); err != nil {
			lastErr = fmt.Errorf("decode response: %w", err)
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("unknown error")
	}
	return fmt.Errorf("after %d attempts: %w", maxRetries, lastErr)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func joinURL(base, path string) string {
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(path, "/")
}
