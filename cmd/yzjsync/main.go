// yzjsync 手动触发一次云之家通讯录增量同步（openId 回填路径 B）。
//
// 为什么需要它：eid 一旦配置好，运维需要**立刻**把全体在职员工的 openId 回填上，
// 而不是等第二天凌晨的定时批次——「当天能不能打开 todo_enabled」就取决于这一跑
// （在此之前，每个收件人都会走「无云之家 openId」跳过分支，一条待办都发不出去）。
//
// 与 cmd/yzjcheck 的分工：
//
//	yzjcheck —— 通道通不通（凭证 / 网络 / appId）
//	yzjsync  —— 把通讯录里的 openId 真正回填到 employee.open_id
//
// 用法（凭证走环境变量，与主程序一致：config.yaml 里 app_id/secret/eid 可留空靠 env 覆盖）：
//
//	YZJ_APP_ID=xxx YZJ_SECRET=yyy YZJ_EID=10603457 go run ./cmd/yzjsync -config config.yaml
//
// 退出码（供脚本 / 运维判读）：
//
//	0 = 全部成功（失败数为 0）；1 = 有失败或配置缺失 / 同步未执行
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
	"asset-mgr/store"
	"asset-mgr/syncer"
)

const (
	exitOK   = 0
	exitFail = 1
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件")
	triggeredBy := flag.String("by", "yzjsync", "写入 sync_run.triggered_by 的触发来源")
	timeout := flag.Duration("timeout", 30*time.Minute, "整轮同步超时")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	yz := cfg.Yunzhijia
	// 前置检查：这几项缺任何一项都跑不起来，先报清楚、别去打网络。
	// 报错要**指出怎么修**——这个工具就是给排障/上线用的。
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
	if strings.TrimSpace(yz.EID) == "" {
		missing = append(missing, "yunzhijia.eid（或环境变量 YZJ_EID）")
	}
	if len(missing) > 0 {
		fmt.Printf("✗ 配置缺失：%s\n", strings.Join(missing, "、"))
		fmt.Printf("  提示：凭证 / eid 通常走环境变量注入，请确认已 source .env 或显式设置。\n")
		os.Exit(exitFail)
	}

	fmt.Printf("云之家通讯录增量同步（openId 回填，路径 B）\n")
	fmt.Printf("  base_url = %s\n", yz.BaseURL)
	fmt.Printf("  app_id   = %s\n", yz.AppID)
	fmt.Printf("  eid      = %s\n", yz.EID)
	if !yz.DirectorySyncEnabled {
		// 本工具是**显式手动触发**：开关控制的是定时批次。这里在内存中强制启用，
		// 不改配置文件、也不影响定时批次的开关——否则用户会以为"跑了"，其实被门禁挡掉。
		fmt.Printf("  注意：yunzhijia.directory_sync_enabled=false；本工具为显式手动触发，\n")
		fmt.Printf("        将在本次运行内强制启用（不修改配置文件）。\n")
		cfg.Yunzhijia.DirectorySyncEnabled = true
	}
	fmt.Println()

	st, err := store.New(cfg.DSN())
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	client := yunzhijia.NewClient(yz.BaseURL, yz.AppID, yz.Secret, 15*time.Second)

	// kingdee client 传 nil：通讯录同步完全不碰星瀚链路。
	svc := syncer.NewService(st, nil, cfg)
	svc.SetYunzhijiaClient(client)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	res, err := svc.SyncYunzhijiaOrg(ctx, *triggeredBy)
	if err != nil {
		fmt.Printf("✗ 同步失败：%v\n", err)
		if res != nil {
			printResult(res)
		}
		os.Exit(exitFail)
	}
	if res == nil {
		fmt.Printf("✗ 同步未执行（门禁跳过）：请确认 directory_sync_enabled 与 eid 已配置。\n")
		os.Exit(exitFail)
	}

	printResult(res)
	if res.Failed > 0 {
		os.Exit(exitFail)
	}
	os.Exit(exitOK)
}

// printResult 打印统计。**「成功回填 N 人」是用户判断"能不能打开 todo_enabled"的唯一依据**，
// 因此单独、明确地打出来。
//
// 「跳过」拆成两行，是为了让用户一眼看懂：云之家通讯录（约 4465 人，整个工作圈）
// 远大于本系统花名册（employee 表仅百余人），因此**绝大多数**通讯录人员都会落进
// 「本系统无此工号」——这是**正常**的，不是故障。若不拆开，用户会把这一大坨数字
// 误读成「同步失败」。
func printResult(res *syncer.YzjOrgResult) {
	fmt.Printf("\n  ── 结果 ──\n")
	fmt.Printf("  拉取页数     = %d\n", res.Pages)
	fmt.Printf("  命中人员数   = %d\n", res.Fetched)
	fmt.Printf("  ✅ 成功回填  = %d 人\n", res.Updated)
	fmt.Printf("  跳过(缺工号/openId)  = %d\n", res.Skipped)
	fmt.Printf("  跳过(本系统无此工号) = %d   ← 正常：云之家通讯录远大于本系统花名册\n", res.NotInSystem)
	fmt.Printf("  失败         = %d（工号重复 / 拉取出错）\n", res.Failed)
	fmt.Printf("  游标         = %s\n", res.Cursor)
	fmt.Printf("  运行状态     = %s（sync_run.id=%d）\n", res.Status, res.RunID)
	fmt.Printf("\n  ✅ 成功回填 %d 人 —— 这是判断能否打开 todo_enabled 的依据。\n", res.Updated)
	fmt.Printf("     判据：只要「失败 = 0」即本次同步成功（退出码 0）。\n")
	fmt.Printf("     「跳过(本系统无此工号)」数值很大是**正常的**（云之家通讯录远大于本系统花名册），\n")
	fmt.Printf("     它不是失败、也不会写 sync_run_error，请勿据此判定同步异常。\n")
}
