// kdverify 核对「星瀚接口值」与「台账落库值」是否一致。
//
// 为什么需要它：同步跑完看到 updated=200 **不代表**对齐了——汇总数字正常、
// 单张卡的字段却可能是错的。上一轮就是靠逐条比对才抓出
// 「22 张卡残值率停在 5%、4 张卡使用期限停在 240」，
// 而当时汇总数字 `updated=200` 看起来完全正常。
//
// 它和 kdscan 是一对：
//
//	kdscan   —— 接口有什么（探测投影结构，回答"字段取不到值"）
//	kdverify —— 落库对不对（核对同步结果，回答"数字对不上"）
//
// 用法：
//
//	go run ./cmd/kdverify -config config.yaml
//
// 只比对**星瀚确实提供了值的**字段（原值 / 累计折旧 / 净值 / 税额 / 含税金额 /
// 财务使用期限 / 残值率）。星瀚没给（值为 0）的字段不做断言——那种情况下
// 台账保留的是人工维护值或类别默认值，本来就不该等于 0。
//
// 解析规则直接复用 syncer.ApplyFinEntry，不重新实现一遍：
// 核对工具自己算错的话，报出来的"不一致"就是假警，而假警比不报还糟——
// 它会让"看汇总数字"这种老习惯重新回来。
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"time"

	"asset-mgr/config"
	"asset-mgr/integration/kingdee"
	"asset-mgr/model"
	"asset-mgr/store"
	"asset-mgr/syncer"
)

// moneyTol 是金额比对容差。库列是 DECIMAL(14,2)、接口给 6 位小数，
// 比对前两边都已收敛到 2 位，取半分钱足够。
const moneyTol = 0.005

// finField 是一个待比对的财务字段。
//
// 用取值函数而不是字段名，是为了避免反射——显式列出来的清单同时也是
// 「我们承诺对齐哪些字段」的声明，改这里必须是有意识的。
type finField struct {
	label string
	pick  func(*model.AssetCard) float64
}

var finFields = []finField{
	{"原值", func(c *model.AssetCard) float64 { return c.FinOriginalValue }},
	{"累计折旧", func(c *model.AssetCard) float64 { return c.FinAccumDepreciaton }},
	{"净值", func(c *model.AssetCard) float64 { return c.FinNetValue }},
	{"财务使用期限", func(c *model.AssetCard) float64 { return float64(c.FinUseMonths) }},
	{"残值率", func(c *model.AssetCard) float64 { return c.FinResidualRate }},
	{"税额", func(c *model.AssetCard) float64 { return c.FinTax }},
	{"含税金额", func(c *model.AssetCard) float64 { return c.FinAmountWithTax }},
}

// fieldStat 记录单个字段的比对统计。
type fieldStat struct{ provided, mismatch int }

// compareFinFields 比对一张卡的财务字段：统计累加进 stats，返回不一致明细。
//
// 规则：只对「星瀚确实给了值」（want > 0）的字段做断言。
// 星瀚没给值时，台账保留的是人工维护值或类别默认值，本来就不该等于 0，
// 硬断言会制造大量假警——而假警比不报更糟，它会让"看汇总数字"的老习惯回来。
func compareFinFields(code string, want, actual *model.AssetCard, stats map[string]*fieldStat) []string {
	var out []string
	for _, f := range finFields {
		w := f.pick(want)
		if w <= 0 {
			continue
		}
		s := stats[f.label]
		if s == nil {
			s = &fieldStat{}
			stats[f.label] = s
		}
		s.provided++
		if g := f.pick(actual); math.Abs(g-w) > moneyTol {
			s.mismatch++
			out = append(out, fmt.Sprintf("  %-18s %-14s 星瀚 %12.2f   台账 %12.2f", code, f.label, w, g))
		}
	}
	return out
}

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	st, err := store.New(cfg.DSN())
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	client, err := kingdee.NewClient(cfg.KingdeeClientConfig())
	if err != nil {
		log.Fatalf("创建金蝶客户端失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	log.Println("拉取星瀚资产卡...")
	cards, err := client.SelectAssetCards(ctx, "")
	if err != nil {
		log.Fatalf("拉取失败: %v", err)
	}

	// 只处理带财务明细的行。同编码重复行里 finentry 为 null 的那份不参与比对：
	// 它本来就不该影响任何财务字段（见 syncer.ApplyFinEntry 的说明）。
	type target struct {
		code string
		want *model.AssetCard
	}
	var targets []target
	uniqueCodes := map[string]bool{}
	for i := range cards {
		src := &cards[i]
		uniqueCodes[src.Number] = true
		if len(src.FinEntry) == 0 {
			continue
		}
		var want model.AssetCard
		syncer.ApplyFinEntry(&want, &src.FinEntry[0])
		targets = append(targets, target{code: src.Number, want: &want})
	}

	fmt.Printf("\n=== 星瀚 ↔ 台账 财务字段核对 ===\n")
	fmt.Printf("拉取 %d 行 / 去重后 %d 个资产编码 / 其中带财务明细 %d 行\n",
		len(cards), len(uniqueCodes), len(targets))

	// 先把库里的卡取出来（同编码只查一次）
	cache := map[string]*model.AssetCard{}
	var missing []string
	for _, t := range targets {
		if _, ok := cache[t.code]; ok {
			continue
		}
		c, err := st.GetCardByCode(t.code)
		if err == sql.ErrNoRows {
			missing = append(missing, t.code)
			continue
		}
		if err != nil {
			log.Fatalf("查询 %s 失败: %v", t.code, err)
		}
		cache[t.code] = c
	}

	// ---- 逐字段比对 ----
	stats := map[string]*fieldStat{}
	var details []string
	for _, t := range targets {
		actual, ok := cache[t.code]
		if !ok {
			continue // 库里没有这张卡，已在"未同步"里单独列出
		}
		details = append(details, compareFinFields(t.code, t.want, actual, stats)...)
	}

	fmt.Printf("\n--- 逐字段比对（只比对星瀚确实提供了值的）---\n")
	fmt.Printf("%-16s %10s %10s\n", "字段", "星瀚有值", "不一致")
	totalMismatch := 0
	for _, f := range finFields {
		s := stats[f.label]
		if s == nil {
			fmt.Printf("%-16s %10s %10s\n", f.label, "-", "-")
			continue
		}
		fmt.Printf("%-16s %10d %10d\n", f.label, s.provided, s.mismatch)
		totalMismatch += s.mismatch
	}

	// ---- 库内勾稽验证 ----
	//
	// 逐字段比对只能证明「我们抄对了」，勾稽验证才能证明「这几个数彼此自洽」。
	// 两者缺一不可：星瀚给的两个数各自都抄对了，但它们之间不满足会计关系，
	// 那说明口径理解错了——这比抄错更难发现。
	var netN, netBad, taxN, taxBad int
	for _, t := range targets {
		c, ok := cache[t.code]
		if !ok || c.FinOriginalValue <= 0 {
			continue
		}
		netN++
		if math.Abs(c.FinNetValue-(c.FinOriginalValue-c.FinAccumDepreciaton)) > moneyTol {
			netBad++
			details = append(details, fmt.Sprintf("  %-18s %-14s 净值 %.2f ≠ 原值-累计折旧 %.2f",
				t.code, "勾稽不符", c.FinNetValue, c.FinOriginalValue-c.FinAccumDepreciaton))
		}
		if c.FinTax > 0 {
			taxN++
			if math.Abs(c.FinAmountWithTax-(c.FinOriginalValue+c.FinTax)) > moneyTol {
				taxBad++
				details = append(details, fmt.Sprintf("  %-18s %-14s 含税金额 %.2f ≠ 原值+税额 %.2f",
					t.code, "勾稽不符", c.FinAmountWithTax, c.FinOriginalValue+c.FinTax))
			}
		}
	}
	fmt.Printf("\n--- 库内勾稽验证 ---\n")
	fmt.Printf("  净值 == 原值 - 累计折旧      参与 %3d 张   不符 %d\n", netN, netBad)
	fmt.Printf("  含税金额 == 原值 + 税额      参与 %3d 张   不符 %d\n", taxN, taxBad)

	if len(missing) > 0 {
		sort.Strings(missing)
		fmt.Printf("\n--- 库中不存在的编码（接口有、台账没有）---\n")
		for _, c := range missing {
			fmt.Printf("  %s\n", c)
		}
	}

	fmt.Printf("\n--- 不一致明细 ---\n")
	if len(details) == 0 {
		fmt.Println("  （无）")
	} else {
		sort.Strings(details)
		for _, d := range details {
			fmt.Println(d)
		}
	}

	// 退出码留给脚本用：0 = 全部一致，1 = 有问题。
	bad := totalMismatch + netBad + taxBad + len(missing)
	if bad == 0 {
		fmt.Printf("\n结论：全部一致 ✓（比对 %d 张卡）\n", len(targets))
		return
	}
	fmt.Printf("\n结论：发现 %d 处问题（字段不一致 %d + 勾稽不符 %d + 未同步 %d）\n",
		bad, totalMismatch, netBad+taxBad, len(missing))
	os.Exit(1)
}
