package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"asset-mgr/config"
	"asset-mgr/model"
)

// —— 免登 token 安全断言（docs/云之家待办通知集成方案.md §5.4 缺陷 1/2、§10.1b）——
//
// 本文件的多数用例只需中间件与 token 逻辑，**不需要数据库**：401/403 都在中间件层短路，
// 不触达业务 handler。为此用一个「桩 mux」注册若干 200 路由，使「本应被拦」的请求若真漏过去
// 会得到干净的 200（而不是因 nil store panic）。只有端到端换会话的用例连真库（连不上则 Skip）。

const (
	testJWTSecret       = "test-session-jwt-secret"
	testDeeplinkSecret  = "test-deeplink-login-secret-independent"
	testScopedRepairID  = int64(100)
	testForeignRepairID = int64(200)
)

// deeplinkTestConfig 构造一份内存配置：会话密钥与免登密钥**互相独立**（这是缺陷 1 的正面前提）。
func deeplinkTestConfig() *config.Config {
	var c config.Config
	c.Auth.JWTSecret = testJWTSecret
	c.Auth.TokenHours = 12
	c.Yunzhijia.LoginTokenSecret = testDeeplinkSecret
	c.Yunzhijia.DeeplinkTokenTTL = time.Hour
	return &c
}

// stubMux 注册一批 200 的桩路由，覆盖详情页会碰到的路径与若干白名单外的路径。
func stubMux() *http.ServeMux {
	ok := func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me", ok)
	mux.HandleFunc("GET /api/enums", ok)
	mux.HandleFunc("GET /api/repairs", ok)
	mux.HandleFunc("GET /api/repairs/{id}", ok)
	mux.HandleFunc("GET /api/repairs/{id}/logs", ok)
	mux.HandleFunc("GET /api/repairs/{id}/attachments", ok)
	mux.HandleFunc("GET /api/assets", ok)
	mux.HandleFunc("GET /api/employees", ok)
	mux.HandleFunc("GET /api/vendors", ok)
	mux.HandleFunc("POST /api/repairs/{id}/dispatch", ok)
	return mux
}

func newStubHandler(cfg *config.Config) http.Handler {
	return NewServer(nil, cfg).Authenticate(stubMux())
}

// newRealHandler 用真实的 srv.Routes 建 mux 再包中间件——用于 deeplink-login 用例：
// 该 handler 在「空 token / 过期 / 未配置密钥」时都在触达 store 之前返回，
// 因此即便 srv.st 为 nil 也安全。
func newRealHandler(srv *Server) http.Handler {
	mux := http.NewServeMux()
	srv.Routes(mux)
	return srv.Authenticate(mux)
}

// doReq 发一个请求，返回状态码与响应体。
func doReq(t *testing.T, h http.Handler, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// signScopedSession 手工签一枚带 rsc 的**会话** token（模拟 deeplink-login 的产物）。
func signScopedSession(t *testing.T, cfg *config.Config, scope int64) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		UserID:      1,
		Username:    "employee",
		RealName:    "只读",
		Role:        model.RoleEmployee,
		RepairScope: scope,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})
	s, err := tok.SignedString([]byte(cfg.Auth.JWTSecret))
	if err != nil {
		t.Fatalf("签会话 token 失败: %v", err)
	}
	return s
}

// signRaw 手工签一枚任意密钥 + 任意用途声明 + 任意有效期的 token，用于构造边界样本。
func signRaw(t *testing.T, key string, c jwt.Claims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	s, err := tok.SignedString([]byte(key))
	if err != nil {
		t.Fatalf("签 token 失败: %v", err)
	}
	return s
}

// —— 用例 1：跨密钥隔离（缺陷 1 的正面验证，§5.4 指定验证方法）——
//
// 拿一枚免登 token 直接调 GET /api/repairs，**必须返回 401**。
func TestDeeplinkTokenRejectedAsSession(t *testing.T) {
	cfg := deeplinkTestConfig()
	srv := NewServer(nil, cfg)
	tok, err := srv.MintDeeplinkToken(1, 7)
	if err != nil {
		t.Fatalf("签发免登 token 失败: %v", err)
	}
	h := srv.Authenticate(stubMux())

	code, body := doReq(t, h, http.MethodGet, "/api/repairs", tok, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("免登 token 直接当会话 token 用：期望 401，实际 %d，body=%s", code, body)
	}
}

// —— 用例 2：用途声明兜底（缺陷 1）——
//
// 构造一枚**用会话密钥 jwt_secret 签、且 typ=yzj_deeplink** 的 token（模拟「有人把两个密钥
// 统一了」的最坏情况）⇒ 中间件必须靠 typ 拒绝，返回 401。
//
// 说明：这枚 token 的签名是**有效**的，因此它命中的正是「独立密钥挡不住」时的兜底分支——
// 删掉中间件的 typ 拒绝后，本用例会变红（见报告「反向验证」）。
func TestMiddlewareRejectsDeeplinkTypClaim(t *testing.T) {
	cfg := deeplinkTestConfig()
	h := newStubHandler(cfg)
	tok := signRaw(t, cfg.Auth.JWTSecret, claims{
		UserID:   1,
		Username: "x",
		Role:     model.RoleEmployee,
		Typ:      deeplinkClaimType,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})

	code, body := doReq(t, h, http.MethodGet, "/api/me", tok, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("typ=yzj_deeplink 的 token：期望 401，实际 %d，body=%s", code, body)
	}
}

// —— 用例 2b：用途声明（任务书原文口径）——
//
// 构造一枚**用 login_token_secret 签、但 typ != yzj_deeplink**（typ="session"）的 token ⇒ 401。
// 该样本被拒的直接原因是「签名与会话密钥对不上」，typ 断言在此路径不参与判定；列此用例以对齐
// 任务书原文，真正的 typ 兜底验证见上一条 TestMiddlewareRejectsDeeplinkTypClaim。
func TestMiddlewareRejectsForeignTypSignedWithDeeplinkKey(t *testing.T) {
	cfg := deeplinkTestConfig()
	h := newStubHandler(cfg)
	tok := signRaw(t, cfg.Yunzhijia.LoginTokenSecret, claims{
		UserID:   1,
		Username: "x",
		Role:     model.RoleEmployee,
		Typ:      "session",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})

	code, body := doReq(t, h, http.MethodGet, "/api/me", tok, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("login_token_secret 签、typ=session 的 token：期望 401，实际 %d，body=%s", code, body)
	}
}

// —— 用例 3 + 5：单据绑定 / 会话收窄（缺陷 2）——
//
// rsc 会话：能读**本单**的详情 / 日志 / 附件，以及 /api/me、/api/enums；
// 对**别的单据**与白名单外路由（/api/assets、/api/employees、/api/vendors）一律 403；
// 写动作（POST /api/repairs/{id}/dispatch）也 403。
func TestDeeplinkScopeNarrowing(t *testing.T) {
	cfg := deeplinkTestConfig()
	h := newStubHandler(cfg)
	tok := signScopedSession(t, cfg, testScopedRepairID)

	allow := []struct{ method, path string }{
		{http.MethodGet, "/api/me"},
		{http.MethodGet, "/api/enums"},
		{http.MethodGet, "/api/repairs/100"},
		{http.MethodGet, "/api/repairs/100/logs"},
		{http.MethodGet, "/api/repairs/100/attachments"},
	}
	for _, c := range allow {
		if code, body := doReq(t, h, c.method, c.path, tok, nil); code != http.StatusOK {
			t.Errorf("收窄会话访问本单路由 %s %s：期望 200，实际 %d，body=%s", c.method, c.path, code, body)
		}
	}

	deny := []struct{ method, path string }{
		{http.MethodGet, "/api/repairs/200"},           // 别的单据
		{http.MethodGet, "/api/repairs/200/logs"},      // 别的单据的日志
		{http.MethodGet, "/api/repairs/100/accept"},    // 本单的写动作（非白名单）
		{http.MethodPost, "/api/repairs/100/dispatch"}, // 本单的写动作
		{http.MethodGet, "/api/assets"},                // 白名单外读
		{http.MethodGet, "/api/repairs"},               // 列表（非单张单）
		{http.MethodGet, "/api/employees"},             // 主数据（刻意不放行）
		{http.MethodGet, "/api/vendors"},               // 主数据（刻意不放行）
	}
	for _, c := range deny {
		if code, body := doReq(t, h, c.method, c.path, tok, nil); code != http.StatusForbidden {
			t.Errorf("收窄会话访问白名单外 %s %s：期望 403，实际 %d，body=%s", c.method, c.path, code, body)
		}
	}
}

// —— 用例 4：TTL（过期 token）——
//
// 过期的免登 token 调 deeplink-login ⇒ 401。
func TestDeeplinkLoginRejectsExpiredToken(t *testing.T) {
	cfg := deeplinkTestConfig()
	srv := NewServer(nil, cfg)
	h := newRealHandler(srv)

	expired := signRaw(t, cfg.Yunzhijia.LoginTokenSecret, deeplinkClaims{
		Typ:      deeplinkClaimType,
		EmpID:    1,
		RepairID: 7,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
	})

	code, body := doReq(t, h, http.MethodPost, "/api/sso/deeplink-login", "", map[string]string{"token": expired})
	if code != http.StatusUnauthorized {
		t.Fatalf("过期免登 token：期望 401，实际 %d，body=%s", code, body)
	}
}

// —— 用例：token 为空 ⇒ 400 ——
func TestDeeplinkLoginEmptyTokenBadRequest(t *testing.T) {
	cfg := deeplinkTestConfig()
	h := newRealHandler(NewServer(nil, cfg))
	code, body := doReq(t, h, http.MethodPost, "/api/sso/deeplink-login", "", map[string]string{"token": ""})
	if code != http.StatusBadRequest {
		t.Fatalf("空 token：期望 400，实际 %d，body=%s", code, body)
	}
}

// —— 用例 6：未配置独立密钥 ⇒ 第 2 层静默禁用（明确状态，不是 500 panic，且不签出 token）——
func TestDeeplinkLoginDisabledWithoutSecret(t *testing.T) {
	cfg := deeplinkTestConfig()
	cfg.Yunzhijia.LoginTokenSecret = ""
	srv := NewServer(nil, cfg)
	h := newRealHandler(srv)

	// 1) MintDeeplinkToken 必须报错，绝不签出 token。
	tok, err := srv.MintDeeplinkToken(1, 7)
	if err == nil || tok != "" {
		t.Fatalf("未配置密钥时 MintDeeplinkToken：期望 (\"\", err)，实际 (%q, %v)", tok, err)
	}

	// 2) deeplink-login 必须回一个明确的状态（503），且不是 500，也不签出 token。
	code, body := doReq(t, h, http.MethodPost, "/api/sso/deeplink-login", "", map[string]string{"token": "whatever"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("未配置密钥时 deeplink-login：期望 503，实际 %d，body=%s", code, body)
	}
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	if _, hasToken := m["token"]; hasToken {
		t.Fatalf("未配置密钥时不得签出 token，实际响应含 token 字段：%s", body)
	}
}

// —— 用例 7：empID / repairID <= 0 ⇒ 拒绝签发 ——
func TestMintDeeplinkTokenRejectsInvalidIDs(t *testing.T) {
	srv := NewServer(nil, deeplinkTestConfig())
	cases := []struct {
		empID, repairID int64
	}{
		{0, 7},
		{-1, 7},
		{1, 0},
		{1, -1},
	}
	for _, c := range cases {
		tok, err := srv.MintDeeplinkToken(c.empID, c.repairID)
		if err == nil || tok != "" {
			t.Errorf("empID=%d repairID=%d：期望报错且不签发，实际 (%q, %v)", c.empID, c.repairID, tok, err)
		}
	}
}

// —— 用例：配置项（§5.4 缺陷 1/3）——
//
// login_token_secret 为空 ⇒ 加载成功（不启动失败）且 TTL 回退 24h；
// deeplink_token_ttl 支持时长字符串；环境变量覆盖优先于 yaml。
func TestConfigDeeplinkTokenDefaultsAndOverrides(t *testing.T) {
	writeCfg := func(t *testing.T, extra string) string {
		t.Helper()
		dir := t.TempDir()
		p := filepath.Join(dir, "config.yaml")
		content := "auth:\n  jwt_secret: \"s\"\n" + extra
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("写临时配置失败: %v", err)
		}
		return p
	}
	clearEnv := func(t *testing.T) {
		t.Helper()
		for _, k := range []string{"YZJ_LOGIN_TOKEN_SECRET", "YZJ_DEEPLINK_TOKEN_TTL", "YZJ_DEEPLINK_TOKEN_TTL_HOURS"} {
			t.Setenv(k, "")
		}
	}

	t.Run("未配置密钥时不报错且TTL回退24h", func(t *testing.T) {
		clearEnv(t)
		cfg, err := config.Load(writeCfg(t, ""))
		if err != nil {
			t.Fatalf("login_token_secret 为空时不应启动失败: %v", err)
		}
		if cfg.Yunzhijia.LoginTokenSecret != "" {
			t.Fatalf("期望空密钥，实际 %q", cfg.Yunzhijia.LoginTokenSecret)
		}
		if cfg.Yunzhijia.DeeplinkTokenTTL != 24*time.Hour {
			t.Fatalf("期望 TTL 回退 24h，实际 %v", cfg.Yunzhijia.DeeplinkTokenTTL)
		}
	})

	t.Run("yaml时长字符串", func(t *testing.T) {
		clearEnv(t)
		cfg, err := config.Load(writeCfg(t, "yunzhijia:\n  deeplink_token_ttl: \"12h\"\n"))
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if cfg.Yunzhijia.DeeplinkTokenTTL != 12*time.Hour {
			t.Fatalf("期望 12h，实际 %v", cfg.Yunzhijia.DeeplinkTokenTTL)
		}
	})

	t.Run("环境变量小时覆盖", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("YZJ_DEEPLINK_TOKEN_TTL_HOURS", "48")
		cfg, err := config.Load(writeCfg(t, "yunzhijia:\n  deeplink_token_ttl: \"12h\"\n"))
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if cfg.Yunzhijia.DeeplinkTokenTTL != 48*time.Hour {
			t.Fatalf("期望环境变量覆盖为 48h，实际 %v", cfg.Yunzhijia.DeeplinkTokenTTL)
		}
	})

	t.Run("环境变量时长覆盖", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("YZJ_DEEPLINK_TOKEN_TTL", "90m")
		cfg, err := config.Load(writeCfg(t, ""))
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if cfg.Yunzhijia.DeeplinkTokenTTL != 90*time.Minute {
			t.Fatalf("期望 90m，实际 %v", cfg.Yunzhijia.DeeplinkTokenTTL)
		}
	})

	t.Run("非法TTL忽略并回退", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("YZJ_DEEPLINK_TOKEN_TTL_HOURS", "abc")
		cfg, err := config.Load(writeCfg(t, ""))
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		if cfg.Yunzhijia.DeeplinkTokenTTL != 24*time.Hour {
			t.Fatalf("非法 TTL 应忽略并回退 24h，实际 %v", cfg.Yunzhijia.DeeplinkTokenTTL)
		}
	})
}

// 用一个**已有账号**的员工（避免建号污染库）签发免登 token → deeplink-login 换会话
// → 会话能过中间件读本单（非 401/403），但读白名单外路由 403。
// —— 用例 8：端到端换会话（连真库；连不上则 Skip）——
//
// 用一个**已有账号**的员工（避免建号污染库）签发免登 token → deeplink-login 换会话
// → 会话能过中间件读本单（非 401/403），但读白名单外路由 403。
func TestDeeplinkLoginEndToEnd(t *testing.T) {
	st, cfg := mustTestStore(t)
	cfg.Yunzhijia.LoginTokenSecret = testDeeplinkSecret
	cfg.Yunzhijia.DeeplinkTokenTTL = time.Hour

	// 选一个已有启用账号的员工，确保 resolveOrCreateSSOUser 走「查得到」分支，不建号。
	var empID int64
	if err := st.DB().QueryRow(
		"SELECT employee_id FROM sys_user WHERE employee_id IS NOT NULL AND employee_id > 0 AND enabled=1 ORDER BY id LIMIT 1").
		Scan(&empID); err != nil {
		t.Skipf("库里没有已绑员工的账号，跳过端到端用例: %v", err)
	}

	srv := NewServer(st, cfg)
	// 换会话走真实路由（deeplink-login）；随后用桩路由校验收窄会话，保证断言确定、不依赖单据数据。
	realH := newRealHandler(srv)
	stubH := NewServer(nil, cfg).Authenticate(stubMux())

	tok, err := srv.MintDeeplinkToken(empID, testScopedRepairID)
	if err != nil {
		t.Fatalf("签发免登 token 失败: %v", err)
	}

	code, body := doReq(t, realH, http.MethodPost, "/api/sso/deeplink-login", "", map[string]string{"token": tok})
	if code != http.StatusOK {
		t.Fatalf("deeplink-login：期望 200，实际 %d，body=%s", code, body)
	}
	var resp struct {
		Token string      `json:"token"`
		User  *model.User `json:"user"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("解析响应失败: %v，body=%s", err, body)
	}
	if resp.Token == "" || resp.User == nil {
		t.Fatalf("响应形状应为 {token,user}，实际 body=%s", body)
	}
	if resp.User.EmployeeID != empID {
		t.Fatalf("换回的 user.employee_id 应为 %d，实际 %d", empID, resp.User.EmployeeID)
	}

	// 收窄会话读本单：中间件应放行（桩路由返回 200）。
	if c, b := doReq(t, stubH, http.MethodGet, "/api/repairs/100", resp.Token, nil); c != http.StatusOK {
		t.Errorf("收窄会话读本单：期望 200，实际 %d，body=%s", c, b)
	}
	// 收窄会话读白名单外：403。
	if c, b := doReq(t, stubH, http.MethodGet, "/api/assets", resp.Token, nil); c != http.StatusForbidden {
		t.Errorf("收窄会话读白名单外：期望 403，实际 %d，body=%s", c, b)
	}
}
