package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"asset-mgr/model"
)

// deeplinkClaimType 是第 2 层深链免登 token 的**用途声明**（claims 里的 typ）。
//
// 它与会话 token 共用同一个 claims 名字空间，靠这个值把「用途」钉死：
//   - 签发时：MintDeeplinkToken 恒写 typ=yzj_deeplink；
//   - 校验时：handleDeeplinkLogin 必须显式断言 typ == yzj_deeplink，拿到别的类型一律拒；
//   - 中间件：Authenticate 显式拒绝 typ == yzj_deeplink 的 token，绝不当会话 token 用。
//
// 见 docs/云之家待办通知集成方案.md §5.4 缺陷 1。
const deeplinkClaimType = "yzj_deeplink"

// deeplinkFailMsg 是 deeplink-login 失败时对外返回的**通用**文案。
//
// 刻意不细分「过期 / 签名不对 / 用途不符 / 绑定缺失」——区分原因会给探测者信息（§5.4 缺陷 1）。
// 前端对第 2 层失败是静默降级的，所以这条文案不面向用户展示，但也不能泄漏原因。
const deeplinkFailMsg = "免登已失效，请重新登录"

// errDeeplinkDisabled 表示未配置独立签名密钥，第 2 层不可用（不是服务器错误）。
var errDeeplinkDisabled = errors.New("免登未启用：未配置 yunzhijia.login_token_secret")

// errAccountDisabled 表示匹配到的账号已被停用（与 sso.go 的「账号已停用」同一语义）。
var errAccountDisabled = errors.New("账号已停用")

// deeplinkClaims 是深链免登 token 的载荷。
//
// 与会话 claims（api/auth.go）刻意分开定义：深链 token 只携带「用途 + 绑定对象 + 有效期」，
// 不含 uid/usr/rol 等会话字段——它换不到任何权限，只能去 deeplink-login 换一枚**收窄**的会话。
type deeplinkClaims struct {
	// Typ 用途声明，恒为 deeplinkClaimType。
	Typ string `json:"typ"`
	// EmpID 绑定收件人（员工 id）：token 只对这一名员工有效。
	EmpID int64 `json:"eid"`
	// RepairID 绑定单据（维修单 id）：token 只对这一张单有效（§5.4 缺陷 2）。
	RepairID int64 `json:"rid"`
	jwt.RegisteredClaims
}

// MintDeeplinkToken 为「某员工 + 某维修单」签发一枚深链免登 token。
//
// 安全要点（§5.4 缺陷 1/2）：
//   - 用 **yunzhijia.login_token_secret** 独立密钥签 HS256，与会话 token（auth.jwt_secret）
//     物理隔离——**绝不**回退 auth.jwt_secret；
//   - claims 含 typ=yzj_deeplink（用途声明）、eid（绑定收件人）、rid（绑定单据）、exp（TTL）；
//   - empID 或 repairID <= 0 直接报错——绝不签出「没有绑定对象」的 token。
//
// 供后续集成点调用（api/notify.go 的 deepLink() 把返回值拼进待办 url 的 ?yzj_token=）：
//
//	token, err := s.MintDeeplinkToken(empID, repairID)
//
// 未配置 login_token_secret 时返回 errDeeplinkDisabled（调用方应据此跳过第 2 层，而不是报错）。
func (s *Server) MintDeeplinkToken(empID, repairID int64) (string, error) {
	return mintDeeplinkToken(s.cfg.Yunzhijia.LoginTokenSecret, s.cfg.Yunzhijia.DeeplinkTokenTTL, empID, repairID)
}

// mintDeeplinkToken 是签发逻辑的**唯一实现**（Server 方法只是薄封装）。
//
// 之所以抽成包级函数：待办卡片 URL 里的 token 由 api/notify.go 的 NotifyService 签发，
// 而通知服务并不持有 *Server。若为它复制一份签发逻辑，两处迟早漂移——尤其是「用哪把密钥」
// 与 typ 取值这两个安全要点（§5.4 缺陷 1）。所以只留这一个实现。
//
// ttl <= 0 时回退 24h：与 config.Load 的兜底一致，使本函数在任何调用路径下都不会签出
// 「立刻过期」的 token。
func mintDeeplinkToken(secret string, ttl time.Duration, empID, repairID int64) (string, error) {
	if secret == "" {
		return "", errDeeplinkDisabled
	}
	if empID <= 0 || repairID <= 0 {
		return "", fmt.Errorf("免登 token 必须绑定员工与单据（empID=%d repairID=%d）", empID, repairID)
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, deeplinkClaims{
		Typ:      deeplinkClaimType,
		EmpID:    empID,
		RepairID: repairID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	})
	return tok.SignedString([]byte(secret))
}

// handleDeeplinkLogin 第 2 层免登：用 URL 里的一次性 token 换一枚**收窄**的会话 token。
//
// 请求体 {"token": "..."}；成功返回 {"token","user"}（与 POST /api/sso/login 同形状，
// 因为前端 exchangeDeeplinkToken() 读的就是 {token, user}）。
//
// 失败一律 401 + 通用文案；未配置密钥返回 503「免登未启用」（不是 500 panic）。
func (s *Server) handleDeeplinkLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Token == "" {
		writeErr(w, http.StatusBadRequest, "缺少 token")
		return
	}

	secret := s.cfg.Yunzhijia.LoginTokenSecret
	if secret == "" {
		// 第 2 层未启用：不是服务器错误，明确回一个「未启用」状态；前端会静默降级到第 3 层。
		writeErr(w, http.StatusServiceUnavailable, "免登未启用")
		return
	}

	var c deeplinkClaims
	// 必须用独立密钥 + 显式限定 HS256；必须显式断言 typ（拿到别的类型的 token 一律拒）。
	tok, err := jwt.ParseWithClaims(req.Token, &c, func(t *jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	// 失败一律 401 + 通用文案。日志只记「长度 + 用途」，**绝不打印 token 本身**（§9 脱敏约定）。
	if err != nil || !tok.Valid || c.Typ != deeplinkClaimType || c.EmpID <= 0 || c.RepairID <= 0 {
		log.Printf("deeplink-login 校验失败：len=%d typ=%q", len(req.Token), c.Typ)
		writeErr(w, http.StatusUnauthorized, deeplinkFailMsg)
		return
	}

	// 复用 handleSSOLogin 的同一套账号解析逻辑（查员工 → 查/建账号 → 停用则拒）。
	user, err := s.resolveOrCreateSSOUser(c.EmpID)
	if err != nil {
		if errors.Is(err, errAccountDisabled) {
			writeErr(w, http.StatusUnauthorized, deeplinkFailMsg)
			return
		}
		log.Printf("deeplink-login 解析账号失败：empID=%d err=%v", c.EmpID, err)
		writeErr(w, http.StatusUnauthorized, deeplinkFailMsg)
		return
	}

	// 会话收窄：签发的会话 token 带 rsc=rid，使其**只能读这一张维修单**（§5.4 缺陷 2）。
	signed, err := s.issueScopedToken(user, c.RepairID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "签发 token 失败")
		return
	}
	log.Printf("deeplink-login ok: user_id=%d empID=%d rid=%d", user.ID, c.EmpID, c.RepairID)
	// readonly:true 与 /api/me 的 readonly 是**同一个事实**（rsc>0 ⇒ 只读会话）的第二种暴露方式：
	// 前端在拿到 token 的同一刻就知道自己只读，可立即隐藏写按钮、显示「登录后处理」入口，
	// 省掉一次「先登录再调 /api/me」的往返（§12-Q16 方案 D）。
	writeJSON(w, http.StatusOK, map[string]any{"token": signed, "user": user, "readonly": true})
}

// resolveOrCreateSSOUser 复刻 api/sso.go handleSSOLogin 的账号解析逻辑（§5.4 缺陷 2 要求复用同一套）：
// 按员工查账号；没有则自动建 employee 员工账号；账号停用则拒绝。
//
// 与 sso.go 的唯一差别：sso.go 建号用的工号/姓名来自云之家 ticket 响应，深链免登没有该响应，
// 改从本地员工主数据取（口径一致：username 都是工号）。
//
// 说明：api/sso.go 不在本次改动文件范围内（并行开发中），故这里抽成独立函数而非直接改
// handleSSOLogin 调用它；后续可把 handleSSOLogin 也改为调用本函数以彻底去重。
func (s *Server) resolveOrCreateSSOUser(empID int64) (*model.User, error) {
	user, err := s.st.UserByEmployee(empID)
	if err != nil {
		return nil, err
	}
	if user != nil {
		if !user.Enabled {
			return nil, errAccountDisabled
		}
		return user, nil
	}

	no, name, err := s.employeeNoAndName(empID)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = no
	}
	id, err := s.st.CreateSSOUser(no, name, []string{model.RoleEmployee}, empID)
	if err != nil {
		return nil, err
	}
	return &model.User{
		ID:         id,
		Username:   no,
		RealName:   name,
		Role:       model.RoleEmployee,
		Roles:      []string{model.RoleEmployee},
		EmployeeID: empID,
		Enabled:    true,
	}, nil
}

// employeeNoAndName 从员工主数据取工号与姓名，供「无账号时自动建号」使用。
//
// store 未提供「按 id 取员工」的方法（store/ 不在本次改动范围内），故遍历一次员工列表。
// 该路径只在「员工从未登录过、需自动建号」时命中，不是热路径，O(n) 可接受。
func (s *Server) employeeNoAndName(empID int64) (no, name string, err error) {
	emps, err := s.st.ListEmployees("")
	if err != nil {
		return "", "", err
	}
	for _, e := range emps {
		if e.ID == empID {
			return e.EmpNo, e.Name, nil
		}
	}
	return "", "", fmt.Errorf("员工不存在：id=%d", empID)
}

// deeplinkScopeAllows 判断「单据收窄会话」（rsc>0）是否允许访问某路由（§5.4 缺陷 2）。
//
// 设计意图：万一 URL 里的 token 泄漏，攻击者拿到的会话**只能读那一张单**，而不是这个员工的
// 全部权限——这是「凭据进 URL」这个取舍能被接受的前提，不得为省事放宽成全放行。
//
// 白名单的定法（依据：grep 前端 web/src/ 找维修单详情页实际会调的接口，见
// web/src/views/repair/Detail.vue 的 load() 与 onMounted()）：
//   - GET /api/repairs/{id}              详情（必须）
//   - GET /api/repairs/{id}/logs         处理进度
//   - GET /api/repairs/{id}/attachments  附件
//   - GET /api/me                        当前用户身份（身份自省，只返回调用者自己）
//   - GET /api/enums                     静态枚举（不查 DB、不含他人数据）
//
// 明确**不**放行 /api/employees、/api/vendors 等主数据列表：它们是全量数据，会突破
// 「只能读这一张单」的边界。详情页的派工对话框对这些调用是 .catch 兜底的，缺数据只表现为
// 下拉为空，不阻塞页面；且派工本身是写操作，收窄会话本就不允许（只读）。
func deeplinkScopeAllows(method, path string, scope int64) bool {
	if method != http.MethodGet {
		return false
	}
	switch path {
	case "/api/me", "/api/enums":
		return true
	}
	return repairPathIsOwnRead(path, scope)
}

// repairPathIsOwnRead 判断 path 是否形如本单的只读子路由，且其中的 {id} 等于 scope。
//
// 认三类：/api/repairs/{id}、/api/repairs/{id}/logs、/api/repairs/{id}/attachments。
// 其它（如 /accept、/dispatch 等写动作，或别的单据 id）一律不认。
func repairPathIsOwnRead(path string, scope int64) bool {
	rest, ok := strings.CutPrefix(path, "/api/repairs/")
	if !ok || rest == "" {
		return false
	}
	idPart := rest
	switch {
	case strings.HasSuffix(rest, "/logs"):
		idPart = strings.TrimSuffix(rest, "/logs")
	case strings.HasSuffix(rest, "/attachments"):
		idPart = strings.TrimSuffix(rest, "/attachments")
	}
	if idPart == "" || strings.Contains(idPart, "/") {
		return false
	}
	id, err := strconv.ParseInt(idPart, 10, 64)
	return err == nil && id == scope
}
