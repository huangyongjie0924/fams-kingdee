package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"asset-mgr/config"
	"asset-mgr/model"
)

// —— 集成点 1：待办 URL 嵌第 2 层免登 token（§5.4）——
//
// 这里守的是「签发出来的深链到底长什么样、token 绑定了谁」——即 notify 侧与 deeplink 侧
// 的接缝。deeplink 侧的验签/拒绝逻辑已在 deeplink_test.go 与 deeplink_keysep_test.go 覆盖，
// 本文件只负责接缝本身。

// deeplinkNotifyConfig 构造最小通知配置。
// TodoEnabled=false 且 yzj=nil ⇒ NewNotifyService 不会起 worker goroutine，测试无泄漏。
func deeplinkNotifyConfig() *config.Config {
	var c config.Config
	c.Auth.JWTSecret = testJWTSecret
	c.Yunzhijia.PortalBaseURL = "https://fams.example.com:8448"
	c.Yunzhijia.LoginTokenSecret = testDeeplinkSecret
	c.Yunzhijia.DeeplinkTokenTTL = 24 * time.Hour
	c.Yunzhijia.TodoEnabled = false
	return &c
}

// splitDeepLink 把深链拆成 URL 与「hash 内的查询串」，并顺带断言凭据**不在** `#` 之前。
//
// 这条断言是设计取舍的一部分，不是格式洁癖：片段（fragment）不会被浏览器发进 HTTP 请求行，
// 所以放 `#` 之内可让凭据避开服务端访问日志与 Referer（§5.6）。若有人把 token 挪到 `#` 之前，
// 功能仍"能用"（前端 readParam 两处都读），但安全性悄悄降级——必须由测试拦下。
func splitDeepLink(t *testing.T, raw string) (*url.URL, url.Values) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("深链不是合法 URL: %v（raw=%s）", err, raw)
	}
	if u.RawQuery != "" {
		t.Fatalf("凭据出现在 `#` 之前的查询串里（RawQuery=%q）——这会让凭据进服务端日志与 Referer", u.RawQuery)
	}
	i := strings.Index(u.Fragment, "?")
	if i < 0 {
		return u, url.Values{}
	}
	q, err := url.ParseQuery(u.Fragment[i+1:])
	if err != nil {
		t.Fatalf("hash 内查询串解析失败: %v", err)
	}
	return u, q
}

// TestDeepLinkEmbedsRecipientBoundToken 断言深链的形态与 token 的绑定对象。
//
// 反向验证：
//   - 把 api/notify.go 的 deepLinkIMTodo 改成不嵌 token ⇒ 本用例「没有 yzj_token」变红；
//   - 把 deepLinkIMTodo 的 empID 参数写成 repairID 或常量 ⇒ 「eid 必须等于收件人」变红；
//   - 把 token 挪到 `#` 之前 ⇒ splitDeepLink 的 RawQuery 断言变红。
func TestDeepLinkEmbedsRecipientBoundToken(t *testing.T) {
	cfg := deeplinkNotifyConfig()
	svc := NewNotifyService(nil, cfg, nil) // 走真实构造函数，连带验证字段确实被接上

	const (
		repairID = int64(100)
		empID    = int64(127)
	)
	raw := svc.deepLinkIMTodo(repairID, empID)

	u, q := splitDeepLink(t, raw)

	if got, want := u.Scheme+"://"+u.Host+u.Path, "https://fams.example.com:8448/"; got != want {
		t.Errorf("深链前缀 = %q, 期望 %q", got, want)
	}
	// 前端是 hash 路由（createWebHashHistory），路由必须是 hash 里的 /repairs/{id}。
	if got, want := strings.SplitN(u.Fragment, "?", 2)[0], "/repairs/100"; got != want {
		t.Errorf("hash 路由 = %q, 期望 %q（不能写成 /repairs/{id} 路径形式）", got, want)
	}

	tok := q.Get("yzj_token")
	if tok == "" {
		t.Fatalf("深链里没有 yzj_token：%s", raw)
	}

	// ① 必须用**独立密钥**验签通过。
	var dc deeplinkClaims
	parsed, err := jwt.ParseWithClaims(tok, &dc, func(*jwt.Token) (any, error) {
		return []byte(cfg.Yunzhijia.LoginTokenSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		t.Fatalf("深链 token 用独立密钥验签失败: %v", err)
	}
	if dc.Typ != deeplinkClaimType {
		t.Errorf("typ = %q, 期望 %q", dc.Typ, deeplinkClaimType)
	}
	// ② 必须绑定**收件人**：否则 A 收到的待办被转发给 B，B 也能免登进来看这张单。
	if dc.EmpID != empID {
		t.Errorf("eid = %d, 期望 %d（token 必须绑定收件人）", dc.EmpID, empID)
	}
	if dc.RepairID != repairID {
		t.Errorf("rid = %d, 期望 %d", dc.RepairID, repairID)
	}
	if dc.ExpiresAt == nil {
		t.Fatal("token 没有 exp")
	}
	if d := (time.Until(dc.ExpiresAt.Time) - 24*time.Hour).Abs(); d > time.Minute {
		t.Errorf("有效期偏离 24h 达 %v（exp=%v）", d, dc.ExpiresAt.Time)
	}

	// ③ 必须**不能**用会话密钥验签（缺陷 1：独立密钥）。
	if _, err := jwt.ParseWithClaims(tok, &deeplinkClaims{}, func(*jwt.Token) (any, error) {
		return []byte(cfg.Auth.JWTSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"})); err == nil {
		t.Error("深链 token 竟能用 auth.jwt_secret 验签通过 —— 独立密钥隔离失效")
	}
}

// TestDeepLinkBindsEachRecipientSeparately 同一张单、不同收件人必须拿到不同 token。
// 若 deepLink 漏传/错传 empID（例如传了 repairID 或常量），本用例会红。
func TestDeepLinkBindsEachRecipientSeparately(t *testing.T) {
	svc := NewNotifyService(nil, deeplinkNotifyConfig(), nil)

	_, qa := splitDeepLink(t, svc.deepLinkIMTodo(100, 127))
	_, qb := splitDeepLink(t, svc.deepLinkIMTodo(100, 128))
	ta, tb := qa.Get("yzj_token"), qb.Get("yzj_token")
	if ta == "" || tb == "" {
		t.Fatalf("token 缺失：a=%q b=%q", ta, tb)
	}
	if ta == tb {
		t.Error("不同收件人拿到同一个 token —— token 未绑定收件人")
	}

	// 再验一层：两个 token 各自解析出的 eid 必须与传入一致。
	for empID, raw := range map[int64]string{127: ta, 128: tb} {
		var dc deeplinkClaims
		if _, err := jwt.ParseWithClaims(raw, &dc, func(*jwt.Token) (any, error) {
			return []byte(testDeeplinkSecret), nil
		}, jwt.WithValidMethods([]string{"HS256"})); err != nil {
			t.Fatalf("empID=%d 的 token 解析失败: %v", empID, err)
		}
		if dc.EmpID != empID {
			t.Errorf("empID=%d 的 token 里 eid=%d", empID, dc.EmpID)
		}
	}
}

// TestDeepLinkDegradesWithoutSecret 未配置独立密钥时（第 2 层按设计静默禁用），
// 深链必须降级为**裸链接**而不是报错或发出半截 URL——否则待办会「点不开」。
func TestDeepLinkDegradesWithoutSecret(t *testing.T) {
	cfg := deeplinkNotifyConfig()
	cfg.Yunzhijia.LoginTokenSecret = ""
	svc := NewNotifyService(nil, cfg, nil)

	got := svc.deepLinkIMTodo(100, 127)
	want := "https://fams.example.com:8448/#/repairs/100"
	if got != want {
		t.Errorf("未配置密钥时应降级为裸链接\n got=%q\nwant=%q", got, want)
	}
}

// TestDeepLinkBaseURLTrailingSlash 门户地址带尾斜杠时不能拼出 `//#/`。
func TestDeepLinkBaseURLTrailingSlash(t *testing.T) {
	cfg := deeplinkNotifyConfig()
	cfg.Yunzhijia.PortalBaseURL = "https://fams.example.com:8448/"
	svc := NewNotifyService(nil, cfg, nil)

	u, _ := splitDeepLink(t, svc.deepLinkIMTodo(100, 127))
	if u.Path != "/" || strings.Contains(u.Path, "//") {
		t.Errorf("尾斜杠未归一化，Path=%q（raw=%s）", u.Path, svc.deepLinkIMTodo(100, 127))
	}
}

// —— 集成点 2：loadRepairInScope 的对象级收窄校验（§5.4 缺陷 2）——
//
// 中间件按**路径字符串**拦一层，本函数按**实际取到的行**再拦一层。
// 本用例直接调 loadRepairInScope 并手工把 rsc 放进 context —— 这是唯一能单独考到
// 「对象级」这一层的方式（走完整 HTTP 时中间件会先把请求拦掉，考不到它）。
//
// 数据策略：**自己写库造一张探针单**，而不是「查库里现成的一张」。后者会让用例在空库上
// 静默 Skip —— 一个被跳过的安全用例等于没有用例，而这里恰恰是要证明越权会被拦。
func TestLoadRepairInScopeRejectsForeignRepair(t *testing.T) {
	st, cfg := mustTestStore(t)

	// 字段与 repair_qa_api_test.go 的探针单保持一致，便于对照。
	res, err := st.DB().Exec(`INSERT INTO repair_order
		(card_id, asset_code, asset_name, reporter_emp_id, reporter_name, use_dept_id, use_dept_name,
		 fault_desc, urgency, status, created_by)
		VALUES (0, ?, ?, 0, '', 0, '', ?, 'normal', ?, 'deeplink-scope-probe')`,
		"DL-SCOPE-PROBE", "深链收窄探针", "深链收窄探针", model.RepairPending)
	if err != nil {
		t.Fatalf("建探针维修单失败: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("取探针单 id 失败: %v", err)
	}
	t.Cleanup(func() {
		st.DB().Exec("DELETE FROM doc_status_log WHERE doc_type=? AND doc_id=?", model.DocTypeRepair, id)
		st.DB().Exec("DELETE FROM repair_attachment WHERE repair_id=?", id)
		st.DB().Exec("DELETE FROM repair_order WHERE id=?", id)
	})

	srv := NewServer(st, cfg)

	call := func(rsc int64) (int, string) {
		raw := strconv.FormatInt(id, 10)
		req := httptest.NewRequest(http.MethodGet, "/api/repairs/"+raw, nil)
		req.SetPathValue("id", raw)
		// 注入一个**不限范围**的角色（admin）：本用例考的是「对象级收窄」这一层（rsc vs 行 ID），
		// 不该被角色可见范围层干扰。历史上前者直接省了注入 User——那是依赖了「空角色=不限」这个
		// fail-open（api/scope.go 已按 P0 改为 fail-closed），不注入就会被 403，掩盖本层要断言的越权拦截。
		req = req.WithContext(context.WithValue(req.Context(), userKey,
			model.User{ID: 1, Username: "admin", RealName: "管理员", Role: model.RoleAdmin}))
		if rsc > 0 {
			req = req.WithContext(context.WithValue(req.Context(), repairScopeKey, rsc))
		}
		w := httptest.NewRecorder()
		o, ok := srv.loadRepairInScope(w, req)
		if ok && o == nil {
			t.Fatal("ok=true 但返回的单据为 nil")
		}
		return w.Code, w.Body.String()
	}

	// ① rsc=0（非免登会话）：本层不参与，不得误拦。
	if code, body := call(0); code == http.StatusForbidden {
		t.Errorf("无收窄会话被本层误拦：%d %s", code, body)
	}
	// ② rsc == 本单 id：放行（正常免登访问）。
	if code, body := call(id); code == http.StatusForbidden {
		t.Errorf("rsc 等于本单 id 时被误拦：%d %s", code, body)
	}
	// ③ rsc 指向**别的**单据：必须 403（IDOR）。
	if code, body := call(id + 999999); code != http.StatusForbidden {
		t.Errorf("rsc 指向别的单据时未被拦：期望 403，实际 %d %s", code, body)
	}
}
