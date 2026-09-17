package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"asset-mgr/model"
)

type ctxKey string

const userKey ctxKey = "user"

type claims struct {
	UserID   int64  `json:"uid"`
	Username string `json:"usr"`
	RealName string `json:"nam"`
	Role     string `json:"rol"`
	// EmployeeID 随 token 下发，盘点模块据此判断「我的盘点是哪些」
	EmployeeID int64 `json:"eid"`
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
		EmployeeID: u.EmployeeID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})
	return tok.SignedString([]byte(s.cfg.Auth.JWTSecret))
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, userFrom(r))
}

// Authenticate 包住整棵路由：除 /api/login 外所有 /api/* 都必须带合法 token。
// 前端静态资源不拦截，页面本身没有敏感数据，数据全走 API。
func (s *Server) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/login" || r.URL.Path == "/api/sso/login" {
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

		u := model.User{
			ID:         c.UserID,
			Username:   c.Username,
			RealName:   c.RealName,
			Role:       c.Role,
			EmployeeID: c.EmployeeID,
			Enabled:    true,
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
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
