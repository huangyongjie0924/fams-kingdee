package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"asset-mgr/integration/yunzhijia"
	"asset-mgr/store"
)

// —— 路径 A：免登时回填 employee.open_id（§6.4）——
//
// 为什么值得单测：openId 是待办通知的**收件人标识**，缺它就只能跳过该收件人（§3.1）。
// 文档明确「SSO 回填是 openId 第一来源，通讯录同步只补覆盖从未免登过的人」，
// 但这条路径此前**没有任何测试**。
//
// 它悄悄坏掉的表现极具迷惑性：免登照常成功、页面照常打开、所有既有测试照常全绿，
// 唯一的变化是「通知永远发不出去」——而那时 openId 覆盖率为 0 的原因会被误判成
// 「大家都还没免登过」。这正是需要一条红灯来区分的情形。

// fakeSsoYzj 是只服务「取 app token」与「ticket 换用户信息」两个接口的假云之家。
// 刻意不复用 notify_test.go 的 fakeYzj：那个服务于待办接口，混在一起会让两边
// 的用例都变脆。
type fakeSsoYzj struct {
	srv    *httptest.Server
	jobNo  string
	openID string
	// hits 记录 acquirecontext 被调用的次数，用来确认请求真的走到了云之家，
	// 而不是因为某个前置校验提前返回、让用例变成「空跑也绿」。
	hits int
}

func newFakeSsoYzj(t *testing.T, jobNo, openID string) *fakeSsoYzj {
	t.Helper()
	f := &fakeSsoYzj{jobNo: jobNo, openID: openID}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/gateway/oauth2/token/getAccessToken":
			fmt.Fprint(w, `{"data":{"accessToken":"FAKE","refreshToken":"rt","expireIn":7200},"success":true,"errorCode":200}`)
		case "/gateway/ticket/user/acquirecontext":
			f.hits++
			// 注意 json tag 是 openid（全小写、无下划线），不是 open_id——写错会静默解析为空串，
			// 于是「回填」变成「什么都没做」，用例却可能因为断言太松而通过。
			b, _ := json.Marshal(map[string]any{
				"success":   "true",
				"errorCode": 200,
				"data": map[string]any{
					"username": "ZZ 免登探针",
					"jobNo":    f.jobNo,
					"openid":   f.openID,
					"eid":      "eid-probe",
				},
			})
			w.Write(b)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errorCode":404,"success":"false"}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// ssoProbe 建一个探针员工（无 open_id）并返回 (st, srv, empID)。
func ssoProbe(t *testing.T, openID string) (*store.Store, *Server, int64, *fakeSsoYzj) {
	t.Helper()
	st, cfg := mustTestStore(t)

	empNo := fmt.Sprintf("ZZ-SSO-OPENID-%d", time.Now().UnixNano())
	res, err := st.DB().Exec(`INSERT INTO employee (emp_no, name, active) VALUES (?, 'ZZ 免登探针', 1)`, empNo)
	if err != nil {
		t.Fatalf("建探针员工失败: %v", err)
	}
	empID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("取探针员工 id 失败: %v", err)
	}
	t.Cleanup(func() {
		// 顺带清掉可能被自动创建的账号，避免留垃圾。
		_, _ = st.DB().Exec("DELETE FROM sys_user WHERE employee_id = ?", empID)
		_, _ = st.DB().Exec("DELETE FROM employee WHERE id = ?", empID)
	})

	fake := newFakeSsoYzj(t, empNo, openID)
	srv := NewServer(st, cfg)
	srv.SetYunzhijiaClient(yunzhijia.NewClient(fake.srv.URL, "500000084", "test-secret", 5*time.Second))
	return st, srv, empID, fake
}

// ssoLogin 走真实路由 POST /api/sso/login，返回状态码。
func ssoLogin(t *testing.T, srv *Server) int {
	t.Helper()
	mux := http.NewServeMux()
	srv.Routes(mux)
	body, _ := json.Marshal(map[string]any{"ticket": "probe-ticket"})
	req := httptest.NewRequest(http.MethodPost, "/api/sso/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Authenticate(mux).ServeHTTP(rec, req)
	return rec.Code
}

// TestSSOLoginBackfillsOpenID 是路径 A 的正面用例：免登成功后 employee.open_id 必须被写入。
//
// 反向验证：删掉 api/sso.go 里的 `if u.OpenID != "" { UpdateEmployeeOpenID(...) }` 块
// ⇒ 本用例变红（open_id 仍为空）。
func TestSSOLoginBackfillsOpenID(t *testing.T) {
	probeOpenID := fmt.Sprintf("probe-openid-%d", time.Now().UnixNano())
	st, srv, empID, fake := ssoProbe(t, probeOpenID)

	// 前置断言：探针员工初始确实没有 open_id，否则「写入成功」无法与「本来就是它」区分。
	if got, err := st.EmployeeOpenID(empID); err != nil {
		t.Fatalf("读初始 open_id 失败: %v", err)
	} else if got != "" {
		t.Fatalf("探针员工初始 open_id 非空（%q），本用例无法区分是否真的写入了", got)
	}

	if code := ssoLogin(t, srv); code != http.StatusOK {
		t.Fatalf("免登应成功，实际 HTTP %d", code)
	}
	if fake.hits == 0 {
		t.Fatal("云之家的 ticket 换用户信息接口没被调用 —— 用例是空跑，结论不可信")
	}

	got, err := st.EmployeeOpenID(empID)
	if err != nil {
		t.Fatalf("读回填后的 open_id 失败: %v", err)
	}
	if got != probeOpenID {
		t.Errorf("路径 A 未回填 open_id：期望 %q，实际 %q（免登成功但通知会永远发不出去）",
			probeOpenID, got)
	}
}

// TestSSOLoginSucceedsWithoutOpenID 覆盖「云之家没返回 openId」的情形。
//
// 这条守的是两件事：① 登录**不得**因为缺 openId 而失败（§7 失败隔离：
// openId 只是通知链路的前置数据，不能把人挡在门外）；② 缺值时不做无意义的写。
func TestSSOLoginSucceedsWithoutOpenID(t *testing.T) {
	st, srv, empID, fake := ssoProbe(t, "") // openid 为空

	if code := ssoLogin(t, srv); code != http.StatusOK {
		t.Fatalf("云之家未返回 openId 时免登仍应成功，实际 HTTP %d", code)
	}
	if fake.hits == 0 {
		t.Fatal("云之家的 ticket 换用户信息接口没被调用 —— 用例是空跑")
	}

	got, err := st.EmployeeOpenID(empID)
	if err != nil {
		t.Fatalf("读 open_id 失败: %v", err)
	}
	if got != "" {
		t.Errorf("云之家未返回 openId 时不应写入任何值，实际 open_id=%q", got)
	}
}
