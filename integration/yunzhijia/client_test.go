package yunzhijia

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient 起一个假云之家 httptest.Server 并返回客户端，测试结束自动关闭。
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "app-test-1", "secret-xyz", 5*time.Second)
}

// writeToken 输出认证接口的成功响应（success 为 **bool**，与待办接口的字符串不同，K2）。
func writeToken(w http.ResponseWriter, expireIn int) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"data":{"accessToken":"tok-abc","refreshToken":"refresh-12345678901234567890123456789012","expireIn":%d},"errorCode":0,"success":true,"error":""}`, expireIn)
}

// isTokenPath 判断是否为认证接口。
func isTokenPath(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, "/getAccessToken")
}

func intPtr(v int) *int { return &v }

// ---------------------------------------------------------------------------
// K11：动态 TTL 缓存
// ---------------------------------------------------------------------------

func TestGetAppTokenCachesWithinExpiry(t *testing.T) {
	var hits int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeToken(w, 7200)
	})
	for i := 0; i < 3; i++ {
		tok, err := c.GetAppToken(context.Background())
		if err != nil {
			t.Fatalf("第 %d 次 GetAppToken 失败：%v", i+1, err)
		}
		if tok != "tok-abc" {
			t.Fatalf("token 期望 tok-abc，实际 %q", tok)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expireIn=7200 时应缓存复用，期望只请求 1 次，实际 %d 次", got)
	}
}

func TestGetAppTokenDoesNotCacheShortLived(t *testing.T) {
	var hits int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeToken(w, 100) // <= 300s 安全余量 → 不缓存
	})
	for i := 0; i < 3; i++ {
		if _, err := c.GetAppToken(context.Background()); err != nil {
			t.Fatalf("第 %d 次 GetAppToken 失败：%v", i+1, err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("expireIn=100（<=300s）时不应缓存，期望请求 3 次，实际 %d 次", got)
	}
}

func TestGetAppTokenRefetchesAfterExpiry(t *testing.T) {
	var hits int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeToken(w, 7200)
	})
	if _, err := c.GetAppToken(context.Background()); err != nil {
		t.Fatalf("首次 GetAppToken 失败：%v", err)
	}
	// 把过期时间拨到过去，模拟缓存失效。
	c.mu.Lock()
	c.tokenExpiry = time.Now().Add(-time.Second)
	c.mu.Unlock()
	if _, err := c.GetAppToken(context.Background()); err != nil {
		t.Fatalf("过期后 GetAppToken 失败：%v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("缓存过期后应重新获取，期望请求 2 次，实际 %d 次", got)
	}
}

// K12：refreshToken 被解析并保存（本期不实现刷新）。
func TestGetAppTokenParsesRefreshToken(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeToken(w, 7200)
	})
	if _, err := c.GetAppToken(context.Background()); err != nil {
		t.Fatalf("GetAppToken 失败：%v", err)
	}
	c.mu.Lock()
	got := c.refreshToken
	c.mu.Unlock()
	if got != "refresh-12345678901234567890123456789012" {
		t.Fatalf("refreshToken 应被解析保存，实际 %q", got)
	}
}

// ---------------------------------------------------------------------------
// K2：flexBool 兼容 bool 与 "true"/"false"
// ---------------------------------------------------------------------------

func TestFlexBool(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`true`, true},
		{`false`, false},
		{`"true"`, true},
		{`"false"`, false},
		{`"True"`, true},
		{`"FALSE"`, false},
		{`1`, true},
		{`0`, false},
		{`null`, false},
		{`""`, false},
	}
	for _, tc := range cases {
		var b flexBool
		if err := json.Unmarshal([]byte(tc.in), &b); err != nil {
			t.Fatalf("解析 %q 失败：%v", tc.in, err)
		}
		if b.Bool() != tc.want {
			t.Fatalf("解析 %q：期望 %v，实际 %v", tc.in, tc.want, b.Bool())
		}
	}
	var b flexBool
	if err := json.Unmarshal([]byte(`"yes"`), &b); err == nil {
		t.Fatalf("非法值 %q 应解析失败", "yes")
	}
}

// K2：创建待办的 success 为字符串 "true" 也能正确识别。
func TestCreateTodoParsesStringSuccess(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{},"success":"true","errorCode":200,"error":"待办创建成功"}`))
	})
	res, err := c.CreateTodo(context.Background(), &TodoRequest{
		SourceID: "repair-1",
		URL:      "https://fams.example.com/#/repairs/1",
		Content:  "有新维修单待受理",
		Title:    "维修单待受理",
		HeadImg:  "https://yzj.example.com/x.png",
		OpenIDs:  []string{"5de50017e4b0b2767b870423"},
	})
	if err != nil {
		t.Fatalf("CreateTodo 失败：%v", err)
	}
	if !res.Success.Bool() || res.ErrorCode != 200 {
		t.Fatalf("success 字符串 \"true\" 应解析为 true，实际 success=%v errorCode=%d", res.Success.Bool(), res.ErrorCode)
	}
	if !res.OK() {
		t.Fatalf("OK() 应为 true")
	}
}

// K3：501 是部分成功，data 为失败 openId 列表，不应整体判失败重试。
func TestCreateTodoPartialFailureParsesFailedOpenIDs(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		_, _ = w.Write([]byte(`{"data":["5b3726f884ae5c69eec1231"],"success":"false","errorCode":501,"error":"有部分用户信息错误"}`))
	})
	res, err := c.CreateTodo(context.Background(), &TodoRequest{SourceID: "repair-1", URL: "u", Content: "c", Title: "t", HeadImg: "h"})
	if err != nil {
		t.Fatalf("CreateTodo 失败：%v", err)
	}
	if res.Success.Bool() {
		t.Fatalf("501 时 success 应为 false")
	}
	if res.ErrorCode != 501 {
		t.Fatalf("errorCode 期望 501，实际 %d", res.ErrorCode)
	}
	if len(res.FailedOpenIDs) != 1 || res.FailedOpenIDs[0] != "5b3726f884ae5c69eec1231" {
		t.Fatalf("应解析出失败 openId 列表，实际 %v", res.FailedOpenIDs)
	}
}

// ---------------------------------------------------------------------------
// K1：actiontype 必须嵌套（含防回归断言）
// ---------------------------------------------------------------------------

func TestActionTodoUsesNestedActionType(t *testing.T) {
	var captured map[string]json.RawMessage
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("请求体不是合法 JSON：%v（原文 %s）", err, body)
		}
		_, _ = w.Write([]byte(`{"data":{},"success":"true","errorCode":200,"error":"待办转已办成功"}`))
	})
	_, err := c.ActionTodo(context.Background(), &TodoActionRequest{
		SourceItemID: "repair-1",
		ActionType:   TodoActionType{Read: intPtr(1), Deal: intPtr(1)},
		OpenIDs:      []string{"5de50017e4b0b2767b870423"},
	})
	if err != nil {
		t.Fatalf("ActionTodo 失败：%v", err)
	}

	// 正向：必须包含嵌套 actiontype。
	raw, ok := captured["actiontype"]
	if !ok {
		t.Fatalf("请求体必须包含嵌套 actiontype（K1），实际字段：%v", keysOf(captured))
	}
	var at map[string]int
	if err := json.Unmarshal(raw, &at); err != nil {
		t.Fatalf("actiontype 应为对象：%v", err)
	}
	if at["read"] != 1 || at["deal"] != 1 {
		t.Fatalf("actiontype 应含 read=1,deal=1，实际 %v", at)
	}

	// 防回归：请求体**不得**出现顶层平铺的 read/deal/delete（否则服务端报 actiontype is null）。
	for _, k := range []string{"read", "deal", "delete"} {
		if _, exists := captured[k]; exists {
			t.Fatalf("K1 回归：请求体出现了顶层平铺字段 %q，必须使用嵌套 actiontype", k)
		}
	}
}

func TestActionTodoDeleteAllOmitsOpenIDs(t *testing.T) {
	var captured map[string]json.RawMessage
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_, _ = w.Write([]byte(`{"data":{},"success":"true","errorCode":200,"error":"待办删除成功"}`))
	})
	// 不填 openids → 删除该 sourceitemid 下所有人。
	_, err := c.ActionTodo(context.Background(), &TodoActionRequest{
		SourceItemID: "repair-1",
		ActionType:   TodoActionType{Delete: intPtr(1)},
	})
	if err != nil {
		t.Fatalf("ActionTodo 失败：%v", err)
	}
	if _, exists := captured["openids"]; exists {
		t.Fatalf("不填 openids 时请求体不应出现 openids 字段，实际 %v", string(captured["openids"]))
	}
	var at map[string]int
	if err := json.Unmarshal(captured["actiontype"], &at); err != nil {
		t.Fatalf("actiontype 应为对象：%v", err)
	}
	if at["delete"] != 1 {
		t.Fatalf("actiontype.delete 期望 1，实际 %v", at)
	}
}

// action.json 参数异常时 data 为字符串，应解析到 DataMessage。
func TestActionTodoParamErrorMessage(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		_, _ = w.Write([]byte(`{"data":"param error:actiontype is null.","success":"false","errorCode":1,"error":""}`))
	})
	res, err := c.ActionTodo(context.Background(), &TodoActionRequest{SourceItemID: "repair-1"})
	if err != nil {
		t.Fatalf("ActionTodo 失败：%v", err)
	}
	if res.Success.Bool() {
		t.Fatalf("success 应为 false")
	}
	if res.DataMessage != "param error:actiontype is null." {
		t.Fatalf("应解析出 data 字符串，实际 %q", res.DataMessage)
	}
}

// ---------------------------------------------------------------------------
// checkcreatetodo
// ---------------------------------------------------------------------------

func TestCheckCreateTodo(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/checkcreatetodo.json") {
			t.Errorf("路径错误：%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":{"sourceid":"fams-probe-nonexistent-0001","appid":"500000084","check":true,"dealCount":0,"undelCount":1},"success":"true","errorCode":0,"error":""}`))
	})
	res, err := c.CheckCreateTodo(context.Background(), &CheckTodoRequest{
		SourceItemID: "fams-probe-nonexistent-0001",
		OpenID:       "5de50017e4b0b2767b870423",
	})
	if err != nil {
		t.Fatalf("CheckCreateTodo 失败：%v", err)
	}
	if !res.Check.Bool() {
		t.Fatalf("check 应为 true")
	}
	if res.AppID != "500000084" || res.DealCount != 0 || res.UnDelCount != 1 {
		t.Fatalf("结果解析错误：%+v", res)
	}
}

// ---------------------------------------------------------------------------
// K10：getallpersons 表单编码
// ---------------------------------------------------------------------------

func TestFetchAllPersonsUsesFormEncoding(t *testing.T) {
	var gotCT string
	var gotForm url.Values
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/getallpersons") {
			t.Errorf("路径错误：%s", r.URL.Path)
		}
		gotCT = r.Header.Get("Content-Type")
		if err := r.ParseForm(); err != nil {
			t.Errorf("解析表单失败：%v", err)
		}
		gotForm = r.PostForm
		_, _ = w.Write([]byte(`{"success":true,"error":null,"errorCode":100,"data":{"persons":[{"openId":"5de50017e4b0b2767b870423","jobNo":"E001","name":"印"}],"time":"2019-12-19 13:50:57"}}`))
	})
	res, err := c.FetchAllPersons(context.Background(), "17951708", "2008-08-02 01:40:38", 0, 1000)
	if err != nil {
		t.Fatalf("FetchAllPersons 失败：%v", err)
	}
	if gotCT != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type 期望 form-urlencoded，实际 %q", gotCT)
	}
	if gotForm.Get("eid") != "17951708" || gotForm.Get("time") != "2008-08-02 01:40:38" ||
		gotForm.Get("begin") != "0" || gotForm.Get("count") != "1000" {
		t.Fatalf("表单参数错误：%v", gotForm)
	}
	if len(res.Persons) != 1 || res.Persons[0].OpenID != "5de50017e4b0b2767b870423" || res.Persons[0].JobNo != "E001" {
		t.Fatalf("人员解析错误：%+v", res.Persons)
	}
	if res.Time != "2019-12-19 13:50:57" {
		t.Fatalf("游标 time 解析错误：%q", res.Time)
	}
}

// ---------------------------------------------------------------------------
// 凭证安全：错误信息不得携带 token/secret
// ---------------------------------------------------------------------------

func TestErrorDoesNotLeakToken(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		// 劫持连接后直接关闭：制造底层 *url.Error（其 Error() 会带含 accessToken 的完整 URL）。
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	})
	_, err := c.CreateTodo(context.Background(), &TodoRequest{SourceID: "x", URL: "u", Content: "c", Title: "t", HeadImg: "h"})
	if err == nil {
		t.Fatalf("期望传输层错误，实际为 nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "tok-abc") {
		t.Fatalf("错误信息泄漏了 accessToken：%s", msg)
	}
	if !strings.Contains(msg, "***") {
		t.Fatalf("期望 token 被脱敏为 ***，实际：%s", msg)
	}
}

func TestServerErrorDoesNotLeakSecret(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		http.Error(w, "boom secret-xyz tok-abc", http.StatusInternalServerError)
	})
	_, err := c.CreateTodo(context.Background(), &TodoRequest{SourceID: "x", URL: "u", Content: "c", Title: "t", HeadImg: "h"})
	if err == nil {
		t.Fatalf("期望 HTTP 500 错误，实际为 nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "tok-abc") || strings.Contains(msg, "secret-xyz") {
		t.Fatalf("错误信息泄漏了凭证：%s", msg)
	}
}

// ---------------------------------------------------------------------------
// MaskOpenID
// ---------------------------------------------------------------------------

func TestMaskOpenID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"5de50017e4b0b2767b870423", "5de5****0423"},
		{"", ""},
		{"short", "****"},
		{"12345678", "****"}, // 恰好 8 位 → 整体打码
		{"123456789", "1234****6789"},
	}
	for _, tc := range cases {
		if got := MaskOpenID(tc.in); got != tc.want {
			t.Fatalf("MaskOpenID(%q) 期望 %q，实际 %q", tc.in, tc.want, got)
		}
	}
}

// keysOf 仅用于失败信息里展示字段名。
func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
