package config

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Addr        string `yaml:"addr"`
		TLSAddr     string `yaml:"tls_addr"` // HTTPS 监听地址，留空则不启用
		CertFile    string `yaml:"cert_file"`
		KeyFile     string `yaml:"key_file"`
		UploadDir   string `yaml:"upload_dir"`
		MaxUploadMB int64  `yaml:"max_upload_mb"`
	} `yaml:"server"`

	MySQL struct {
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		User     string `yaml:"user"`
		Password string `yaml:"password"`
		Database string `yaml:"database"`
	} `yaml:"mysql"`

	Auth struct {
		JWTSecret         string `yaml:"jwt_secret"`
		TokenHours        int    `yaml:"token_hours"`
		InitAdminUser     string `yaml:"init_admin_user"`
		InitAdminPassword string `yaml:"init_admin_password"`
	} `yaml:"auth"`

	CodeRule struct {
		Prefix   string `yaml:"prefix"`
		SeqWidth int    `yaml:"seq_width"`
	} `yaml:"code_rule"`

	Kingdee struct {
		BaseURL             string `yaml:"base_url"`
		ClientID            string `yaml:"client_id"`
		ClientSecret        string `yaml:"client_secret"`
		Username            string `yaml:"username"`
		AccountID           string `yaml:"account_id"`
		RequestTimeout      int    `yaml:"request_timeout"`
		PageSize            int    `yaml:"page_size"`
		MaxRetries          int    `yaml:"max_retries"`
		QueryPath           string `yaml:"query_path"`
		PersonnelQueryPath  string `yaml:"personnel_query_path"`
		DepartmentQueryPath string `yaml:"department_query_path"`
	} `yaml:"kingdee"`

	Yunzhijia struct {
		BaseURL string `yaml:"base_url"`
		AppID   string `yaml:"app_id"`
		Secret  string `yaml:"secret"`

		// —— 待办通知（见 docs/云之家待办通知集成方案.md §8）——
		// TodoEnabled 是待办总开关，默认 false（灰度）：关闭时所有通知为 no-op。
		TodoEnabled bool `yaml:"todo_enabled"`
		// TodoAppID 是待办 appId（generatetodo 的 appId）。已实测与免登 app_id 是同一个
		// 「第三方应用ID」（checkcreatetodo 回显 appid=500000084），留空时自动回退 AppID；
		// 保留独立键仅为将来可能的覆盖。
		TodoAppID string `yaml:"todo_app_id"`
		// HeadImg 是待办图标 URL（generatetodo 必填；缺失会创建失败或发出点不开的待办）。
		HeadImg string `yaml:"head_img"`
		// TodoType：0=通知中心 / 1=待审批。P0 统一 0（应用已被勾选为「通知类」）。
		TodoType int `yaml:"todo_type"`
		// TagID 通知分类，仅当应用被勾选为「通知类」时生效（P0 可不填）。
		TagID string `yaml:"tag_id"`
		// SenderID 发送人 openId（选填）。
		SenderID string `yaml:"sender_id"`
		// EID 团队id，通讯录增量同步用（§6.6）。
		EID string `yaml:"eid"`
		// PortalBaseURL 是 FAMS 对外基址（含协议），拼待办深链用（§5.2）。启用待办时必填。
		PortalBaseURL string `yaml:"portal_base_url"`
		// DirectorySyncEnabled 是否启用云之家通讯录增量同步（openId 回填路径 B）。
		DirectorySyncEnabled bool `yaml:"directory_sync_enabled"`

		// InformationalEnabled 是「信息性告知」开关（§3.2 的 #2/#5/#7 共三处），默认 false。
		//
		// 涉及三处（都是给收件人的「进展告知」、不要求他行动）：
		//   #2 受理后给报修人的「已受理」、#5 接单后给报修人的「维修进行中」、
		//   #7 确认完工后给维修工的「已完工」。
		//
		// 语义（产品定义 Q2 拍板）：这类通知应「推，但置『已读』不打扰」——即**新建一条已读待办**
		// （见 api/notify_plan.go 的 ensureReadOp / informationalNotice），而不是让它以「未读」
		// 挂在收件人的待办列表里。
		//
		// 为什么必须两步：云之家 generatetodo 的 params 每项只有 openId/url/content、
		// **没有 read 字段**，故「新建即已读」只能 create 成功后紧跟 action{read:1}。
		//
		// 流程中心通道下这三处**不产生**（该通道无「待办置已读」，只有 todo/done 与
		// carboncopy/read）——见 docs/云之家流程中心集成方案.md §11 第 4 项。
		InformationalEnabled bool `yaml:"informational_enabled"`

		// —— 统一流程中心（见 docs/云之家流程中心集成方案.md §8）——
		// FlowCenterEnabled 是流程中心总开关，默认 false：关闭时新待办仍走 im-todo，
		// 行为与现在完全一致。打开后「新」待办走流程中心，而「旧」待办
		// （repair_notify_task.channel='im-todo'）继续走 im-todo —— 两种集成方式必须并存，
		// 否则切换前已发出的旧待办在云之家永远转不成已办。
		FlowCenterEnabled bool `yaml:"flowcenter_enabled"`
		// FlowCenterTypeID 是业务系统模板标识（init 的 typeId），本项目取 "WXSQD"（维修申请单）。
		//
		// 官方文档原文：typeId「**可以是任意值**」，由业务系统自己定义（示例：付款申请用
		// FQSK，请假用 QJSQ）。**不需要去云之家查询**——云之家侧只有 appId 与 signKey
		// 需要在「轻应用管理中心」查（appId 已实测为 500000084）。
		//
		// 唯一约束来自 send 接口：「对于同一个流程，每次发送待办都应该使用相同的 typeId」。
		// 所以**一旦开始发待办就不能再改**（已发出的待办绑定它），但 init 之前可自由改。
		//
		// ⚠️ 别把这条与「必读」第 1 条混淆：第 1 条约束的是 **AppId**
		//（原文「上线后请勿再修改集成参数，尤其是 AppId」），不是 typeId。
		FlowCenterTypeID string `yaml:"flowcenter_type_id"`
		// FlowCenterTypeName 是模板名称（智能审批里显示的流程名），如「维修工单」。
		FlowCenterTypeName string `yaml:"flowcenter_type_name"`
		// FlowCenterAppName 是模板所属分类名（一般填轻应用名）。
		FlowCenterAppName string `yaml:"flowcenter_app_name"`
		// FlowCenterWebURL / FlowCenterMobileURL 是业务系统发起页地址（统一发起用，可留空）。
		FlowCenterWebURL    string `yaml:"flowcenter_web_url"`
		FlowCenterMobileURL string `yaml:"flowcenter_mobile_url"`
		// FlowCenterSignKey 是快捷同意的签名密钥（轻应用管理中心可查；P0 不做快捷同意可留空）。
		FlowCenterSignKey string `yaml:"flowcenter_sign_key"`

		// —— 深链免登（第 2 层，见 docs/云之家待办通知集成方案.md §5.4）——
		// LoginTokenSecret 是深链免登 token 的**独立**签名密钥。
		//
		// **必须独立于 auth.jwt_secret**（物理隔离，§5.4 缺陷 1 的修法核心）——**绝不回退**
		// auth.jwt_secret：一旦两者相同，一枚免登 token 就能当会话 token 直接用，拿到该员工的全部权限。
		//
		// 留空 ⇒ 第 2 层**静默禁用**（记一条明确日志），但**不影响**第 0 层（APP ticket）
		// 与第 3 层（登录页）——不能因为这一层没配就让进程启动失败。
		LoginTokenSecret string `yaml:"login_token_secret"`
		// DeeplinkTokenTTL 是第 2 层免登 token 的有效期（§5.4 缺陷 3）。
		//
		// 配置写法为 Go 时长字符串（如 "24h" / "90m"）；<= 0 时回退 24h。
		// 取值须覆盖「待办可点击窗口」——token 在待办创建时签发，用户可能数小时后才点击；
		// 越长越方便、泄露窗口越大。
		DeeplinkTokenTTL time.Duration `yaml:"deeplink_token_ttl"`
	} `yaml:"yunzhijia"`

	Sync struct {
		Enable bool `yaml:"enable"`
		// DailyAt 是每日定时同步时刻，格式 "HH:MM"（本地时区），默认 00:00。
		//
		// 取代了原来的 interval_minutes（每 N 分钟跑一次资产卡）：
		// 现在一个批次里先同步组织主数据、再同步资产卡，两者必须成对且有序，
		// 按分钟间隔各跑各的会把顺序打散。
		DailyAt       string `yaml:"daily_at"`
		DryRun        bool   `yaml:"dry_run"`
		AllowFullSync bool   `yaml:"allow_full_sync"`
	} `yaml:"sync"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.UploadDir == "" {
		c.Server.UploadDir = "uploads"
	}
	if c.Server.MaxUploadMB <= 0 {
		c.Server.MaxUploadMB = 10
	}
	if c.Auth.TokenHours <= 0 {
		c.Auth.TokenHours = 12
	}
	if c.CodeRule.SeqWidth <= 0 {
		c.CodeRule.SeqWidth = 5
	}
	if c.Kingdee.RequestTimeout <= 0 {
		c.Kingdee.RequestTimeout = 30
	}
	if c.Kingdee.PageSize <= 0 {
		c.Kingdee.PageSize = 200
	}
	if c.Kingdee.PageSize > 1000 {
		c.Kingdee.PageSize = 1000
	}
	if c.Kingdee.MaxRetries < 0 {
		c.Kingdee.MaxRetries = 3
	}
	if c.Kingdee.QueryPath == "" {
		c.Kingdee.QueryPath = "/v2/gcgs/fa/fa_asset_card/Select_AssetCard"
	}
	if c.Kingdee.PersonnelQueryPath == "" {
		c.Kingdee.PersonnelQueryPath = "/v2/gcgs/base/bos_user/query-personnel"
	}
	if c.Kingdee.DepartmentQueryPath == "" {
		c.Kingdee.DepartmentQueryPath = "/v2/gcgs/base/bos_adminorg/query-department"
	}
	if c.Sync.DailyAt == "" {
		c.Sync.DailyAt = "00:00"
	}
	// 在启动时就把格式错误打出来。放到调度器里解析的话，一个写错的 "24:00"
	// 会表现为「定时同步静默不跑」，而这类问题通常要等到第二天早上才发现。
	if _, _, err := ParseDailyAt(c.Sync.DailyAt); err != nil {
		return nil, fmt.Errorf("sync.daily_at 配置有误: %w", err)
	}
	// 云之家基础地址不设默认值：各企业用自己的私有化域名，写死只会让
	// 「配置漏填」表现为运行时连不上。留空时仍可用 YZJ_BASE_URL 注入。

	// 敏感字段允许通过环境变量覆盖，避免写入配置文件
	overrideIfSet("KINGDEE_BASE_URL", &c.Kingdee.BaseURL)
	overrideIfSet("KINGDEE_CLIENT_ID", &c.Kingdee.ClientID)
	overrideIfSet("KINGDEE_CLIENT_SECRET", &c.Kingdee.ClientSecret)
	overrideIfSet("KINGDEE_USERNAME", &c.Kingdee.Username)
	overrideIfSet("KINGDEE_ACCOUNT_ID", &c.Kingdee.AccountID)
	overrideIfSet("YZJ_BASE_URL", &c.Yunzhijia.BaseURL)
	overrideIfSet("YZJ_APP_ID", &c.Yunzhijia.AppID)
	overrideIfSet("YZJ_SECRET", &c.Yunzhijia.Secret)
	overrideIfSet("YZJ_EID", &c.Yunzhijia.EID)
	overrideIfSet("YZJ_PORTAL_BASE_URL", &c.Yunzhijia.PortalBaseURL)
	overrideIfSet("YZJ_TODO_APP_ID", &c.Yunzhijia.TodoAppID)
	overrideIfSet("YZJ_HEAD_IMG", &c.Yunzhijia.HeadImg)
	overrideBoolIfSet("YZJ_TODO_ENABLED", &c.Yunzhijia.TodoEnabled)
	overrideBoolIfSet("YZJ_DIRECTORY_SYNC_ENABLED", &c.Yunzhijia.DirectorySyncEnabled)
	// 信息性告知开关（#2/#5/#7 三处「新建已读待办」）。它不是必填项，缺省即 false，
	// 故**不**进 MissingTodoConfig / MissingFlowCenterConfig。
	overrideBoolIfSet("YZJ_INFORMATIONAL_ENABLED", &c.Yunzhijia.InformationalEnabled)

	// 统一流程中心（cloudflow）。注意：team 级 accessToken 必须带 eid，故启用流程中心时
	// YZJ_EID 必填（见 docs/云之家流程中心集成方案.md §3.1）。
	overrideBoolIfSet("YZJ_FLOWCENTER_ENABLED", &c.Yunzhijia.FlowCenterEnabled)
	overrideIfSet("YZJ_FLOWCENTER_TYPE_ID", &c.Yunzhijia.FlowCenterTypeID)
	overrideIfSet("YZJ_FLOWCENTER_TYPE_NAME", &c.Yunzhijia.FlowCenterTypeName)
	overrideIfSet("YZJ_FLOWCENTER_APP_NAME", &c.Yunzhijia.FlowCenterAppName)
	overrideIfSet("YZJ_FLOWCENTER_WEB_URL", &c.Yunzhijia.FlowCenterWebURL)
	overrideIfSet("YZJ_FLOWCENTER_MOBILE_URL", &c.Yunzhijia.FlowCenterMobileURL)
	overrideIfSet("YZJ_FLOWCENTER_SIGN_KEY", &c.Yunzhijia.FlowCenterSignKey)

	// 深链免登（第 2 层，§5.4）。密钥优先走环境变量，避免写进配置文件。
	// TTL 两个入口等价：YZJ_DEEPLINK_TOKEN_TTL 用时长字符串（如 "24h"），
	// YZJ_DEEPLINK_TOKEN_TTL_HOURS 用整数小时；两者都设时后者优先（更具体）。
	overrideIfSet("YZJ_LOGIN_TOKEN_SECRET", &c.Yunzhijia.LoginTokenSecret)
	overrideDurationIfSet("YZJ_DEEPLINK_TOKEN_TTL", &c.Yunzhijia.DeeplinkTokenTTL)
	overrideHoursIfSet("YZJ_DEEPLINK_TOKEN_TTL_HOURS", &c.Yunzhijia.DeeplinkTokenTTL)
	if c.Yunzhijia.DeeplinkTokenTTL <= 0 {
		c.Yunzhijia.DeeplinkTokenTTL = 24 * time.Hour
	}
	// 未配置独立密钥 ⇒ 第 2 层静默禁用（**不是启动失败**）：第 0 层与第 3 层仍可用，
	// 不能因为这一层没配就整体起不来。日志写清楚「哪一层被禁用、怎么恢复」。
	if c.Yunzhijia.LoginTokenSecret == "" {
		log.Printf("配置提示：yunzhijia.login_token_secret 未配置，第 2 层（深链免登 yzj_token）已静默禁用；" +
			"第 0 层（APP ticket）与第 3 层（登录页回跳）不受影响。如需启用请配置该独立密钥" +
			"（环境变量 YZJ_LOGIN_TOKEN_SECRET，**勿复用 auth.jwt_secret**）")
	}

	// 独立密钥的**强制性**检查（§5.4 缺陷 1）：文档要求 login_token_secret 必须独立于
	// auth.jwt_secret。两者相同 ⇒ 免登 token 与会话 token 签名同源，缺陷 1 的「物理隔离」
	// 已失效，此时**仅剩** typ 用途声明一道兜底（api/auth.go 的 typ 判定）。
	// 这是「静默的安全配置错误」——服务照常起来、日志里什么也看不出，必须显式提示。
	// 不直接返回 error：typ 兜底仍能挡住两个方向的混用（有测试 TestDeeplinkTokenStill
	// RejectedWhenSecretsAreEqual 钉住），为一条可自愈的告警把已有部署打挂不划算。
	if c.Yunzhijia.LoginTokenSecret != "" && c.Yunzhijia.LoginTokenSecret == c.Auth.JWTSecret {
		log.Printf("配置警告：yunzhijia.login_token_secret 与 auth.jwt_secret 相同，第 2 层深链免登的" +
			"密钥隔离已失效（缺陷 1），当前仅靠 token 的 typ 用途声明兜底。请改用独立的随机密钥" +
			"（环境变量 YZJ_LOGIN_TOKEN_SECRET，**勿复用 auth.jwt_secret**）")
	}

	// 待办 appId 默认回退免登 appId（已实测同一「第三方应用ID」，§8）。
	// 必须在 AppID 的环境变量覆盖之后再做，否则 YZJ_APP_ID 注入时会回退到空值。
	if c.Yunzhijia.TodoAppID == "" {
		c.Yunzhijia.TodoAppID = c.Yunzhijia.AppID
	}
	// 待办总开关的启动防御（§8）：todo_enabled=true 但必填项为空时，**记一条明确错误日志
	// 并把待办功能当作关闭**——url（portal_base_url）与 headImg（head_img）是 generatetodo 的
	// **必填**字段，缺失会创建失败或发出「点不开的待办」，绝不能放行；app_id / base_url 缺失
	// 则连 token 都拿不到。关闭后 TodoEnabled 即反映「实际生效状态」，调用方无需再判。
	if c.Yunzhijia.TodoEnabled {
		if missing := c.MissingTodoConfig(); len(missing) > 0 {
			log.Printf("配置错误：yunzhijia.todo_enabled=true 但缺少必填项 [%s]，已自动关闭待办通知"+
				"（url / headImg 是云之家 generatetodo 的必填字段，缺失会导致创建失败或发出点不开的待办）",
				strings.Join(missing, "、"))
			c.Yunzhijia.TodoEnabled = false
		}
	}

	// 统一流程中心总开关的启动防御（§8）。与待办同理：**缺必填项时记明确错误日志并当作关闭**，
	// 而不是放行后让每条通知在运行时静默失败。
	//
	// 为什么 type_id / type_name / app_name 一个都不能少：init 是流程中心的**建模板**接口，
	// 三者共同决定云之家侧模板的唯一标识与展示位置。其中 type_id 由我们自己定义（任意值），
	// 但 send 接口要求「同一个流程每次发送都使用相同的 typeId」，故**发过待办后不能改**。
	// eid 缺失则 team 级 accessToken 直接取不到。
	if c.Yunzhijia.FlowCenterEnabled {
		if missing := c.MissingFlowCenterConfig(); len(missing) > 0 {
			log.Printf("配置错误：yunzhijia.flowcenter_enabled=true 但缺少必填项 [%s]，已自动关闭"+
				"统一流程中心（新待办回退 im-todo，行为与未开启时一致）",
				strings.Join(missing, "、"))
			c.Yunzhijia.FlowCenterEnabled = false
		}
	}

	if c.Kingdee.BaseURL == "" || c.Kingdee.ClientID == "" || c.Kingdee.ClientSecret == "" ||
		c.Kingdee.Username == "" || c.Kingdee.AccountID == "" {
		// 未配置金蝶时关闭同步，避免启动失败
		c.Sync.Enable = false
	}
	if c.Auth.JWTSecret == "" {
		return nil, fmt.Errorf("auth.jwt_secret must be set")
	}
	return &c, nil
}

func overrideIfSet(key string, target *string) {
	if v := os.Getenv(key); v != "" {
		*target = v
	}
}

// overrideBoolIfSet 与 overrideIfSet 相同，但解析布尔值。
// 接受 1/true/yes/on 与 0/false/no/off（大小写不敏感）；空值不覆盖；非法值忽略并记日志
// ——静默把 "yes" 当作 false 会让人以为「开关打开了」，这类误判比直接报错难查。
func overrideBoolIfSet(key string, target *bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		*target = true
	case "0", "false", "no", "off":
		*target = false
	default:
		log.Printf("环境变量 %s=%q 不是合法布尔值，已忽略", key, v)
	}
}

// overrideDurationIfSet 与 overrideIfSet 相同，但把环境变量解析为 time.Duration（如 "24h" / "90m"）。
// 空值不覆盖；非法值记日志并忽略——与 overrideBoolIfSet 同一哲学：静默当作默认值会让人
// 以为「配上了」，这类误判比直接报错难查。
func overrideDurationIfSet(key string, target *time.Duration) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("环境变量 %s=%q 不是合法时长（如 24h / 90m），已忽略", key, v)
		return
	}
	*target = d
}

// overrideHoursIfSet 与 overrideIfSet 相同，但把「整数小时」解析为 time.Duration。
// 供 YZJ_DEEPLINK_TOKEN_TTL_HOURS 使用（与 deeplink_token_ttl 的时长写法等价的便捷入口）。
// 只接受正整数小时；非法值记日志并忽略。
func overrideHoursIfSet(key string, target *time.Duration) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return
	}
	h, err := strconv.Atoi(v)
	if err != nil || h <= 0 {
		log.Printf("环境变量 %s=%q 不是合法正整数小时，已忽略", key, v)
		return
	}
	*target = time.Duration(h) * time.Hour
}

// MissingTodoConfig 返回启用待办通知所必需、但为空的配置项（人类可读名）。
// 按依赖顺序列出：base_url → app_id → head_img → portal_base_url。
//
// 缺一不可的理由：base_url 决定能否连通云之家；app_id 决定能否拿到 token；
// head_img 与 portal_base_url 分别是 generatetodo 的必填字段 url / headImg 的来源，
// 缺了会创建失败或发出「点不开的待办」。空列表表示配置齐备。
func (c *Config) MissingTodoConfig() []string {
	var missing []string
	if c.Yunzhijia.BaseURL == "" {
		missing = append(missing, "base_url")
	}
	if c.Yunzhijia.TodoAppID == "" {
		missing = append(missing, "app_id")
	}
	if c.Yunzhijia.HeadImg == "" {
		missing = append(missing, "head_img")
	}
	if c.Yunzhijia.PortalBaseURL == "" {
		missing = append(missing, "portal_base_url")
	}
	return missing
}

// MissingFlowCenterConfig 返回启用统一流程中心所必需、但为空的配置项（人类可读名）。
// 空列表表示配置齐备。
//
// 逐项理由：
//   - base_url：决定能否连通云之家（与待办共用同一网关域名）；
//   - eid：**team 级 accessToken 的必填参数**，缺失时认证接口直接返回 11000400 参数错误；
//   - app_id：init 建模板时的业务系统标识（AppID 为空时客户端回退到 c.appID）；
//   - type_id / type_name / app_name：init 的必填参数，且 type_id 上线后不可改。
//
// 注意 portal_base_url **不在**此列：流程中心待办不经过 generatetodo，
// 不需要它的 url / headImg 必填字段，跳转地址由编排层按 channel 各自生成。
func (c *Config) MissingFlowCenterConfig() []string {
	var missing []string
	if c.Yunzhijia.BaseURL == "" {
		missing = append(missing, "base_url")
	}
	if c.Yunzhijia.EID == "" {
		missing = append(missing, "eid")
	}
	if c.Yunzhijia.TodoAppID == "" {
		missing = append(missing, "app_id")
	}
	if c.Yunzhijia.FlowCenterTypeID == "" {
		missing = append(missing, "flowcenter_type_id")
	}
	if c.Yunzhijia.FlowCenterTypeName == "" {
		missing = append(missing, "flowcenter_type_name")
	}
	if c.Yunzhijia.FlowCenterAppName == "" {
		missing = append(missing, "flowcenter_app_name")
	}
	return missing
}

// ParseDailyAt 解析 "HH:MM" 形式的每日执行时刻。
//
// 严格要求两位：只收 "00:00" / "23:59"，不收 "0:0" / "24:00" / "00:60"。
// 配置文件里的时间是要长期躺着不动的，宽松解析省下的几个字符，
// 换来的是"0:0 到底是零点还是没写"这种只能靠猜的歧义。
func ParseDailyAt(v string) (hour, minute int, err error) {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, 0, fmt.Errorf("格式应为 HH:MM（例如 00:00），实际是 %q", v)
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("小时不是数字: %q", parts[0])
	}
	minute, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("分钟不是数字: %q", parts[1])
	}
	if hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("小时应在 00~23，实际是 %d", hour)
	}
	if minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("分钟应在 00~59，实际是 %d", minute)
	}
	return hour, minute, nil
}

// DSN 固定用 +08:00 数值偏移，不用命名时区：MySQL 实例可能缺时区表。
func (c *Config) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=true&loc=Local&time_zone=%s",
		c.MySQL.User, c.MySQL.Password, c.MySQL.Host, c.MySQL.Port, c.MySQL.Database,
		url.QueryEscape("'+08:00'"))
}
