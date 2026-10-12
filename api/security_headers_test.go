package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// 纯单测：不连库、不读 config.yaml，任何环境都必须跑起来。
//
// 为什么值得单独钉死：docs/云之家待办通知集成方案.md §5.7 里，
// 「必须加哪两个头」和「**绝对不能**加哪个头」是一体两面——
// 后半条一旦被违反（有人顺手加上 X-Frame-Options: DENY 或收紧
// CSP frame-ancestors），免登第 1 层（JS 桥，首选路径）会在真实门户里静默失效，
// 而**任何现有测试都不会变红**。所以这里把「禁止项」也写成断言。

// TestSecurityHeadersPresent 断言 §5.7 要求显式声明的两个头存在。
func TestSecurityHeadersPresent(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Errorf("Referrer-Policy = %q，期望 %q（§5.7）", got, "strict-origin-when-cross-origin")
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q，期望 %q（§5.7）", got, "nosniff")
	}
}

// TestSecurityHeadersDoNotBreakIframeEmbedding 钉死 §5.7 的**禁止项**。
//
// 反过来说：如果有人为了「安全加固」加上 X-Frame-Options: DENY，这个测试必须变红——
// 它挡住的是「单测全绿但免登在真实门户里废掉」这种最难查的故障。
func TestSecurityHeadersDoNotBreakIframeEmbedding(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Errorf("不得设置 X-Frame-Options（当前 %q）：免登第 1 层依赖页面在云之家门户 iframe 内执行 "+
			"qing.call('getTicket')，禁掉嵌入会让该路径静默失效（§5.7 禁止项 / §5.4 缺陷 5）", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "" {
		t.Errorf("不得收紧 Content-Security-Policy frame-ancestors（当前 %q）：同上，会破坏免登第 1 层（§5.7）", got)
	}
}

// TestAuthenticateAppliesSecurityHeaders 断言安全头真的覆盖到「静态资源 + API」两条路径。
//
// 只测 SecurityHeaders 本身不够：它必须挂在 Authenticate 外层（Authenticate 已包住整棵树），
// 否则 index.html 就带不上头——而 index.html 恰恰是引入 qing.js 的那个页面（§5.7 的立论起点）。
func TestAuthenticateAppliesSecurityHeaders(t *testing.T) {
	s := &Server{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := s.Authenticate(inner)

	cases := []struct {
		name string
		path string
		want int
	}{
		// 静态资源（index.html）：不鉴权，直接 200，但必须带安全头。
		{"静态资源", "/index.html", http.StatusOK},
		// API 无 token：401，但安全头仍应存在。
		{"API 未登录", "/api/me", http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))

			if rec.Code != c.want {
				t.Fatalf("%s 状态码 = %d，期望 %d", c.path, rec.Code, c.want)
			}
			if got := rec.Header().Get("Referrer-Policy"); got == "" {
				t.Errorf("%s 未带 Referrer-Policy", c.path)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got == "" {
				t.Errorf("%s 未带 X-Content-Type-Options", c.path)
			}
			if got := rec.Header().Get("X-Frame-Options"); got != "" {
				t.Errorf("%s 被设置了 X-Frame-Options=%q，会破坏免登第 1 层（§5.7）", c.path, got)
			}
		})
	}
}

// TestSSOBackfillsOpenIDSource 是「源码级」断言：§6.4 路径 A 必须在免登里回填 openId。
//
// 为什么不写成端到端：回填要连真库 + 真云之家 ticket，本地跑不起来；
// 而这条链路的失效方式恰恰是「静默漏发通知」——没人会发现。
// 所以退一步钉住「调用点存在」这个下限：`UpdateEmployeeOpenID` 一旦被删掉，
// 免登仍会正常工作（openId 只是通知的前置数据），只有这个测试会红。
func TestSSOBackfillsOpenIDSource(t *testing.T) {
	src, err := os.ReadFile("sso.go")
	if err != nil {
		t.Fatalf("读取 sso.go 失败：%v", err)
	}
	norm := strings.Join(strings.Fields(string(src)), " ")
	if !strings.Contains(norm, "UpdateEmployeeOpenID(empID, u.OpenID)") {
		t.Fatalf("api/sso.go 里找不到 `UpdateEmployeeOpenID(empID, u.OpenID)`——" +
			"§6.4 路径 A（免登回填 openId）被删了，待办通知会静默漏发给活跃用户")
	}
	// 回填必须发生在「工号匹配到员工」之后：否则会把 openId 写到 empID=0 上。
	if strings.Index(norm, "UpdateEmployeeOpenID(empID, u.OpenID)") < strings.Index(norm, "if empID == 0 {") {
		t.Fatalf("openId 回填出现在 `if empID == 0` 判空之前——empID 未确认就会写库（§6.4）")
	}
}
