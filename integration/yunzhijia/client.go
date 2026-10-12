package yunzhijia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// tokenSafetyMargin 是 accessToken 缓存的安全余量。
//
// K11：expireIn 是「剩余秒数」（倒计时），不是总时长——已实测同一接口连续两次返回
// expireIn = 7200 → 7161，递减。且 token 是**服务端共享**的：任何一方先取过
// （另一实例 / 运维调试 / 探针），我们本次拿到的 expireIn 就可能只剩几十秒。
// 因此缓存时长必须按返回值动态计算，并留出安全余量，避免在边界上用到即将过期的 token。
const tokenSafetyMargin = 300 * time.Second

// 授权 scope 取值：app 级（im-todo 待办 / ticket 免登）与 team 级（统一流程中心 cloudflow）
// 是**两套独立凭据**，互不通用——
//   - scope=app 调流程中心 → 10000401 认证失败；
//   - scope=team 调 im-todo → 10000401 认证失败；
//   - scope=team 不带 eid → 11000400 参数错误。
const (
	scopeApp  = "app"
	scopeTeam = "team"
)

// Client 封装云之家（私有化部署）服务端接口：app 级 accessToken 的获取/缓存、ticket 免登解析，
// 以及待办通知（创建 / 流转 / 状态查询）与通讯录增量拉取。
// 注意：所有错误信息都不携带 token/secret，避免日志泄漏凭证（见 do/scrub）。
type Client struct {
	baseURL string
	appID   string
	secret  string
	http    *http.Client

	mu sync.Mutex
	// token / tokenExpiry 实现动态 TTL 缓存（K11）。
	token       string
	tokenExpiry time.Time
	// refreshToken 由认证接口返回（K12）。本期**只解析并保存、不使用**，
	// 具体取舍见 GetAppToken 注释。
	refreshToken string

	// —— jsapiticket 的独立缓存（免登第 1 层：JS 桥）——
	// 与 app token 的 mu/token/tokenExpiry **物理分离**：jsapiticket 只有 600s
	// （token 是 7200s），复用同一份缓存或同一个过期字段会把两者寿命搞混。
	// 独立锁亦避免「取 jsapiticket」与「取 app token」互相阻塞。详见 jsapi.go。
	jsapiMu           sync.Mutex
	jsapiTicket       string
	jsapiTicketExpiry time.Time

	// —— team 级 accessToken 的独立缓存（统一流程中心 cloudflow）——
	// app 与 team 是两套独立凭据、互不通用（app 调流程中心返回 10000401 认证失败），
	// 且寿命不同，故与 app token 的 mu/token/tokenExpiry **物理分离**（照抄 jsapiticket 范式）。
	// eid 是企业 id，team 级 token 必须携带（实测缺 eid 返回 11000400 参数错误），
	// 由 SetEID 设置；读写均在 teamMu 保护下。
	teamMu          sync.Mutex
	teamToken       string
	teamTokenExpiry time.Time
	eid             string

	// —— getallpersons 专用节流状态（限流 500011 对策，详见 person.go）——
	// 与 token / jsapi 的锁**物理分离**：Client 是共享的，节流**只作用于
	// FetchAllPersons 这条路径**，绝不能拖慢 CreateTodo / ActionTodo / GetAppToken。
	personsMu       sync.Mutex
	personsLastCall time.Time
	personsInterval time.Duration // <=0 表示使用默认 personsMinInterval（生产默认 2.5s）
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

// SetEID 设置企业 id（eid）。team 级 accessToken（统一流程中心）必须携带 eid，
// 否则认证接口返回 11000400 参数错误。
//
// 为什么用 setter 而非新增构造函数：**保留 NewClient 原签名不变**是硬约束——现有 6 个
// 调用点（main.go / cmd/yzjsync / cmd/yzjcheck / syncer）全部只关心 app 级能力，改用
// 构造函数会迫使它们全部改签名；setter 让「是否需要 team 能力」成为调用方的**可选**选择，
// 不调用即保持现状（行为完全不变）。写入受 teamMu 保护，须在并发使用前调用（配置期一次）。
func (c *Client) SetEID(eid string) {
	c.teamMu.Lock()
	c.eid = eid
	c.teamMu.Unlock()
}

// User 是 ticket 解析后的用户上下文，只取本系统需要的字段。
type User struct {
	Username string `json:"username"` // 姓名
	JobNo    string `json:"jobNo"`    // 工号
	EID      string `json:"eid"`
	OpenID   string `json:"openid"`
	UserID   string `json:"userid"`
}

// flexBool 兼容云之家不同接口对布尔字段的类型不一致（K2，已实测）：
//   - 认证接口 /gateway/oauth2/token/getAccessToken 返回 bool true；
//   - 待办接口 /gateway/newtodo/open/... 返回字符串 "true"/"false"。
//
// 直接声明为 bool 会在字符串场景解析失败，声明为 string 又会在 bool 场景失败，
// 故所有响应结构体统一用 flexBool 解析布尔字段。
type flexBool bool

// UnmarshalJSON 同时接受 JSON 布尔、字符串 "true"/"false"（大小写不敏感）与数字 0/1；
// null / 空串按 false 处理。
func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		*b = false
		return nil
	}
	// 去掉字符串引号（形如 "true"）；空串 "" 按 false 处理。
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	switch strings.ToLower(s) {
	case "", "false", "0":
		*b = false
	case "true", "1":
		*b = true
	default:
		return fmt.Errorf("flexBool: 无法解析 %q", s)
	}
	return nil
}

// Bool 返回底层布尔值。
func (b flexBool) Bool() bool { return bool(b) }

// MaskOpenID 把 openId 脱敏为「前 4 后 4」形式（如 5de5****0423），供日志/错误串统一使用，
// 避免完整用户标识外泄（设计 §9）。长度不足 8 时整体打码，避免露出过多字符。
func MaskOpenID(openID string) string {
	if openID == "" {
		return ""
	}
	if len(openID) <= 8 {
		return "****"
	}
	return openID[:4] + "****" + openID[len(openID)-4:]
}

// GetAppToken 获取 app 级 accessToken，并按返回的 expireIn 动态缓存（K11）。
//
// 与旧实现（固定缓存 6000s、把 expireIn 解析进结构体后直接丢弃）不同：
//   - 缓存时长 = expireIn - tokenSafetyMargin(300s)；
//   - expireIn <= tokenSafetyMargin 时不缓存，本次直接用、下次调用重新获取。
//
// 关于 refreshToken（K12）：响应里的 refreshToken 会被解析并保存在 Client 上，但本期
// **有意不实现 token 刷新**。理由：secret 已在服务端，refreshToken 的「少传一次 secret」
// 收益有限，而引入刷新分支会增加状态与失败路径；如后续确有需要，再按文档补
// RefreshToken(ctx)（文档：POST /gateway/oauth2/token/refreshToken，app 级只需
// appId + refreshToken + timestamp + scope，不必传 secret）。
func (c *Client) GetAppToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.tokenExpiry) {
		return c.token, nil
	}
	tok, err := c.fetchToken(ctx, scopeApp)
	if err != nil {
		return "", err
	}
	c.token = tok.AccessToken
	// K12：解析并保存，本期不使用（见方法注释）。
	c.refreshToken = tok.RefreshToken
	if tok.ExpireIn > int(tokenSafetyMargin/time.Second) {
		c.tokenExpiry = time.Now().Add(time.Duration(tok.ExpireIn)*time.Second - tokenSafetyMargin)
	} else {
		// 剩余寿命不足安全余量：不缓存（tokenExpiry 归零 → 下次调用必然重新获取）。
		c.tokenExpiry = time.Time{}
	}
	return c.token, nil
}

// GetTeamToken 获取 team 级 accessToken（统一流程中心专用），并按返回的 expireIn 动态缓存。
//
// 与 GetAppToken **完全同构**，但：
//   - 使用 scope=team（app 级 token 调流程中心返回 10000401 认证失败）；
//   - 使用**独立缓存**（teamMu/teamToken/teamTokenExpiry），绝不触碰 c.token/c.tokenExpiry——
//     两者寿命不同，复用同一份缓存会并发互相覆盖（该客户端曾因 token 缓存踩过 K11 的坑）；
//   - 请求体额外携带 eid（缺 eid 返回 11000400），eid 由 SetEID 预先设置。
//
// team token 的 refreshToken 本期**不解析保存**（与 app 侧不同）：流程中心调用方不涉及刷新。
func (c *Client) GetTeamToken(ctx context.Context) (string, error) {
	c.teamMu.Lock()
	defer c.teamMu.Unlock()

	if c.teamToken != "" && time.Now().Before(c.teamTokenExpiry) {
		return c.teamToken, nil
	}
	tok, err := c.fetchToken(ctx, scopeTeam)
	if err != nil {
		return "", err
	}
	c.teamToken = tok.AccessToken
	if tok.ExpireIn > int(tokenSafetyMargin/time.Second) {
		c.teamTokenExpiry = time.Now().Add(time.Duration(tok.ExpireIn)*time.Second - tokenSafetyMargin)
	} else {
		// 剩余寿命不足安全余量：不缓存，下次调用重新获取。
		c.teamTokenExpiry = time.Time{}
	}
	return c.teamToken, nil
}

// appToken 是 getAccessToken 返回的凭据集合。
type appToken struct {
	AccessToken  string
	RefreshToken string
	ExpireIn     int
}

// fetchToken 调 getAccessToken 取一枚新凭据。scope 决定授权级别：
//   - scopeApp：im-todo / ticket 免登（不携带 eid）；
//   - scopeTeam：统一流程中心（**必须**携带 eid，缺 eid 实测返回 11000400 参数错误）。
func (c *Client) fetchToken(ctx context.Context, scope string) (*appToken, error) {
	body := struct {
		AppID     string `json:"appId"`
		Secret    string `json:"secret"`
		Timestamp int64  `json:"timestamp"`
		Scope     string `json:"scope"`
		EID       string `json:"eid,omitempty"`
	}{
		AppID:     c.appID,
		Secret:    c.secret,
		Timestamp: time.Now().UnixMilli(),
		Scope:     scope,
	}
	if scope == scopeTeam {
		// 读取 eid：调用方（GetTeamToken）已持有 teamMu，故此处读取是并发安全的。
		if c.eid == "" {
			return nil, fmt.Errorf("yunzhijia team token: eid 未配置（须先调用 SetEID）")
		}
		body.EID = c.eid
	}
	var resp struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpireIn     int    `json:"expireIn"`
		} `json:"data"`
		ErrorCode int      `json:"errorCode"`
		Success   flexBool `json:"success"` // K2：认证接口返回 bool，用 flexBool 兼容
		Error     string   `json:"error"`
	}
	// 认证接口自身不带 accessToken（token 传空 → 不做脱敏替换）。
	if err := c.doJSON(ctx, "/gateway/oauth2/token/getAccessToken", "", body, &resp); err != nil {
		return nil, fmt.Errorf("yunzhijia access token: %w", err)
	}
	if !resp.Success.Bool() || resp.Data.AccessToken == "" {
		return nil, fmt.Errorf("yunzhijia access token denied: errorCode=%d error=%s", resp.ErrorCode, resp.Error)
	}
	return &appToken{
		AccessToken:  resp.Data.AccessToken,
		RefreshToken: resp.Data.RefreshToken,
		ExpireIn:     resp.Data.ExpireIn,
	}, nil
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
		Data      User     `json:"data"`
		ErrorCode int      `json:"errorCode"`
		Success   flexBool `json:"success"`
		Error     string   `json:"error"`
	}
	path := "/gateway/ticket/user/acquirecontext?accessToken=" + url.QueryEscape(token)
	if err := c.doJSON(ctx, path, token, body, &resp); err != nil {
		return nil, fmt.Errorf("yunzhijia resolve user: %w", err)
	}
	if !resp.Success.Bool() {
		return nil, fmt.Errorf("yunzhijia resolve user denied: errorCode=%d error=%s", resp.ErrorCode, resp.Error)
	}
	return &resp.Data, nil
}

// tokenPath 取一次 app token 并拼到 path 的 query 上；第二个返回值是该 token，
// 仅用于在错误信息里脱敏（scrub），不用于其它用途。
func (c *Client) tokenPath(ctx context.Context, path string) (string, string, error) {
	token, err := c.GetAppToken(ctx)
	if err != nil {
		return "", "", err
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "accessToken=" + url.QueryEscape(token), token, nil
}

// tokenPathScope 是 tokenPath 的 scope 变体：按 scope 取对应级别的 token 并拼到 path 的 query 上。
//   - scopeApp → GetAppToken（行为与 tokenPath 完全一致）；
//   - scopeTeam → GetTeamToken（统一流程中心）。
//
// ⚠️ 保留原 tokenPath 不动：现有 im-todo（todo.go）与通讯录（person.go）仍调用它，
// 改签名会波及这些路径（本任务要求「不改变现有 im-todo 的任何行为」）。
func (c *Client) tokenPathScope(ctx context.Context, path, scope string) (string, string, error) {
	var (
		token string
		err   error
	)
	if scope == scopeTeam {
		token, err = c.GetTeamToken(ctx)
	} else {
		token, err = c.GetAppToken(ctx)
	}
	if err != nil {
		return "", "", err
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "accessToken=" + url.QueryEscape(token), token, nil
}

// doJSON 以 application/json 发送请求并解析响应。
func (c *Client) doJSON(ctx context.Context, path, token string, body any, dst any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.do(ctx, path, token, "application/json", payload, dst)
}

// doForm 以 application/x-www-form-urlencoded 发送请求并解析响应。
// 通讯录 getallpersons 要求表单编码（K10），不能复用 doJSON。
func (c *Client) doForm(ctx context.Context, path, token string, form url.Values, dst any) error {
	return c.do(ctx, path, token, "application/x-www-form-urlencoded", []byte(form.Encode()), dst)
}

// do 是统一的 HTTP 发送 + 响应解析。token 仅用于把可能出现在底层错误里的凭证替换掉
// （见 scrub），确保返回的 error 不携带 token/secret。
func (c *Client) do(ctx context.Context, path, token, contentType string, payload []byte, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return c.scrub(err, token)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := c.http.Do(req)
	if err != nil {
		return c.scrub(err, token)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return c.scrub(err, token)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// scrub 把错误信息里可能出现的 token 替换为 ***。
// 必要性：net/http 在传输层失败时返回的 *url.Error 会把完整请求 URL（含 ?accessToken=xxx）
// 拼进错误串，若不处理会随日志泄漏凭证。
func (c *Client) scrub(err error, token string) error {
	if err == nil {
		return nil
	}
	if token == "" {
		return err
	}
	msg := err.Error()
	msg = strings.ReplaceAll(msg, token, "***")
	if esc := url.QueryEscape(token); esc != token {
		msg = strings.ReplaceAll(msg, esc, "***")
	}
	return errors.New(msg)
}
