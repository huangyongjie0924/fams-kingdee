package api

import (
	"net/http"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// —— 缺陷 1「独立密钥」的**直接**验证（补 §10.1b 的缺口）——
//
// 背景（实测）：把 api/deeplink.go 的签发密钥从 login_token_secret 换成 auth.jwt_secret
// ——也就是缺陷 1 复发——之后，本包内**全部既有用例依然全绿**。原因是「免登 token 不能当
// 会话 token 用」这条 401 由两道防线共同保证：
//   ① 独立密钥：签名对不上，jwt.ParseWithClaims 直接失败；
//   ② typ 用途声明：api/auth.go 显式拒绝 typ == "yzj_deeplink"。
// 只破坏①时，②还在挡，所以既有用例察觉不到。
// 结论：此前**没有任何用例真正钉住「必须用独立密钥签发」**。下面两条补上这个缺口。

// TestMintDeeplinkTokenUsesIndependentKey 直接断言签发用的就是独立密钥。
//
// 反向验证：把 api/deeplink.go:67 的 `s.cfg.Yunzhijia.LoginTokenSecret` 改成
// `s.cfg.Auth.JWTSecret` ⇒ 本用例第 ② 步（用会话密钥验签必须失败）变红。
func TestMintDeeplinkTokenUsesIndependentKey(t *testing.T) {
	cfg := deeplinkTestConfig()
	if cfg.Yunzhijia.LoginTokenSecret == cfg.Auth.JWTSecret {
		t.Fatal("测试夹具失效：两个密钥相同，本用例会退化为恒真")
	}
	srv := NewServer(nil, cfg)

	tok, err := srv.MintDeeplinkToken(1, testScopedRepairID)
	if err != nil {
		t.Fatalf("签发免登 token 失败: %v", err)
	}

	// ① 用**免登**密钥验签：必须成功 —— 证明签发源就是独立密钥。
	if _, err := parseWith(t, tok, cfg.Yunzhijia.LoginTokenSecret); err != nil {
		t.Errorf("免登 token 用免登密钥验签失败（签发时用错密钥了？）: %v", err)
	}

	// ② 用**会话**密钥验签：必须失败 —— 证明没有复用 auth.jwt_secret。
	if _, err := parseWith(t, tok, cfg.Auth.JWTSecret); err == nil {
		t.Error("免登 token 竟能用会话密钥验签通过 —— 独立密钥隔离已失效（缺陷 1 复发）")
	}
}

// TestDeeplinkTokenStillRejectedWhenSecretsAreEqual 把「运维把两个密钥配成一样」
// 这一真实误配场景固化为回归用例。
//
// 该场景下防线①已失效，唯一还能挡的是 typ 用途声明。config.Load 会为这种配置打一条
// 警告（不报错），所以「配错也能起来」是既定行为——那么就必须有测试证明它**仍然安全**。
//
// 反向验证：删掉 api/auth.go 的 typ 判定后，本用例变红。
func TestDeeplinkTokenStillRejectedWhenSecretsAreEqual(t *testing.T) {
	cfg := deeplinkTestConfig()
	// 模拟误配：两把密钥配成同一个值。
	cfg.Auth.JWTSecret = testDeeplinkSecret
	cfg.Yunzhijia.LoginTokenSecret = testDeeplinkSecret
	srv := NewServer(nil, cfg)

	tok, err := srv.MintDeeplinkToken(1, testScopedRepairID)
	if err != nil {
		t.Fatalf("签发免登 token 失败: %v", err)
	}

	// 前置条件：该 token 确实能用会话密钥验签通过。若不成立，本用例就只是「因为签名
	// 不对才 401」，根本没考到 typ 兜底 —— 那样的绿灯是假绿灯。
	if _, err := parseWith(t, tok, cfg.Auth.JWTSecret); err != nil {
		t.Fatalf("前置条件不成立：token 未能用会话密钥验签（%v），本用例考不到 typ 兜底", err)
	}

	h := srv.Authenticate(stubMux())
	code, body := doReq(t, h, http.MethodGet, "/api/repairs", tok, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("两密钥相同时免登 token 被当会话 token 用：期望 401（typ 兜底），实际 %d，body=%s",
			code, body)
	}
}

// parseWith 用指定密钥解析并校验 token；任一环节失败都返回 error。
func parseWith(t *testing.T, raw, key string) (*claims, error) {
	t.Helper()
	var c claims
	tok, err := jwt.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) {
		return []byte(key), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, jwt.ErrTokenInvalidClaims
	}
	return &c, nil
}
