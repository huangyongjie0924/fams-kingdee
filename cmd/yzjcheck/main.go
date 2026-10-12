// yzjcheck 云之家（云之家）待办连通性自检 —— **零副作用**。
//
// 为什么需要它：用户报「收不到待办」时，第一件要分清的事是
//
//	「接口 / 凭证不通」 还是 「通知逻辑根本没触发」 —— 这两类的排查方向完全相反。
//
// 没有这个工具，就只能「发一条真待办试试」，而那样会污染真实单据、还会给无关的人
// 推一条假待办。本工具用 checkcreatetodo 实现：**不创建任何待办**即可验证
// 「凭证有效 + 接口可达 + appId 正确」（方案 §10.5）。
//
// 它和通知服务是互补的：
//
//	yzjcheck —— 通道通不通（凭证 / 网络 / appId）
//	通知服务 —— 逻辑该不该发（触发矩阵 / 幂等状态机 / openId 是否回填）
//
// ⚠️ 零副作用约定（硬要求）：固定使用**不存在的** sourceitemid 与**无效的** openId，
// **绝不**用真实单据 / 真实 openId。本文件里出现的两个探针常量都是刻意造假的。
//
// 用法：
//
//	# 凭证走环境变量（与主程序一致：config.yaml 里 app_id/secret 留空，靠 env 覆盖）
//	YZJ_APP_ID=xxx YZJ_SECRET=yyy go run ./cmd/yzjcheck -config config.yaml
//	# 或先 source .env 再跑
//
// 退出码（供脚本 / 运维判读）：
//
//	0 = 正常；1 = 凭证或授权问题；2 = 频率限制；3 = 其它错误
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"asset-mgr/config"
	"asset-mgr/integration/yunzhijia"
)

// 探针常量：两个值都是**刻意造假**的，保证零副作用。
//
//   - sourceitemid 用一个不可能存在的批次号：若它"存在"，说明有人拿真实单据在探测，
//     或者我们撞上了真实数据 —— 两种情况都必须让操作者看见，而不是静默通过。
//   - openId 用一个语法上像但不可能是真人的值：查询语义只看 sourceitemid 是否存在，
//     所以无效 openId 不会影响判定，但能避免把真实用户标识写进探针日志。
const (
	probeSourceItemID = "fams-probe-nonexistent-0001"
	probeOpenID       = "fams-probe-invalid-openid"
)

// 退出码。
const (
	exitOK          = 0
	exitCredential  = 1
	exitRateLimited = 2
	exitOther       = 3
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件")
	timeout := flag.Duration("timeout", 10*time.Second, "单次请求超时")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	yz := cfg.Yunzhijia
	// 前置检查：这三项缺任何一项都连 token 都拿不到，没必要再去打网络。
	// 报错要**指出怎么修**，而不是只报"配置缺失"——这个工具就是给排障用的。
	var missing []string
	if yz.BaseURL == "" {
		missing = append(missing, "yunzhijia.base_url")
	}
	if yz.AppID == "" {
		missing = append(missing, "yunzhijia.app_id（或环境变量 YZJ_APP_ID）")
	}
	if yz.Secret == "" {
		missing = append(missing, "yunzhijia.secret（或环境变量 YZJ_SECRET）")
	}
	if len(missing) > 0 {
		log.Fatalf("配置缺失：%s\n提示：凭证通常走环境变量注入，请确认已 source .env 或显式设置。",
			strings.Join(missing, "、"))
	}

	fmt.Printf("云之家待办连通性自检（零副作用）\n")
	fmt.Printf("  base_url = %s\n", yz.BaseURL)
	fmt.Printf("  app_id   = %s\n", yz.AppID)
	fmt.Printf("  探针     = sourceitemid=%s openId=%s（均为造假值，不会创建任何待办）\n\n",
		probeSourceItemID, probeOpenID)

	ctx := context.Background()
	client := yunzhijia.NewClient(yz.BaseURL, yz.AppID, yz.Secret, *timeout)

	// ── 第 1 步：取 app 级 accessToken ────────────────────────────────────────
	// 这一步单独报，因为它的失败原因和后面完全不同（网络 / base_url / app_id / secret），
	// 混在一起报会让操作者分不清是"连不上"还是"没权限"。
	fmt.Printf("[1/2] 获取 accessToken ... ")
	if _, err := client.GetAppToken(ctx); err != nil {
		fmt.Printf("失败\n")
		fmt.Printf("\n✗ 结论：**凭证或网络不通**，待办一定发不出去。\n")
		fmt.Printf("  原始错误：%v\n", err)
		fmt.Printf("  排查方向：base_url 是否可达（生产只有 yzj.example.com 能出网）；\n")
		fmt.Printf("            YZJ_APP_ID / YZJ_SECRET 是否与云之家后台一致。\n")
		os.Exit(exitCredential)
	}
	fmt.Printf("成功\n")

	// ── 第 2 步：checkcreatetodo（不创建待办） ─────────────────────────────────
	fmt.Printf("[2/2] 查询探针待办状态 ... ")
	res, err := client.CheckCreateTodo(ctx, &yunzhijia.CheckTodoRequest{
		// SourceType 留空 → 客户端回退 appID（已实测免登 appId 与待办 appId 是同一个）。
		SourceItemID: probeSourceItemID,
		OpenID:       probeOpenID,
	})
	if err != nil {
		fmt.Printf("失败\n")
		// 分类是**尽力而为**的字符串匹配：客户端的错误串里带了 errorCode，
		// 但它没有导出结构化字段，所以这里只能按文本判。**无论判成哪一类，
		// 原始错误都照原样打出来**——宁可多打一行，也不要让操作者被我们的分类误导。
		msg := err.Error()
		code := exitOther
		hint := "排查方向：检查云之家后台该应用是否已开通待办能力、凭证是否被停用。"
		switch {
		case strings.Contains(msg, "10000401"), strings.Contains(msg, "404"):
			code = exitCredential
			hint = "排查方向：凭证或授权问题（10000401 = 认证失败；404 = 接口地址不存在）。"
		case strings.Contains(msg, "202"):
			code = exitRateLimited
			hint = "排查方向：触发频率限制，稍后重试即可。"
		}
		fmt.Printf("\n✗ 结论：**接口或凭证有问题**，待办一定发不出去。\n")
		fmt.Printf("  原始错误：%v\n", err)
		fmt.Printf("  %s\n", hint)
		os.Exit(code)
	}

	// ── 判定 ─────────────────────────────────────────────────────────────────
	// 依据 §10.5：探针的 sourceitemid 不存在，所以正常结果必然是
	// check=true 且 dealCount==0 && undelCount==0（"单据不存在"）。
	// **始终把原始值打出来**：分类只是给人看的辅助，判定依据必须是原始值。
	fmt.Printf("成功\n\n")
	fmt.Printf("  原始返回：sourceid=%q appid=%q check=%v dealCount=%d undelCount=%d\n\n",
		res.SourceID, res.AppID, res.Check.Bool(), res.DealCount, res.UnDelCount)

	// appId 回显是这一步最有价值的副产品：它直接证明"我们配的 app_id 就是云之家认的那个应用"，
	// 这是排查"待办发给了错误的 app"这类问题的唯一直接证据。
	if res.AppID != "" && res.AppID != yz.AppID {
		fmt.Printf("⚠️ appId 回显（%s）与配置（%s）不一致 —— 待办会挂到另一个应用下。\n\n",
			res.AppID, yz.AppID)
	}

	if !res.Check.Bool() {
		fmt.Printf("✗ 结论：接口通了、凭证有效，但 check=false。\n")
		fmt.Printf("  这不是凭证问题（否则第 1 步就失败了）。请把上面的原始返回发给云之家确认语义。\n")
		os.Exit(exitOther)
	}
	if res.DealCount != 0 || res.UnDelCount != 0 {
		// 探针单据不该存在。出现计数说明撞上了真实数据（或有人用了同一个探针 ID），
		// 此时"通过"是假象，必须报出来。
		fmt.Printf("✗ 结论：探针单据居然存在（dealCount=%d undelCount=%d），本次自检结论不可信。\n",
			res.DealCount, res.UnDelCount)
		fmt.Printf("  排查方向：换一个探针 sourceitemid；确认没有真实业务在用这个 ID。\n")
		os.Exit(exitOther)
	}

	fmt.Printf("✓ 结论：**通道正常** —— 凭证有效、接口可达、appId 正确。\n")
	fmt.Printf("  探针单据不存在（check=true, dealCount=0, undelCount=0），符合预期。\n")
	fmt.Printf("  若此时用户仍收不到待办，问题不在通道，请转向通知逻辑：\n")
	fmt.Printf("    - todo_enabled 是否真的为 true（启动日志会打印自动关闭的原因）；\n")
	fmt.Printf("    - 收件人的 employee.open_id 是否已回填（未免登过的人只能靠通讯录同步补）；\n")
	fmt.Printf("    - 触发矩阵是否覆盖该跃迁；\n")
	fmt.Printf("    - 查 repair_notify_task 表里 state='failed' 的行与 last_error。\n")
	os.Exit(exitOK)
}
