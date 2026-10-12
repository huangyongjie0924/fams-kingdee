package yunzhijia

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 已知答案向量（KAT）：用**独立于 Go 代码**的途径算出期望签名，做跨实现校验，
// 而不是「拿自己的输出当期望值」的自证。
//
// 向量：
//
//	jsapiticket = "jsapi-ticket-DEMO-0001"
//	nonceStr    = "0.7057841489531929"
//	timestamp   = "1530867948317"
//	url         = "https://fams.example.com/#/repairs/123"
//
// 期望签名（带 `&` 形态）：
//	tDBu2p3vU9uIOXeGOm7KblFU+2Q=
//
// 计算命令（两种独立途径，结果一致，均已实测）：
//
//	# 途径 A：openssl
//	printf '%s' 'noncestr=0.7057841489531929&timestamp=1530867948317&url=https://fams.example.com/#/repairs/123' \
//	  | openssl dgst -sha1 -hmac 'jsapi-ticket-DEMO-0001' -binary | openssl base64
//
//	# 途径 B：python
//	python -c "import hmac,hashlib,base64; \
//	b='noncestr=0.7057841489531929&timestamp=1530867948317&url=https://fams.example.com/#/repairs/123'; \
//	print(base64.b64encode(hmac.new(b'jsapi-ticket-DEMO-0001',b.encode(),hashlib.sha1).digest()).decode())"
//
// ⚠️ 同一向量在**无 `&`** 形态（文档 :180 的 Java 参考实现）下的签名是
// "loo2YRPwCbkR/CI6rEJto+TZRIY="（与带 `&` 的不同）。两者差异见 TestJsapiBaselineKnownVector。
// ---------------------------------------------------------------------------

const (
	katTicket    = "jsapi-ticket-DEMO-0001"
	katNonce     = "0.7057841489531929"
	katTimestamp = "1530867948317"
	katURL       = "https://fams.example.com/#/repairs/123"

	katBaselineAmp   = "noncestr=0.7057841489531929&timestamp=1530867948317&url=https://fams.example.com/#/repairs/123"
	katBaselineNoAmp = "noncestr=0.7057841489531929timestamp=1530867948317url=https://fams.example.com/#/repairs/123"

	katSignatureAmp   = "tDBu2p3vU9uIOXeGOm7KblFU+2Q="
	katSignatureNoAmp = "loo2YRPwCbkR/CI6rEJto+TZRIY="
)

// writeJsapiTicket 输出 generateticket 的成功响应。
func writeJsapiTicket(w http.ResponseWriter, ticket string, expiresIn int) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"data":{"jsapiticket":%q,"expires_in":%d},"errorCode":0,"success":true,"error":""}`, ticket, expiresIn)
}

// isJsapiTicketPath 判断是否为 generateticket 接口。
func isJsapiTicketPath(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, "/jsapi/generateticket")
}

// ---------------------------------------------------------------------------
// 已知答案向量（跨实现校验）
// ---------------------------------------------------------------------------

// 基线拼接：断言拼写（全小写 noncestr）与段间分隔符形态。
func TestJsapiBaselineKnownVector(t *testing.T) {
	got := jsapiBaseline(katNonce, katTimestamp, katURL)
	if got != katBaselineAmp {
		t.Fatalf("baseline 期望\n  %q\n实际\n  %q", katBaselineAmp, got)
	}

	// ⚠️ 拼写陷阱断言（陷阱 1）：基线里必须是**全小写 noncestr**。
	if !strings.Contains(got, "noncestr=") {
		t.Fatalf("baseline 必须含全小写 `noncestr=`，实际 %q", got)
	}
	if strings.Contains(got, "nonceStr=") {
		t.Fatalf("baseline 不得含驼峰 `nonceStr=`（那是返回 JSON 的键名），实际 %q", got)
	}
	// 基线里的时间戳参数名是全小写 timestamp。
	if !strings.Contains(got, "timestamp=") {
		t.Fatalf("baseline 必须含全小写 `timestamp=`，实际 %q", got)
	}
}

// 签名计算：与 openssl/python 独立算出的期望值逐字比对。
func TestJsapiSignatureKnownVector(t *testing.T) {
	if got := jsapiSignature(katTicket, katBaselineAmp); got != katSignatureAmp {
		t.Fatalf("带 `&` 形态签名：期望 %q，实际 %q", katSignatureAmp, got)
	}
	// 反向对照：无 `&` 形态是**另一个**签名，证明分隔符确实影响结果（把分歧钉住，便于联调时定位）。
	if got := jsapiSignature(katTicket, katBaselineNoAmp); got != katSignatureNoAmp {
		t.Fatalf("无 `&` 形态签名：期望 %q，实际 %q", katSignatureNoAmp, got)
	}
	if katSignatureAmp == katSignatureNoAmp {
		t.Fatalf("两种 baseline 形态的签名不应相同")
	}
}

// 完整载荷 + JSON 键名（返回给客户端的键必须逐字匹配文档）。
func TestBuildJSAPISignatureKnownVectorAndJSONKeys(t *testing.T) {
	got := buildJSAPISignature("app-test-1", katTicket, katNonce, katTimestamp, katURL)
	want := JSAPISignature{
		AppID:     "app-test-1",
		TimeStamp: katTimestamp,
		NonceStr:  katNonce,
		Signature: katSignatureAmp,
	}
	if got != want {
		t.Fatalf("载荷不匹配：\n期望 %+v\n实际 %+v", want, got)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	wantJSON := `{"appId":"app-test-1","timeStamp":"1530867948317","nonceStr":"0.7057841489531929","signature":"tDBu2p3vU9uIOXeGOm7KblFU+2Q="}`
	if string(raw) != wantJSON {
		t.Fatalf("JSON 期望\n  %s\n实际\n  %s", wantJSON, raw)
	}
	// 返回 JSON 的键必须是**驼峰 nonceStr**（陷阱 1 的另一半）。
	if !strings.Contains(string(raw), `"nonceStr"`) {
		t.Fatalf("返回 JSON 必须含驼峰键 `nonceStr`，实际 %s", raw)
	}
	if strings.Contains(string(raw), `"noncestr"`) {
		t.Fatalf("返回 JSON 不得含全小写键 `noncestr`，实际 %s", raw)
	}
}

// ---------------------------------------------------------------------------
// 请求形态：表单编码 + accesstoken + 实测路径
// ---------------------------------------------------------------------------

func TestJsapiTicketRequestShape(t *testing.T) {
	var gotCT, gotPath, rawBody string
	var gotForm url.Values
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		rawBody = string(b)
		gotForm, _ = url.ParseQuery(rawBody)
		writeJsapiTicket(w, "jsapi-ticket-ABCDEF123456", 600)
	})
	if _, err := c.JsapiTicket(context.Background()); err != nil {
		t.Fatalf("JsapiTicket 失败：%v", err)
	}

	// 路径：实测确认是**非 gateway** 形态（/gateway/... 实测返回 10000404 URI 不存在）。
	if gotPath != "/authorization/jsapi/generateticket" {
		t.Fatalf("路径期望 /authorization/jsapi/generateticket，实际 %q", gotPath)
	}
	// Content-Type 必须是表单编码（K10 同类坑：不能是 JSON）。
	if gotCT != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type 期望 form-urlencoded，实际 %q", gotCT)
	}
	if strings.Contains(rawBody, "{") || strings.Contains(rawBody, "}") {
		t.Fatalf("请求体不应是 JSON，实际 %q", rawBody)
	}
	if gotForm.Get("accesstoken") != "tok-abc" {
		t.Fatalf("表单应含 accesstoken=tok-abc，实际 %v（原文 %q）", gotForm, rawBody)
	}
}

// ---------------------------------------------------------------------------
// 缓存：与 app token 分离 + 边界
// ---------------------------------------------------------------------------

func TestJsapiTicketCachesIndependentlyFromAppToken(t *testing.T) {
	var tokenHits, ticketHits int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isTokenPath(r):
			atomic.AddInt32(&tokenHits, 1)
			writeToken(w, 7200)
		case isJsapiTicketPath(r):
			atomic.AddInt32(&ticketHits, 1)
			writeJsapiTicket(w, "jsapi-ticket-ABCDEF123456", 600)
		default:
			t.Errorf("意外路径：%s", r.URL.Path)
		}
	})
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		tk, err := c.JsapiTicket(ctx)
		if err != nil {
			t.Fatalf("第 %d 次 JsapiTicket 失败：%v", i+1, err)
		}
		if tk != "jsapi-ticket-ABCDEF123456" {
			t.Fatalf("ticket 期望 jsapi-ticket-ABCDEF123456，实际 %q", tk)
		}
	}
	if got := atomic.LoadInt32(&ticketHits); got != 1 {
		t.Fatalf("expires_in=600 时应缓存复用，期望 generateticket 只 1 次，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(&tokenHits); got != 1 {
		t.Fatalf("app token 也应缓存复用，期望 getAccessToken 只 1 次，实际 %d 次", got)
	}

	// 证明 jsapiticket 缓存**独立于** app token 缓存：
	// 把 app token 缓存置为过期，jsapiticket 仍应命中**自身**缓存，不再打 generateticket、也不触碰 token。
	c.mu.Lock()
	c.tokenExpiry = time.Now().Add(-time.Second)
	c.mu.Unlock()
	if _, err := c.JsapiTicket(ctx); err != nil {
		t.Fatalf("token 过期后 JsapiTicket 失败：%v", err)
	}
	if got := atomic.LoadInt32(&ticketHits); got != 1 {
		t.Fatalf("app token 过期不应影响 jsapiticket 缓存，期望 generateticket 仍为 1 次，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(&tokenHits); got != 1 {
		t.Fatalf("jsapiticket 命中缓存时不应触碰 app token，期望 getAccessToken 仍为 1 次，实际 %d 次", got)
	}

	// 再把 jsapiticket 缓存置为过期 → 两处各自重新获取一次。
	c.jsapiMu.Lock()
	c.jsapiTicketExpiry = time.Now().Add(-time.Second)
	c.jsapiMu.Unlock()
	if _, err := c.JsapiTicket(ctx); err != nil {
		t.Fatalf("ticket 过期后 JsapiTicket 失败：%v", err)
	}
	if got := atomic.LoadInt32(&ticketHits); got != 2 {
		t.Fatalf("jsapiticket 过期后应重新获取，期望 generateticket 2 次，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(&tokenHits); got != 2 {
		t.Fatalf("app token 已过期，应随之重新获取，期望 getAccessToken 2 次，实际 %d 次", got)
	}
}

// expires_in <= 安全余量（60s）→ 不缓存，每次都重新请求。
func TestJsapiTicketNotCachedBelowMargin(t *testing.T) {
	var ticketHits int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		atomic.AddInt32(&ticketHits, 1)
		writeJsapiTicket(w, "t", 60) // 恰好等于余量 → 不缓存（要求 > 60 才缓存）
	})
	for i := 0; i < 3; i++ {
		if _, err := c.JsapiTicket(context.Background()); err != nil {
			t.Fatalf("第 %d 次 JsapiTicket 失败：%v", i+1, err)
		}
	}
	if got := atomic.LoadInt32(&ticketHits); got != 3 {
		t.Fatalf("expires_in=60（<=60s 余量）时不应缓存，期望 3 次，实际 %d 次", got)
	}
}

// 服务端拒绝（实测生产返回 501 accesstoken验证失败）应返回错误，且不泄漏 token。
func TestJsapiTicketDenied(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		_, _ = w.Write([]byte(`{"data":null,"errorCode":501,"success":false,"error":"accesstoken验证失败"}`))
	})
	_, err := c.JsapiTicket(context.Background())
	if err == nil {
		t.Fatalf("501 应返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "501") {
		t.Fatalf("错误应含 errorCode=501，实际 %q", err.Error())
	}
	if strings.Contains(err.Error(), "tok-abc") {
		t.Fatalf("错误信息不应泄漏 accessToken：%s", err.Error())
	}
}

// ---------------------------------------------------------------------------
// SignJSAPI：纯本地计算 + stripHash 接线
// ---------------------------------------------------------------------------

func TestSignJSAPIWiring(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		writeJsapiTicket(w, "jsapi-ticket-ABCDEF123456", 600)
	})
	ctx := context.Background()

	sig, err := c.SignJSAPI(ctx, katURL, false)
	if err != nil {
		t.Fatalf("SignJSAPI 失败：%v", err)
	}
	if sig.AppID != "app-test-1" {
		t.Fatalf("appId 期望 app-test-1，实际 %q", sig.AppID)
	}
	if sig.TimeStamp == "" || sig.NonceStr == "" || sig.Signature == "" {
		t.Fatalf("载荷字段不应为空：%+v", sig)
	}
	// timeStamp：13 位毫秒字符串（十进制）。
	if _, err := strconv.ParseInt(sig.TimeStamp, 10, 64); err != nil {
		t.Fatalf("timeStamp 应为十进制字符串，实际 %q", sig.TimeStamp)
	}
	// nonceStr：小数记法，不应出现科学计数法（避免客户端解析歧义）。
	if _, err := strconv.ParseFloat(sig.NonceStr, 64); err != nil {
		t.Fatalf("nonceStr 应为浮点字符串，实际 %q", sig.NonceStr)
	}
	if strings.ContainsAny(sig.NonceStr, "eE") || !strings.Contains(sig.NonceStr, ".") {
		t.Fatalf("nonceStr 应为定点小数记法，实际 %q", sig.NonceStr)
	}

	// stripHash=false → 按原样 url（含 #）签名。
	want := jsapiSignature("jsapi-ticket-ABCDEF123456", jsapiBaseline(sig.NonceStr, sig.TimeStamp, katURL))
	if sig.Signature != want {
		t.Fatalf("stripHash=false 应以原样 url 签名，期望 %q，实际 %q", want, sig.Signature)
	}

	// stripHash=true → 以剥掉 # 片段的 url 签名。
	sig2, err := c.SignJSAPI(ctx, katURL, true)
	if err != nil {
		t.Fatalf("SignJSAPI(stripHash=true) 失败：%v", err)
	}
	want2 := jsapiSignature("jsapi-ticket-ABCDEF123456", jsapiBaseline(sig2.NonceStr, sig2.TimeStamp, "https://fams.example.com/"))
	if sig2.Signature != want2 {
		t.Fatalf("stripHash=true 应剥掉 # 片段，期望 %q，实际 %q", want2, sig2.Signature)
	}
}

func TestSignJSAPIEmptyURL(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if isTokenPath(r) {
			writeToken(w, 7200)
			return
		}
		writeJsapiTicket(w, "t", 600)
	})
	if _, err := c.SignJSAPI(context.Background(), "   ", false); err == nil {
		t.Fatalf("空 url 应返回错误")
	}
}

func TestStripURLHash(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://h/#/repairs/123", "https://h/"},
		{"https://h/repairs/123", "https://h/repairs/123"},
		{"https://h/#", "https://h/"},
		{"https://h/", "https://h/"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := stripURLHash(tc.in); got != tc.want {
			t.Fatalf("stripURLHash(%q) 期望 %q，实际 %q", tc.in, tc.want, got)
		}
	}
}
