package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"asset-mgr/config"
	"asset-mgr/integration/yunzhijia"
	"asset-mgr/model"
	"asset-mgr/store"
)

// 本文件用 httptest.NewServer 造假云之家（不依赖真实云之家），覆盖 docs/云之家待办通知集成方案.md
// §10.3 的集成场景。需要真库（沿用 qaSetup 的「真库 + httptest 记录请求」风格）；连不上则跳过。

// —— 假云之家 ——

type recordedReq struct {
	path string
	body map[string]any
	raw  string
}

type fakeYzj struct {
	srv      *httptest.Server
	mu       sync.Mutex
	reqs     []recordedReq
	failHTTP atomic.Int64 // 非 0 时待办接口返回该 HTTP 状态码（模拟云之家不可用）
}

func newFakeYzj(t *testing.T) *fakeYzj {
	t.Helper()
	f := &fakeYzj{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeYzj) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	f.mu.Lock()
	f.reqs = append(f.reqs, recordedReq{path: r.URL.Path, body: m, raw: string(raw)})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	fail := f.failHTTP.Load() != 0
	switch r.URL.Path {
	case "/gateway/oauth2/token/getAccessToken":
		fmt.Fprint(w, `{"data":{"accessToken":"FAKE-TOKEN","refreshToken":"rt","expireIn":7200},"success":true,"errorCode":200}`)
	case "/gateway/newtodo/open/generatetodo.json":
		if fail {
			w.WriteHeader(int(f.failHTTP.Load()))
			fmt.Fprint(w, `{"errorCode":500,"success":"false","error":"boom"}`)
			return
		}
		fmt.Fprint(w, `{"data":{},"success":"true","errorCode":200,"error":"待办创建成功"}`)
	case "/gateway/newtodo/open/action.json":
		if fail {
			w.WriteHeader(int(f.failHTTP.Load()))
			fmt.Fprint(w, `{"errorCode":500,"success":"false","error":"boom"}`)
			return
		}
		fmt.Fprint(w, `{"data":{},"success":"true","errorCode":200,"error":"待办删除成功"}`)
	// —— 统一流程中心（flowcenter）桩 ——
	case "/gateway/flowcenter/native/todo/send":
		if fail {
			w.WriteHeader(int(f.failHTTP.Load()))
			fmt.Fprint(w, `{"errorCode":500,"success":false,"error":"boom"}`)
			return
		}
		fmt.Fprint(w, `{"data":null,"success":true,"errorCode":0,"error":""}`)
	case "/gateway/flowcenter/native/todo/done":
		fmt.Fprint(w, `{"data":null,"success":true,"errorCode":0,"error":""}`)
	case "/gateway/flowcenter/native/instance/finish":
		fmt.Fprint(w, `{"data":null,"success":true,"errorCode":0,"error":""}`)
	case "/gateway/flowcenter/native/instance/delete":
		fmt.Fprint(w, `{"data":null,"success":true,"errorCode":200,"error":""}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errorCode":404,"success":"false"}`)
	}
}

func (f *fakeYzj) snapshot() []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedReq, len(f.reqs))
	copy(out, f.reqs)
	return out
}

func (f *fakeYzj) reset() {
	f.mu.Lock()
	f.reqs = nil
	f.mu.Unlock()
}

func (f *fakeYzj) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// creates 返回针对该 sourceId 的 generatetodo 请求。
func (f *fakeYzj) creates(sourceID string) []recordedReq {
	var out []recordedReq
	for _, r := range f.snapshot() {
		if r.path == "/gateway/newtodo/open/generatetodo.json" && str(r.body, "sourceId") == sourceID {
			out = append(out, r)
		}
	}
	return out
}

// actions 返回针对该 sourceId 的 action 请求。
func (f *fakeYzj) actions(sourceID string) []recordedReq {
	var out []recordedReq
	for _, r := range f.snapshot() {
		if r.path == "/gateway/newtodo/open/action.json" && str(r.body, "sourceitemid") == sourceID {
			out = append(out, r)
		}
	}
	return out
}

// —— 请求体断言小工具 ——

func openIDInField(body map[string]any, field, openID string) bool {
	arr, ok := body[field].([]any)
	if !ok {
		return false
	}
	for _, v := range arr {
		if s, _ := v.(string); s == openID {
			return true
		}
	}
	return false
}

func findCreate(reqs []recordedReq, openID string) *recordedReq {
	for i := range reqs {
		if openIDInField(reqs[i].body, "openId", openID) {
			return &reqs[i]
		}
	}
	return nil
}

func actionTypeOf(r recordedReq) map[string]any {
	at, _ := r.body["actiontype"].(map[string]any)
	return at
}

// findActionWith 找一条对该 openID 生效、且 actiontype 含给定键（read/deal/delete）的 action。
func findActionWith(reqs []recordedReq, openID, key string) *recordedReq {
	for i := range reqs {
		at := actionTypeOf(reqs[i])
		if at == nil {
			continue
		}
		if _, ok := at[key]; !ok {
			continue
		}
		if openIDInField(reqs[i].body, "openids", openID) {
			return &reqs[i]
		}
	}
	return nil
}

// findDeleteAll 找一条「不填 openids 的 delete」（撤单删整批）。
func findDeleteAll(reqs []recordedReq) *recordedReq {
	for i := range reqs {
		at := actionTypeOf(reqs[i])
		if at == nil {
			continue
		}
		if _, ok := at["delete"]; !ok {
			continue
		}
		if _, hasOpen := reqs[i].body["openids"]; !hasOpen {
			return &reqs[i]
		}
	}
	return nil
}

// —— 测试脚手架 ——

type notifyH struct {
	t       *testing.T
	st      *store.Store
	cfg     *config.Config
	srv     *Server
	svc     *NotifyService
	fake    *fakeYzj
	handler http.Handler
}

func notifySetup(t *testing.T, todoEnabled bool, fake *fakeYzj) *notifyH {
	t.Helper()
	st, cfg := mustTestStore(t)

	cfg.Yunzhijia.AppID = "500000084"
	cfg.Yunzhijia.Secret = "test-secret"
	cfg.Yunzhijia.BaseURL = fake.srv.URL
	cfg.Yunzhijia.TodoEnabled = todoEnabled
	cfg.Yunzhijia.TodoAppID = cfg.Yunzhijia.AppID
	cfg.Yunzhijia.HeadImg = "https://img.example.com/head.png"
	cfg.Yunzhijia.PortalBaseURL = "https://fams.example.com"
	// ⚠️ 必须显式钉死，不能继承 config.yaml 的值：生产配置 informational_enabled=true 是有意开启的
	// （见 config.yaml:92），但本 harness 的默认语义是「信息性告知关闭」（与 config/config.go:86 的
	// 默认值一致）。TestNotifyFullFlow 断言「默认（informational=false）不应发任何进展告知」，
	// 若继承环境里的 true，会把一条与环境耦合的假失败当成代码 bug。
	// 需要打开该开关的用例（TestNotifyAcceptInformationalSwitch）自行置 h.svc.informational=true 覆盖。
	cfg.Yunzhijia.InformationalEnabled = false
	// 同类隐患：流程中心总开关也必须钉死。enabled 与「新待办通道」都由它参与决定
	// （notify.go:133 的 enabled、newTaskChannel 的分派）。config.yaml 一旦把
	// flowcenter_enabled 置 true，本文件大量基于 im-todo 通道 create/read 的断言会整体错位成假失败。
	cfg.Yunzhijia.FlowCenterEnabled = false
	// 同类隐患：深链签名密钥。harness 必须与环境 config.yaml / YZJ_LOGIN_TOKEN_SECRET 解耦，
	// 否则开发机一旦配了密钥，深链相关行为会随环境漂移。置空 = 复现「未配置」的测试基线。
	cfg.Yunzhijia.LoginTokenSecret = ""

	yzj := yunzhijia.NewClient(cfg.Yunzhijia.BaseURL, cfg.Yunzhijia.AppID, cfg.Yunzhijia.Secret, 5*time.Second)
	svc := NewNotifyService(st, cfg, yzj)
	svc.maxAttempts = 1 // 测试里不做退避重试，失败即落库
	svc.backoff = 0

	srv := NewServer(st, cfg)
	srv.SetNotifyService(svc)

	mux := http.NewServeMux()
	srv.Routes(mux)
	h := &notifyH{t: t, st: st, cfg: cfg, srv: srv, svc: svc, fake: fake, handler: srv.Authenticate(mux)}
	t.Cleanup(func() { svc.Stop(time.Second) })
	return h
}

func (h *notifyH) token(u model.User) string {
	h.t.Helper()
	tok, err := h.srv.issueToken(&u)
	if err != nil {
		h.t.Fatalf("签发 token 失败: %v", err)
	}
	return tok
}

func (h *notifyH) do(method, path, token string, body any) (int, map[string]any, []byte) {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m, rec.Body.Bytes()
}

// wait 等待通知队列排空（异步投递）。
func (h *notifyH) wait() {
	h.t.Helper()
	if !h.svc.WaitIdle(5 * time.Second) {
		h.t.Fatalf("通知队列未在超时前排空")
	}
}

// taskState 读本地 repair_notify_task.state（不存在返回空串），用于断言本地状态机。
func (h *notifyH) taskState(repairID, empID int64, sourceID string) string {
	h.t.Helper()
	var st string
	if err := h.st.DB().QueryRow(
		`SELECT state FROM repair_notify_task WHERE repair_id=? AND emp_id=? AND source_id=?`,
		repairID, empID, sourceID).Scan(&st); err != nil {
		return ""
	}
	return st
}

func (h *notifyH) newEmployee(name, openID string) int64 {
	h.t.Helper()
	res, err := h.st.DB().Exec(`INSERT INTO employee (emp_no, name, active, open_id) VALUES (?, ?, 1, ?)`,
		fmt.Sprintf("QA-NOTIFY-%d", time.Now().UnixNano()), name, openID)
	if err != nil {
		h.t.Fatalf("建探针员工失败: %v", err)
	}
	id, _ := res.LastInsertId()
	h.t.Cleanup(func() { h.st.DB().Exec("DELETE FROM employee WHERE id=?", id) })
	return id
}

func (h *notifyH) newAssetManager(username string, empID int64) int64 {
	h.t.Helper()
	id, err := h.st.CreateUser(username, "qa-pass-123", "QA-"+username, []string{model.RoleAssetManager}, empID, 0, true)
	if err != nil {
		h.t.Fatalf("建探针资产管理员失败: %v", err)
	}
	h.t.Cleanup(func() { h.st.DB().Exec("DELETE FROM sys_user WHERE id=?", id) })
	return id
}

func (h *notifyH) newVendor(name string) int64 {
	h.t.Helper()
	res, err := h.st.DB().Exec(`INSERT INTO vendor (name) VALUES (?)`, name)
	if err != nil {
		h.t.Fatalf("建探针供应商失败: %v", err)
	}
	id, _ := res.LastInsertId()
	h.t.Cleanup(func() { h.st.DB().Exec("DELETE FROM vendor WHERE id=?", id) })
	return id
}

func (h *notifyH) newCard(code string) int64 {
	h.t.Helper()
	var catID int64
	if err := h.st.DB().QueryRow("SELECT id FROM asset_category WHERE repairable = 1 ORDER BY id LIMIT 1").Scan(&catID); err != nil {
		h.t.Fatalf("取可维修分类失败（需求 A 前置：库里应至少有一个 repairable=1 的分类）: %v", err)
	}
	res, err := h.st.DB().Exec(`INSERT INTO asset_card (asset_code, name, status, biz_status, category_id, created_by)
		VALUES (?, ?, ?, '', ?, 'qa')`, code, "QA待办探针卡", model.StatusInUse, catID)
	if err != nil {
		h.t.Fatalf("建探针卡失败: %v", err)
	}
	id, _ := res.LastInsertId()
	h.t.Cleanup(func() {
		rows, _ := h.st.DB().Query("SELECT id FROM repair_order WHERE card_id = ?", id)
		if rows != nil {
			var ids []int64
			for rows.Next() {
				var oid int64
				_ = rows.Scan(&oid)
				ids = append(ids, oid)
			}
			rows.Close()
			for _, oid := range ids {
				h.st.DB().Exec("DELETE FROM repair_notify_task WHERE repair_id=?", oid)
				h.st.DB().Exec("DELETE FROM doc_status_log WHERE doc_type=? AND doc_id=?", model.DocTypeRepair, oid)
				h.st.DB().Exec("DELETE FROM repair_attachment WHERE repair_id=?", oid)
			}
		}
		h.st.DB().Exec("DELETE FROM repair_order WHERE card_id=?", id)
		h.st.DB().Exec("DELETE FROM asset_history WHERE card_id=?", id)
		h.st.DB().Exec("DELETE FROM asset_card WHERE id=?", id)
	})
	return id
}

// actors 是一组探针身份：报修人 / 受理人 / 维护人员，各自带 openId 与登录 token。
type actors struct {
	cardID                           int64
	mgrEmp, techEmp, repEmp          int64
	mgrOpenID, techOpenID, repOpenID string
	mgrTok, techTok, repTok          string
}

func (h *notifyH) newActors(mgrOpenID, techOpenID, repOpenID string) actors {
	h.t.Helper()
	ns := time.Now().UnixNano()
	rep := h.newEmployee("QA报修人", repOpenID)
	tech := h.newEmployee("QA维护人员", techOpenID)
	mgr := h.newEmployee("QA受理人", mgrOpenID)
	h.newAssetManager(fmt.Sprintf("qa-notify-mgr-%d", ns), mgr)
	card := h.newCard(fmt.Sprintf("QA-NOTIFY-%d", ns))
	return actors{
		cardID: card, mgrEmp: mgr, techEmp: tech, repEmp: rep,
		mgrOpenID: mgrOpenID, techOpenID: techOpenID, repOpenID: repOpenID,
		mgrTok:  h.token(model.User{ID: 1, Username: "qa-mgr", RealName: "QA受理人", Role: model.RoleAssetManager, EmployeeID: mgr}),
		techTok: h.token(model.User{ID: 2, Username: "qa-tech", RealName: "QA维护人员", Role: model.RoleMaintainer, EmployeeID: tech}),
		repTok:  h.token(model.User{ID: 3, Username: "qa-rep", RealName: "QA报修人", Role: model.RoleEmployee, EmployeeID: rep}),
	}
}

// createRepair 走 API 建一张探针维修单，返回 (orderID, sourceID)。
func (h *notifyH) createRepair(a actors, fault, urgency string) (int64, string) {
	h.t.Helper()
	body := map[string]any{"card_id": a.cardID, "fault_desc": fault}
	if urgency != "" {
		body["urgency"] = urgency
	}
	code, m, _ := h.do("POST", "/api/repairs", a.repTok, body)
	if code != 200 {
		h.t.Fatalf("提交报修失败: %d %s", code, str(m, "error"))
	}
	oid := int64(m["id"].(float64))
	h.wait()
	return oid, notifySourceID(oid)
}

// TestNotifyFullFlow 报修 → 受理 → 派工 → 接单 → 报完工 → 确认，逐步断言待办调用（§10.3）。
func TestNotifyFullFlow(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, true, fake)
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")

	// —— 提交报修 → 受理人 create ——
	oid, src := h.createRepair(a, "异响停机", "high")
	creates := fake.creates(src)
	mg := findCreate(creates, a.mgrOpenID)
	if mg == nil {
		t.Fatalf("受理人应收到 create，实际 creates=%d", len(creates))
	}
	if got := str(mg.body, "sourceId"); got != src {
		t.Errorf("sourceId 应为 %q，实际 %q", src, got)
	}
	wantURL := fmt.Sprintf("https://fams.example.com/#/repairs/%d", oid)
	if got := str(mg.body, "url"); got != wantURL {
		t.Errorf("url 应为 %q（hash 路由），实际 %q", wantURL, got)
	}
	content := str(mg.body, "content")
	if !strings.Contains(content, "QA报修人") || !strings.Contains(content, "异响停机") {
		t.Errorf("intake content 应含报修人与故障描述，实际 %q", content)
	}
	if !strings.HasPrefix(content, "【紧急】") {
		t.Errorf("urgency=high 应加【紧急】前缀，实际 %q", content)
	}
	if str(mg.body, "headImg") == "" {
		t.Errorf("headImg 必填，不应为空")
	}
	if str(mg.body, "title") != "新报修待受理" {
		t.Errorf("title 应为「新报修待受理」，实际 %q", str(mg.body, "title"))
	}
	// 报修人自己不应收到待办（回声）
	if findCreate(creates, a.repOpenID) != nil {
		t.Errorf("报修人不应在提交时收到待办")
	}
	if got := h.taskState(oid, a.mgrEmp, src); got != model.NotifyStateActive {
		t.Errorf("提交后受理人待办本地应为 active，实际 %q", got)
	}

	// —— 受理 → 默认不发任何请求（产品定义事件 13）——
	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), a.mgrTok, nil); c != 200 {
		t.Fatalf("受理失败: %d %s", c, str(m, "error"))
	}
	h.wait()
	if n := fake.count(); n != 0 {
		t.Errorf("accept 默认（informational=false）不应发任何请求，实际 %d 条", n)
	}
	// 漏洞 1 回归：受理后受理人待办**仍是 active**（绝不能是 dealt），否则派工提醒会消失。
	if got := h.taskState(oid, a.mgrEmp, src); got != model.NotifyStateActive {
		t.Errorf("受理后受理人待办应保持 active（漏洞 1），实际 %q", got)
	}

	// —— 派工 → 受理人 deal（AC-2）+ 维护人员 create ——
	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", oid), a.mgrTok,
		map[string]any{"assignee_emp_id": a.techEmp}); c != 200 {
		t.Fatalf("派工失败: %d %s", c, str(m, "error"))
	}
	h.wait()
	// AC-2：派工后受理人待办转为已办。
	if got := h.taskState(oid, a.mgrEmp, src); got != model.NotifyStateDealt {
		t.Errorf("派工后受理人待办应为 dealt（AC-2），实际 %q", got)
	}
	if findActionWith(fake.actions(src), a.mgrOpenID, "deal") == nil {
		t.Errorf("派工应关闭受理人待办")
	}
	tc := findCreate(fake.creates(src), a.techOpenID)
	if tc == nil {
		t.Fatalf("派工后维护人员应收到 create")
	}
	if !strings.Contains(str(tc.body, "content"), "指派你维修") {
		t.Errorf("派工 content 应为「指派你维修」，实际 %q", str(tc.body, "content"))
	}
	if got := h.taskState(oid, a.techEmp, src); got != model.NotifyStateActive {
		t.Errorf("派工后维护人员待办应为 active，实际 %q", got)
	}

	// —— 接单 → 维护人员**只置已读**（{read:1}），本地保持 active（漏洞 2 / 事件 14）——
	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/take", oid), a.techTok, nil); c != 200 {
		t.Fatalf("接单失败: %d %s", c, str(m, "error"))
	}
	h.wait()
	rd := findActionWith(fake.actions(src), a.techOpenID, "read")
	if rd == nil {
		t.Fatalf("接单应给维护人员发 read")
	}
	rat := actionTypeOf(*rd)
	if _, hasDeal := rat["deal"]; hasDeal {
		t.Errorf("接单只应置已读、不得 deal（漏洞 2），实际 actiontype=%v", rat)
	}
	if _, flat := rd.body["read"]; flat {
		t.Errorf("不得使用顶层 read 平铺字段（K1），实际 body=%s", rd.raw)
	}
	if got := h.taskState(oid, a.techEmp, src); got != model.NotifyStateActive {
		t.Errorf("接单后维护人员待办本地应保持 active（漏洞 2），实际 %q", got)
	}

	// —— 报完工 → 维护人员 deal（事件 15）+ 报修人 create ——
	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/finish", oid), a.techTok,
		map[string]any{"handler_desc": "更换压缩机"}); c != 200 {
		t.Fatalf("报完工失败: %d %s", c, str(m, "error"))
	}
	h.wait()
	if got := h.taskState(oid, a.techEmp, src); got != model.NotifyStateDealt {
		t.Errorf("报完工后维护人员待办应为 dealt（事件 15），实际 %q", got)
	}
	rc2 := findCreate(fake.creates(src), a.repOpenID)
	if rc2 == nil {
		t.Fatalf("报完工后报修人应收到 create")
	}
	if !strings.Contains(str(rc2.body, "content"), "请确认维修结果") {
		t.Errorf("报完工 content 应含动作要求句，实际 %q", str(rc2.body, "content"))
	}
	// 报修人此前无待办（accept 默认不发）→ 直接 create，不应出现 delete。
	if findActionWith(fake.actions(src), a.repOpenID, "delete") != nil {
		t.Errorf("报修人此前无待办，报完工不应发 delete")
	}

	// —— 确认完工 → 报修人 / 维护人员 / 受理人三方全部 dealt（AC-5）——
	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/confirm", oid), a.repTok, nil); c != 200 {
		t.Fatalf("确认完工失败: %d %s", c, str(m, "error"))
	}
	h.wait()
	for name, emp := range map[string]int64{"报修人": a.repEmp, "维护人员": a.techEmp, "受理人": a.mgrEmp} {
		if got := h.taskState(oid, emp, src); got != model.NotifyStateDealt {
			t.Errorf("确认完工后 %s 待办应为 dealt（AC-5），实际 %q", name, got)
		}
	}
}

// TestNotifyAcceptInformationalSwitch accept 的报修人「已受理」通知受 informational 开关控制：
// 关闭（默认）零请求；打开则给报修人**新建一条已读待办**（create + read，共 2 次云之家请求），
// 且**受理人待办仍保持 active**（accept 从不 deal 受理人）。
//
// ⚠️ 需要真库，安全阀生效时（DSN 指向生产库 asset）会 SKIP；其 store-free 的等价断言见
// notify_plan_informational_test.go（那些用例在任何环境都会真正执行）。
func TestNotifyAcceptInformationalSwitch(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, true, fake)
	h.svc.informational = true // 打开信息性告知开关
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")

	oid, src := h.createRepair(a, "受理告知", "")
	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), a.mgrTok, nil); c != 200 {
		t.Fatalf("受理失败: %d %s", c, str(m, "error"))
	}
	h.wait()
	rc := findCreate(fake.creates(src), a.repOpenID)
	if rc == nil {
		t.Fatalf("informational=true 时受理应给报修人 create")
	}
	if !strings.Contains(str(rc.body, "content"), "已被受理") {
		t.Errorf("受理 content 应为「已被受理」，实际 %q", str(rc.body, "content"))
	}
	// 信息性告知 = 新建**已读**待办（产品定义 Q2）：create 之后必须紧跟一条 read，避免打扰。
	rd := findActionWith(fake.actions(src), a.repOpenID, "read")
	if rd == nil {
		t.Fatalf("informational=true 时受理应给报修人发 read（新建即已读），实际 actions=%+v", fake.actions(src))
	}
	if _, hasDeal := actionTypeOf(*rd)["deal"]; hasDeal {
		t.Errorf("信息性告知只应置已读、不得 deal，实际 actiontype=%v", actionTypeOf(*rd))
	}
	// 恰好两次云之家请求：create + read。
	if n := fake.count(); n != 2 {
		t.Errorf("informational=true 的受理应发恰好 2 次云之家请求（create+read），实际 %d 次: %+v",
			n, fake.snapshot())
	}
	if got := h.taskState(oid, a.mgrEmp, src); got != model.NotifyStateActive {
		t.Errorf("accept 从不 deal 受理人，受理人待办应保持 active，实际 %q", got)
	}
}

// TestNotifyDealIdempotent 对已 dealt 的人再触发 deal 必须退化为 no-op（不发云之家 action）。
// 这条钉死 deliverDeal 的幂等守卫：删掉守卫本用例必红。
func TestNotifyDealIdempotent(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, true, fake)
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")

	oid, src := h.createRepair(a, "幂等场景", "")
	h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), a.mgrTok, nil)
	h.wait()
	h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", oid), a.mgrTok, map[string]any{"assignee_emp_id": a.techEmp})
	h.wait()
	h.do("POST", fmt.Sprintf("/api/repairs/%d/take", oid), a.techTok, nil)
	h.wait()
	h.do("POST", fmt.Sprintf("/api/repairs/%d/finish", oid), a.techTok, map[string]any{"handler_desc": "修好"})
	h.wait()
	// 此刻：维护人员 dealt（报完工）、受理人 dealt（派工）、报修人 active（报完工 create）。

	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/confirm", oid), a.repTok, nil); c != 200 {
		t.Fatalf("确认完工失败: %d %s", c, str(m, "error"))
	}
	h.wait()

	// 已 dealt 的维护人员 / 受理人：confirm 的 deal 应被守卫拦下，不再发 action。
	if r := findActionWith(fake.actions(src), a.techOpenID, "deal"); r != nil {
		t.Errorf("对已 dealt 的维护人员不应再发 action，实际 %+v", r)
	}
	if r := findActionWith(fake.actions(src), a.mgrOpenID, "deal"); r != nil {
		t.Errorf("对已 dealt 的受理人不应再发 action，实际 %+v", r)
	}
	// 正向对照：报修人此刻是 active，confirm 必须把他 deal 掉——证明 confirm 确实跑了。
	if findActionWith(fake.actions(src), a.repOpenID, "deal") == nil {
		t.Errorf("报修人应被 deal（正向对照）")
	}
}

// TestNotifyDispatchExternalVendorSkipped 派给外部供应商（VendorID>0）：**仅跳过维护人员通知**，
// 受理人那条仍应被关闭（产品定义行 3：任务已交出去，管理员不再需要动作）。
func TestNotifyDispatchExternalVendorSkipped(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, true, fake)
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")
	vendorID := h.newVendor(fmt.Sprintf("QA供应商-%d", time.Now().UnixNano()))

	oid, src := h.createRepair(a, "外部维保", "")
	h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), a.mgrTok, nil)
	h.wait()

	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", oid), a.mgrTok,
		map[string]any{"vendor_id": vendorID}); c != 200 {
		t.Fatalf("派给供应商失败: %d %s", c, str(m, "error"))
	}
	h.wait()
	if n := len(fake.creates(src)); n != 0 {
		t.Errorf("派给外部供应商不应创建维护人员待办，实际 %d 条 create", n)
	}
	// 受理人那条仍应被关闭（供应商分支只是不通知维护人员）。
	if findActionWith(fake.actions(src), a.mgrOpenID, "deal") == nil {
		t.Errorf("派给供应商时受理人待办仍应被关闭")
	}
	if got := h.taskState(oid, a.mgrEmp, src); got != model.NotifyStateDealt {
		t.Errorf("派给供应商后受理人待办应为 dealt，实际 %q", got)
	}
}

// TestNotifyNoOpenIDSkipped 维护人员无 openId：跳过该人、记日志、不报错（§3.2 约定 / AC-9）。
func TestNotifyNoOpenIDSkipped(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, true, fake)
	// 维护人员 openId 为空
	a := h.newActors("MGROPENID0001", "", "REPOPENID001")

	oid, src := h.createRepair(a, "无 openId 维护人员", "")
	h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), a.mgrTok, nil)
	h.wait()

	fake.reset()
	c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", oid), a.mgrTok,
		map[string]any{"assignee_emp_id": a.techEmp})
	if c != 200 {
		t.Fatalf("无 openId 时派工仍应成功（流程不阻断），实际 %d %s", c, str(m, "error"))
	}
	h.wait()
	if n := len(fake.creates(src)); n != 0 {
		t.Errorf("维护人员无 openId 时应跳过、不发 create，实际 %d 条", n)
	}
}

// TestNotifyReturnReactivates 退回：报修人待办流转 {deal:1}，维护人员待办重新激活（先删后建）（§3.2 #8）。
func TestNotifyReturnReactivates(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, true, fake)
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")

	oid, src := h.createRepair(a, "退回场景", "")
	// 逐步等待，保证每个跃迁的投递先于下一个跃迁（避免并发乱序干扰断言）。
	h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), a.mgrTok, nil)
	h.wait()
	h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", oid), a.mgrTok, map[string]any{"assignee_emp_id": a.techEmp})
	h.wait()
	h.do("POST", fmt.Sprintf("/api/repairs/%d/take", oid), a.techTok, nil)
	h.wait()
	h.do("POST", fmt.Sprintf("/api/repairs/%d/finish", oid), a.techTok, map[string]any{"handler_desc": "初修"})
	h.wait()

	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/return", oid), a.repTok,
		map[string]any{"remark": "仍异响"}); c != 200 {
		t.Fatalf("退回失败: %d %s", c, str(m, "error"))
	}
	h.wait()

	// 报修人待办流转 {deal:1}（无 read）
	rd := findActionWith(fake.actions(src), a.repOpenID, "deal")
	if rd == nil {
		t.Fatalf("退回后报修人待办应被流转")
	}
	if _, hasRead := actionTypeOf(*rd)["read"]; hasRead {
		t.Errorf("退回时报修人流转应为 {deal:1}，不应含 read，实际 %v", actionTypeOf(*rd))
	}
	// 维护人员待办重新激活：先删后建
	if findActionWith(fake.actions(src), a.techOpenID, "delete") == nil {
		t.Errorf("退回应删除维护人员旧待办（先删后建）")
	}
	tc := findCreate(fake.creates(src), a.techOpenID)
	if tc == nil {
		t.Fatalf("退回后维护人员应重新收到 create")
	}
	if !strings.Contains(str(tc.body, "content"), "请重新处理") {
		t.Errorf("退回 content 应含动作要求句「请重新处理」，实际 %q", str(tc.body, "content"))
	}
	if !strings.Contains(str(tc.body, "content"), "仍异响") {
		t.Errorf("退回 content 应含退回原因，实际 %q", str(tc.body, "content"))
	}
}

// TestNotifyRejectToReporter 驳回：受理人待办流转，报修人收到「已驳回」。
func TestNotifyRejectToReporter(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, true, fake)
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")

	oid, src := h.createRepair(a, "驳回场景", "")
	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/reject", oid), a.mgrTok,
		map[string]any{"remark": "重复报修"}); c != 200 {
		t.Fatalf("驳回失败: %d %s", c, str(m, "error"))
	}
	h.wait()

	if findActionWith(fake.actions(src), a.mgrOpenID, "deal") == nil {
		t.Errorf("驳回后受理人待办应被流转")
	}
	rc := findCreate(fake.creates(src), a.repOpenID)
	if rc == nil {
		t.Fatalf("驳回后报修人应收到 create")
	}
	content := str(rc.body, "content")
	if !strings.Contains(content, "被驳回") || !strings.Contains(content, "重复报修") {
		t.Errorf("驳回 content 应含「被驳回」与原因，实际 %q", content)
	}
}

// TestNotifyCancelDeletesAll 撤单：delete 且**不填 openids**（删该 sourceId 下所有人）（§3.2 #9）。
func TestNotifyCancelDeletesAll(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, true, fake)
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")

	oid, src := h.createRepair(a, "撤单场景", "")
	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/cancel", oid), a.repTok, nil); c != 200 {
		t.Fatalf("撤单失败: %d %s", c, str(m, "error"))
	}
	h.wait()
	if findDeleteAll(fake.actions(src)) == nil {
		t.Errorf("撤单应发 delete 且不填 openids，实际 actions=%+v", fake.actions(src))
	}
}

// TestNotifyFailureIsolation 云之家不可用时维修单 API 仍返回 200，业务事务不回滚（§7 / AC-8）。
// 这是本功能最重要的一条：外部依赖失败绝不阻塞或回滚业务。
func TestNotifyFailureIsolation(t *testing.T) {
	fake := newFakeYzj(t)
	fake.failHTTP.Store(http.StatusInternalServerError) // 云之家不可用
	h := notifySetup(t, true, fake)
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")

	// 提交报修：受理人 create 失败。
	oid, _ := h.createRepair(a, "云之家挂了", "")

	// 受理默认不发通知；用**派工**（会 deal 受理人 + create 维护人员）验证失败隔离。
	if c, _, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), a.mgrTok, nil); c != 200 {
		t.Fatalf("受理应 200，实际 %d", c)
	}
	code, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", oid), a.mgrTok,
		map[string]any{"assignee_emp_id": a.techEmp})
	if code != 200 {
		t.Fatalf("云之家不可用时派工仍应 200（失败隔离），实际 %d %s", code, str(m, "error"))
	}
	if str(m, "status") != model.RepairDispatched {
		t.Fatalf("派工后状态应为 dispatched，实际 %q", str(m, "status"))
	}
	// 业务数据已落库（事务未被回滚）
	o, err := h.st.GetRepair(oid)
	if err != nil || o == nil || o.Status != model.RepairDispatched {
		t.Fatalf("派工事务应已提交（status=dispatched），实际 %+v err=%v", o, err)
	}
	// 失败已落 repair_notify_task.state='failed'
	h.wait()
	var n int
	if err := h.st.DB().QueryRow(
		"SELECT COUNT(*) FROM repair_notify_task WHERE repair_id=? AND state='failed'", oid).Scan(&n); err != nil {
		t.Fatalf("查询失败任务失败: %v", err)
	}
	if n == 0 {
		t.Errorf("云之家不可用时应落 state='failed' 任务（失败可观测），实际 0 条")
	}
}

// TestNotifyDisabledSendsNothing todo_enabled=false 时完全不发请求（请求计数为 0）。
func TestNotifyDisabledSendsNothing(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, false, fake)
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")

	oid, _ := h.createRepair(a, "开关关闭", "")
	h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), a.mgrTok, nil)
	h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", oid), a.mgrTok, map[string]any{"assignee_emp_id": a.techEmp})
	h.wait()

	if n := fake.count(); n != 0 {
		t.Errorf("todo_enabled=false 时不应发出任何请求（含 token），实际 %d 条", n)
	}
}

// TestNotifySourceIDStable 同一单据的所有待办共用 sourceId = repair-{id}（§4.2）。
func TestNotifySourceIDStable(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, true, fake)
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")

	oid, src := h.createRepair(a, "sourceId 稳定性", "")
	h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), a.mgrTok, nil)
	h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", oid), a.mgrTok, map[string]any{"assignee_emp_id": a.techEmp})
	h.wait()

	for _, r := range fake.snapshot() {
		if r.path == "/gateway/newtodo/open/generatetodo.json" {
			if got := str(r.body, "sourceId"); got != src {
				t.Errorf("所有 create 的 sourceId 应为 %q，实际 %q", src, got)
			}
		}
		if r.path == "/gateway/newtodo/open/action.json" {
			if got := str(r.body, "sourceitemid"); got != src {
				t.Errorf("所有 action 的 sourceitemid 应为 %q，实际 %q", src, got)
			}
		}
	}
}

// indexOfDeleteAll 返回「不填 openids 的 delete」在请求序列里的下标（-1 表示没有）。
// 用于断言撤单的 delete-all 与随后的 create 之间的先后顺序。
func indexOfDeleteAll(reqs []recordedReq) int {
	for i, r := range reqs {
		at := actionTypeOf(r)
		if at == nil {
			continue
		}
		if _, ok := at["delete"]; !ok {
			continue
		}
		if _, hasOpen := r.body["openids"]; !hasOpen {
			return i
		}
	}
	return -1
}

// indexOfCreate 返回针对该 openId 的 generatetodo 在请求序列里的下标（-1 表示没有）。
func indexOfCreate(reqs []recordedReq, openID string) int {
	for i, r := range reqs {
		if r.path == "/gateway/newtodo/open/generatetodo.json" && openIDInField(r.body, "openId", openID) {
			return i
		}
	}
	return -1
}

// TestNotifyAdminCancelNotifiesReporter 行 11（P0，产品定义行 11）：管理员取消
// （跃迁前状态 ≠ pending）时，delete-all 之后必须给报修人**重新 create** 一条
// 「已被管理员取消」待办——否则 delete-all 会把报修人那条一并删掉，他永远不知道报修被取消。
//
// 这是「⑦ 行 11」的正向用例：删掉 `prevStatus != pending` 分流（改成无条件 create），
// 或把调用点的 `o.Status` 误传成 `updated.Status`（取消后已是 cancelled≠pending，会误判为行 10），
// 本用例必红。
func TestNotifyAdminCancelNotifiesReporter(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, true, fake)
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")

	oid, src := h.createRepair(a, "管理员取消", "")
	h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), a.mgrTok, nil)
	h.wait()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/dispatch", oid), a.mgrTok,
		map[string]any{"assignee_emp_id": a.techEmp}); c != 200 {
		t.Fatalf("派工失败: %d %s", c, str(m, "error"))
	}
	h.wait()

	// 管理员取消（dispatched→cancelled，行 11）。
	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/cancel", oid), a.mgrTok, nil); c != 200 {
		t.Fatalf("管理员取消失败: %d %s", c, str(m, "error"))
	}
	h.wait()

	reqs := fake.snapshot()
	if findDeleteAll(fake.actions(src)) == nil {
		t.Errorf("取消应发 delete 且不填 openids，实际 actions=%+v", fake.actions(src))
	}
	rc := findCreate(fake.creates(src), a.repOpenID)
	if rc == nil {
		t.Fatalf("管理员取消后报修人应收到「已取消」待办（行 11），实际 creates=%d", len(fake.creates(src)))
	}
	if got := str(rc.body, "content"); !strings.Contains(got, "已被管理员取消") {
		t.Errorf("取消 content 应含「已被管理员取消」，实际 %q", got)
	}
	if got := str(rc.body, "title"); got != "报修已取消" {
		t.Errorf("取消 title 应为「报修已取消」，实际 %q", got)
	}
	// 顺序：先 delete-all 清场，再 create 重建（否则报修人的新待办会被随后的 delete 再删掉）。
	di := indexOfDeleteAll(reqs)
	ci := indexOfCreate(reqs, a.repOpenID)
	if di < 0 || ci < 0 || di > ci {
		t.Errorf("应先 delete-all（idx=%d）再 create（idx=%d）", di, ci)
	}
	// 报修人本地最终应为 active（新待办未办）。
	if got := h.taskState(oid, a.repEmp, src); got != model.NotifyStateActive {
		t.Errorf("取消后报修人待办本地应为 active，实际 %q", got)
	}
}

// TestNotifyReporterCancelPendingNoReporterCreate 行 10（反向）：报修人自己撤单
// （pending→cancelled）只 delete-all，**不**给报修人 create——他刚撤的单无需再被通知。
// 删掉 `prevStatus != pending` 分流（改成无条件 create）本用例必红。
func TestNotifyReporterCancelPendingNoReporterCreate(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, true, fake)
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")

	oid, src := h.createRepair(a, "报修人撤单", "")
	fake.reset()
	if c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/cancel", oid), a.repTok, nil); c != 200 {
		t.Fatalf("撤单失败: %d %s", c, str(m, "error"))
	}
	h.wait()

	if findDeleteAll(fake.actions(src)) == nil {
		t.Errorf("撤单应发 delete 且不填 openids，实际 actions=%+v", fake.actions(src))
	}
	if rc := findCreate(fake.creates(src), a.repOpenID); rc != nil {
		t.Errorf("报修人主动撤单（pending）不应再收到 create（行 10），实际 %+v", rc)
	}
	if n := len(fake.creates(src)); n != 0 {
		t.Errorf("pending→cancelled 不应有任何 create，实际 %d 条", n)
	}
}

// TestNotifyCancelFromAcceptedNoError 管理员在 accepted（尚未派工，AssigneeEmpID=0）时取消：
// 流程不报错，delete-all + 报修人「已取消」通知都要发出（行 11 边界：空维护人员不产生异常）。
func TestNotifyCancelFromAcceptedNoError(t *testing.T) {
	fake := newFakeYzj(t)
	h := notifySetup(t, true, fake)
	a := h.newActors("MGROPENID0001", "TECHOPENID01", "REPOPENID001")

	oid, src := h.createRepair(a, "受理后取消", "")
	h.do("POST", fmt.Sprintf("/api/repairs/%d/accept", oid), a.mgrTok, nil)
	h.wait()

	fake.reset()
	c, m, _ := h.do("POST", fmt.Sprintf("/api/repairs/%d/cancel", oid), a.mgrTok, nil)
	if c != 200 {
		t.Fatalf("已受理后管理员取消应 200（空维护人员不报错），实际 %d %s", c, str(m, "error"))
	}
	if got := str(m, "status"); got != model.RepairCancelled {
		t.Fatalf("取消后状态应为 cancelled，实际 %q", got)
	}
	h.wait()

	if findDeleteAll(fake.actions(src)) == nil {
		t.Errorf("取消应发 delete 且不填 openids，实际 actions=%+v", fake.actions(src))
	}
	rc := findCreate(fake.creates(src), a.repOpenID)
	if rc == nil {
		t.Fatalf("管理员取消后报修人应收到「已取消」待办（行 11），实际 creates=%d", len(fake.creates(src)))
	}
	if !strings.Contains(str(rc.body, "content"), "已被管理员取消") {
		t.Errorf("取消 content 应含「已被管理员取消」，实际 %q", str(rc.body, "content"))
	}
}
