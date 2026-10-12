package yunzhijia

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// pathGenerateJSAPITicket 是 jsapiticket 获取接口路径。
//
// ⚠️ 与仓库其它接口（/gateway/...）不同，本接口**不在 /gateway/ 前缀下**。
// 这是**实测结论**，不是照文档推断——探针记录（2026-09-21，生产凭证，仅认证类调用，零业务副作用）：
//
//	POST /authorization/jsapi/generateticket           → HTTP 200，业务体 {"success":false,"errorCode":501,"error":"accesstoken验证失败"}
//	POST /gateway/authorization/jsapi/generateticket   → HTTP 200，业务体 {"errorCode":10000404,"error":"请求的URI地址不存在"}
//	GET  /authorization/jsapi/generateticket           → HTTP 405（仅支持 POST）
//	POST /jsapi/generateticket                         → HTTP 404（openresty）
//
// 第一条返回**业务错误**而非「URI 不存在」，证明**路由真实存在**于非 gateway 形态；
// 第二条的 10000404 证明 /gateway/ 前缀是错的。故真实路径取下面这一条。
const pathGenerateJSAPITicket = "/authorization/jsapi/generateticket"

// jsapiTicketSafetyMargin 是 jsapiticket 缓存的安全余量。
//
// ⚠️ 刻意**不复用** tokenSafetyMargin(300s)：jsapiticket 的 expires_in 只有 600s，
// 若沿用 300s 会把票的有效期砍掉一半。这里取 60s 的独立、更小余量。
//
// 与 K11 同理，expires_in 应按「剩余秒数（倒计时）」理解（K11 已实测 app token 的
// expireIn 递减：7200 → 7161）。jsapiticket 的 expires_in 是否也递减**未实测确认**——
// 本次探针因接口返回 501（见下）未能取到 ticket，故无法观测；本实现按「剩余秒数」保守处理。
const jsapiTicketSafetyMargin = 60 * time.Second

// JSAPISignature 是返回给云之家客户端的签名载荷。
//
// ⚠️ JSON 键名必须与文档**逐字一致**（client-api_lightapp_bridge-auth.md:97-107 的返回示例）：
//   - appId / timeStamp / nonceStr / signature，均为**字符串**（文档示例里 timeStamp、
//     nonceStr 都是带引号的字符串，如 timeStamp:'1530867948317'、nonceStr:'0.7057841489531929'）。
//   - 特别注意 **timeStamp 是驼峰**（大写 S）；而基线串里的时间戳参数名是全小写 `timestamp`
//     （见 jsapiBaseline）。返回键与基线参数名**不是**同一套拼写。
type JSAPISignature struct {
	AppID     string `json:"appId"`
	TimeStamp string `json:"timeStamp"`
	NonceStr  string `json:"nonceStr"`
	Signature string `json:"signature"`
}

// JsapiTicket 获取并缓存 jsapiticket（按返回的 expires_in 动态缓存）。
//
// 缓存与 app token **完全分离**：独立字段（jsapiTicket/jsapiTicketExpiry）+ 独立锁（jsapiMu）。
//   - 命中缓存（未过期）→ 直接返回，不请求、也不触碰 app token；
//   - 缓存时长 = expires_in - jsapiTicketSafetyMargin(60s)；
//   - expires_in <= 安全余量（含缺失/为 0）→ **不缓存**，本次直接用、下次重新获取。
func (c *Client) JsapiTicket(ctx context.Context) (string, error) {
	c.jsapiMu.Lock()
	defer c.jsapiMu.Unlock()

	if c.jsapiTicket != "" && time.Now().Before(c.jsapiTicketExpiry) {
		return c.jsapiTicket, nil
	}
	ticket, expireIn, err := c.fetchJsapiTicket(ctx)
	if err != nil {
		return "", err
	}
	c.jsapiTicket = ticket
	if time.Duration(expireIn)*time.Second > jsapiTicketSafetyMargin {
		c.jsapiTicketExpiry = time.Now().Add(time.Duration(expireIn)*time.Second - jsapiTicketSafetyMargin)
	} else {
		// 剩余寿命不足安全余量（或服务端未给 expires_in）：不缓存，下次重新获取。
		c.jsapiTicketExpiry = time.Time{}
	}
	return ticket, nil
}

// fetchJsapiTicket 调 generateticket 取一枚新票。
//
// 请求形态（文档 client-api_lightapp_bridge-auth.md:132-147）：
//   - method: POST
//   - Content-Type: application/x-www-form-urlencoded（**表单编码**，非 JSON——同 K10 那类坑）
//   - body: accesstoken=<app 级 accessToken>
//
// 返回取 data.jsapiticket 与 data.expires_in。token 传入仅用于错误脱敏（scrub）。
func (c *Client) fetchJsapiTicket(ctx context.Context) (string, int, error) {
	token, err := c.GetAppToken(ctx)
	if err != nil {
		return "", 0, err
	}
	form := url.Values{}
	form.Set("accesstoken", token)

	var resp struct {
		Data struct {
			JsapiTicket string `json:"jsapiticket"`
			ExpiresIn   int    `json:"expires_in"`
		} `json:"data"`
		ErrorCode int      `json:"errorCode"`
		Success   flexBool `json:"success"`
		Error     string   `json:"error"`
	}
	if err := c.doForm(ctx, pathGenerateJSAPITicket, token, form, &resp); err != nil {
		return "", 0, fmt.Errorf("yunzhijia jsapi ticket: %w", err)
	}
	if !resp.Success.Bool() || resp.Data.JsapiTicket == "" {
		return "", 0, fmt.Errorf("yunzhijia jsapi ticket denied: errorCode=%d error=%s", resp.ErrorCode, resp.Error)
	}
	return resp.Data.JsapiTicket, resp.Data.ExpiresIn, nil
}

// SignJSAPI 生成免登第 1 层（JS 桥 getTicket）所需的签名载荷。
//
// 步骤（文档 client-api_lightapp_bridge-auth.md:149-192）：
//
//	signature = Base64( HMAC-SHA1( baseline, jsapiticket ) )
//	baseline  = "noncestr=" + nonceStr + "&timestamp=" + timestamp + "&url=" + url
//
// 纯本地计算：拿到 jsapiticket 后**不再发起任何网络请求**，无副作用。
//
// stripHash 说明（设计 §5.3 陷阱 2）：本系统前端是 hash 路由，深链形如
// https://host/#/repairs/123。客户端 POST 上来的 url 含不含 `#` 片段，文档未写；
// 而 HMAC 是**单值**（一个 baseline 只能出一个签名），无法两种形态都返回，故留此开关：
//   - false（默认）：按收到的 url 原样签名（文档语义：客户端送来什么就签什么）；
//   - true：签名前先剥掉 `#` 及其后片段。
//
// ⚠️ 该开关的最终取值**必须真机联调**才能定，属未验证项。
func (c *Client) SignJSAPI(ctx context.Context, pageURL string, stripHash bool) (JSAPISignature, error) {
	if strings.TrimSpace(pageURL) == "" {
		return JSAPISignature{}, fmt.Errorf("yunzhijia jsapi sign: url 不能为空")
	}
	ticket, err := c.JsapiTicket(ctx)
	if err != nil {
		return JSAPISignature{}, err
	}
	signURL := pageURL
	if stripHash {
		signURL = stripURLHash(pageURL)
	}
	nonceStr := generateNonceStr()
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	return buildJSAPISignature(c.appID, ticket, nonceStr, timestamp, signURL), nil
}

// buildJSAPISignature 由给定参数计算完整签名载荷（纯函数，便于用固定向量做已知答案测试）。
func buildJSAPISignature(appID, ticket, nonceStr, timestamp, signURL string) JSAPISignature {
	baseline := jsapiBaseline(nonceStr, timestamp, signURL)
	return JSAPISignature{
		AppID:     appID,
		TimeStamp: timestamp,
		NonceStr:  nonceStr,
		Signature: jsapiSignature(ticket, baseline),
	}
}

// jsapiBaseline 拼接待签名的基线串。
//
// ⚠️ 陷阱 1（本集成第二关键的坑）：基线里的参数名是**全小写 `noncestr`**，而返回给客户端的
// JSON 键是**驼峰 `nonceStr`**（见 JSAPISignature）。两者**不可混用**，照抄必错：
//   - 基线全小写：client-api_lightapp_bridge-auth.md:180
//   - 返回 JSON 驼峰：client-api_lightapp_bridge-auth.md:103
//
// ⚠️ 未定项（段间分隔符 `&`）：本函数按**团队规范 / 设计 §5.3**用 `&` 连接三段。
// 但同一文档的参考实现（:180 的 Java 片段）**没有 `&`**，而是直接首尾相接：
//
//	baseline = "noncestr=" + noncestr + "timestamp=" + timestamp + "url=" + url
//
// 两种写法会产生**不同签名**（对同一组固定向量：带 `&` = "tDBu2p3vU9uIOXeGOm7KblFU+2Q="，
// 不带 `&` = "loo2YRPwCbkR/CI6rEJto+TZRIY="，见 jsapi_test.go 的已知答案向量）。
// 该差异**必须真机联调**确认；若实测为「无 `&`」形态，只需改此一处即可（其余逻辑不变）。
func jsapiBaseline(nonceStr, timestamp, signURL string) string {
	return "noncestr=" + nonceStr + "&timestamp=" + timestamp + "&url=" + signURL
}

// jsapiSignature 计算 signature = Base64(HMAC-SHA1(baseline, jsapiticket))。
// jsapiticket 作 HMAC 密钥，baseline 作消息；输出标准 Base64（含 `=` 填充）。
func jsapiSignature(ticket, baseline string) string {
	mac := hmac.New(sha1.New, []byte(ticket))
	mac.Write([]byte(baseline))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// generateNonceStr 生成随机 nonceStr。形态对齐文档示例（Java 的 String.valueOf(Math.random())，
// 如 "0.7057841489531929"）：固定小数记法、16 位小数，避免出现科学计数法。
// nonce 无需保密（客户端原样回传），故用 math/rand 即可（Go 1.20+ 自动播种）。
func generateNonceStr() string {
	return strconv.FormatFloat(rand.Float64(), 'f', 16, 64)
}

// stripURLHash 去掉 URL 中 `#` 及其后片段（hash 路由场景，见设计 §5.3 陷阱 2）。
func stripURLHash(raw string) string {
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		return raw[:i]
	}
	return raw
}
