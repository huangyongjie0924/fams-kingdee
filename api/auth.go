package api

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"asset-mgr/model"
)

type ctxKey string

const userKey ctxKey = "user"

// repairScopeKey 把「单据收窄」值放进 request context，供后续集成点读取
// （api/repair.go 的 loadRepairInScope 会用 repairScopeFrom(r) 取它，再校验 rsc == o.ID）。
const repairScopeKey ctxKey = "repair_scope"

type claims struct {
	UserID   int64  `json:"uid"`
	Username string `json:"usr"`
	RealName string `json:"nam"`
	// Role 是**主角色**（= Roles[0]），保留以兼容旧 token / 旧前端。
	Role string `json:"rol"`
	// Roles 是账号的全部角色（一人可多角色，2026-09-24 需求）。
	// 旧 token 没有该字段，读出来是 nil，EffectiveRoles() 会自动回落到 [Role]，无需兼容分支。
	Roles []string `json:"rls,omitempty"`
	// EmployeeID 随 token 下发，盘点模块据此判断「我的盘点是哪些」
	EmployeeID int64 `json:"eid"`
	// RepairScope 是「单据收窄」声明：第 2 层深链免登换来的会话只允许读这一张维修单
	// （§5.4 缺陷 2）。0 表示普通会话，不做收窄。
	RepairScope int64 `json:"rsc,omitempty"`
	// Typ 是用途声明。会话 token 不带该字段；深链免登 token 带 typ=yzj_deeplink。
	// Authenticate 显式拒绝 typ=yzj_deeplink 的 token（§5.4 缺陷 1 的兜底）。
	Typ string `json:"typ,omitempty"`
	jwt.RegisteredClaims
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	trimAll(&req.Username)
	if req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "用户名和密码不能为空")
		return
	}

	u, err := s.st.Authenticate(req.Username, req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "登录失败")
		return
	}
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}

	signed, err := s.issueToken(u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "签发 token 失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": signed, "user": u})
}

// issueToken 为登录成功的账号签发 JWT，供密码登录与 SSO 免登共用。
func (s *Server) issueToken(u *model.User) (string, error) {
	exp := time.Now().Add(time.Duration(s.cfg.Auth.TokenHours) * time.Hour)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		UserID:     u.ID,
		Username:   u.Username,
		RealName:   u.RealName,
		Role:       u.Role,
		Roles:      u.EffectiveRoles(),
		EmployeeID: u.EmployeeID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})
	return tok.SignedString([]byte(s.cfg.Auth.JWTSecret))
}

// issueScopedToken 与 issueToken 相同，但额外带上 rsc（单据收窄）声明：
// 该会话在中间件里被限制为「只读这一张维修单」（§5.4 缺陷 2）。
// 供第 2 层深链免登（api/deeplink.go）使用。
func (s *Server) issueScopedToken(u *model.User, repairScope int64) (string, error) {
	exp := time.Now().Add(time.Duration(s.cfg.Auth.TokenHours) * time.Hour)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		UserID:      u.ID,
		Username:    u.Username,
		RealName:    u.RealName,
		Role:        u.Role,
		Roles:       u.EffectiveRoles(),
		EmployeeID:  u.EmployeeID,
		RepairScope: repairScope,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})
	return tok.SignedString([]byte(s.cfg.Auth.JWTSecret))
}

// handleMe 返回当前会话的身份，并**自省只读状态**（docs/云之家待办通知集成方案.md §12-Q16 方案 D）。
//
// 为什么要把「我是只读会话」告诉客户端：经第 2 层深链免登进来的会话带 rsc（repair_scope>0），
// 中间件 deeplinkScopeAllows 对非 GET 一律拒绝 ⇒ 该会话**只读**。若客户端无从知道这一点，
// 页面会照常渲染「受理 / 接单 / 确认」等写按钮，用户一点就 403 —— 渲染一个注定失败的按钮
// 是糟糕的 UX。把 readonly 暴露出来后，客户端可**隐藏**这些按钮并改显示「登录后处理」入口。
//
// 形状说明：原先直接回 model.User，现改为信封 {user, readonly, repair_scope}。
// 已确认 /api/me 无既有前端消费方（web/src 全仓 grep 无命中），故改形状不破坏调用方。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	scope := repairScopeFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":         userFrom(r),
		"readonly":     scope > 0,
		"repair_scope": scope,
	})
}

// SecurityHeaders 给**所有**响应补安全响应头（docs/云之家待办通知集成方案.md §5.7）。
//
// 为什么本次必做：web/index.html 原本没有任何外部子资源，引入云之家 qing.js
// （免登第 1 层，§5.3）后页面出现了第一个外部子资源，引用来源策略必须显式声明。
//
// ⚠️ **禁止项（会静默破坏免登，别好心办坏事）**：
//   - **不得**添加 `X-Frame-Options: DENY`；
//   - **不得**收紧 `Content-Security-Policy: frame-ancestors`。
//
// 理由：免登第 1 层（首选路径）依赖 FAMS 页面在云之家门户 iframe 内执行 JS 桥
// `qing.call('getTicket')`。一旦禁掉被 iframe 嵌入，第 1 层直接失效，而**单测不会变红**、
// 只有真实客户端才看得出（§5.8 M7）。若确需限制 iframe，必须先用真实门户验证桥路径不受影响。
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// Authenticate 包住整棵路由：除免登端点（/api/login、/api/sso/login、
// /api/sso/deeplink-login）外所有 /api/* 都必须带合法 token。
// 前端静态资源不拦截，页面本身没有敏感数据，数据全走 API。
//
// 顺带套一层 SecurityHeaders——它挂在这里而不是 main.go，是因为 Authenticate 已经
// 包住了「静态资源 + API」整棵树（main.go 的 handler := srv.Authenticate(mux)），
// 这样 index.html 也会带上安全响应头，无需再改装配代码。
func (s *Server) Authenticate(next http.Handler) http.Handler {
	return SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") ||
			r.URL.Path == "/api/login" ||
			r.URL.Path == "/api/sso/login" ||
			r.URL.Path == "/api/sso/deeplink-login" {
			next.ServeHTTP(w, r)
			return
		}

		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if raw == "" {
			writeErr(w, http.StatusUnauthorized, "未登录")
			return
		}
		var c claims
		tok, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
			return []byte(s.cfg.Auth.JWTSecret), nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if err != nil || !tok.Valid {
			writeErr(w, http.StatusUnauthorized, "登录状态已失效，请重新登录")
			return
		}
		// 用途声明兜底（§5.4 缺陷 1）：深链免登 token 绝不当会话 token 用。
		// 独立密钥已能挡住（签名对不上，上面那步就 401 了），这条是「将来有人把
		// login_token_secret 与 jwt_secret 统一了」时的兜底，必须有、必须有测试。
		if c.Typ == deeplinkClaimType {
			writeErr(w, http.StatusUnauthorized, "登录状态已失效，请重新登录")
			return
		}

		u := model.User{
			ID:         c.UserID,
			Username:   c.Username,
			RealName:   c.RealName,
			Role:       c.Role,
			Roles:      c.Roles,
			EmployeeID: c.EmployeeID,
			Enabled:    true,
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		// 单据收窄（§5.4 缺陷 2）：深链免登会话只允许读它自己那一张维修单，其余路径 403。
		// 每次 403 都记一条日志（含 path 与 rsc），便于联调时立刻发现漏了哪条路由。
		if c.RepairScope > 0 {
			if !deeplinkScopeAllows(r.Method, r.URL.Path, c.RepairScope) {
				log.Printf("deeplink 会话收窄拦截：path=%q method=%s rsc=%d", r.URL.Path, r.Method, c.RepairScope)
				writeErr(w, http.StatusForbidden, "免登会话仅可查看该维修单")
				return
			}
			ctx = context.WithValue(ctx, repairScopeKey, c.RepairScope)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	}))
}

// repairScopeFrom 返回请求上下文里的「单据收窄」值（0 表示无收窄）。
//
// 供 api/repair.go 的 loadRepairInScope 使用（后续集成点，本轮不改 repair.go）：
// 当返回值 > 0 时，该会话只应被允许访问该维修单（校验 rsc == o.ID）。
func repairScopeFrom(r *http.Request) int64 {
	if v, ok := r.Context().Value(repairScopeKey).(int64); ok {
		return v
	}
	return 0
}

func userFrom(r *http.Request) model.User {
	if u, ok := r.Context().Value(userKey).(model.User); ok {
		return u
	}
	return model.User{}
}

func operatorOf(r *http.Request) string {
	u := userFrom(r)
	if u.RealName != "" {
		return u.RealName
	}
	return u.Username
}
