package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"asset-mgr/config"
)

// captureStdout 把 reportDiff 的打印接住，用于断言"报了/没报什么"。
//
// 为什么要断言文本而不是返回值：reportDiff 的返回值只说"值不值得处理"，
// 而这次踩的坑恰恰是**方向错了**——0 行时它照常输出了「48 个字段全部消失」，
// 返回值也是 true（没错），但结论把人引向了"对方删了投影列"。
// 返回值对、内容错，这种 bug 只能靠断言内容抓住。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// 2026-09-18 的现场：人员接口被加了一条服务端过滤条件，
// 请求体怎么传都覆盖不了，行数从 5038 直接变 0。
//
// 这条变更完全不在返回数据里——0 行时连字段都没有，只能从 filter 回显上看见。
func TestDiffFilterCatchesServerSideFilterChange(t *testing.T) {
	old := "[null]"
	cur := "[(entryentity.orgstructure.number = '1202' OR entryentity.orgstructure.number = '1201')]"

	change, important := diffFilter(old, cur)
	if change == "" {
		t.Fatal("过滤条件变了却没报出来 —— 这正是 2026-09-18 漏掉的那次变更")
	}
	if !important {
		t.Error("查询口径变更必须人工确认")
	}
	if !strings.Contains(change, "1202") || !strings.Contains(change, "上次") {
		t.Errorf("变更报告应同时给出上次和本次的过滤条件，实际:\n%s", change)
	}
}

// 资产卡接口也在同一天被加了过滤（(org.number='1202' OR '1201') AND billstatus='C'），
// 只是因为恰好和业务可见范围一致而没被发现。基线里原本没有这个字段（空串），
// 所以「从无到有」这条路径必须单独覆盖。
func TestDiffFilterReportsFirstTimeFilter(t *testing.T) {
	change, important := diffFilter("", "[(org.number = '1202' OR org.number = '1201') AND billstatus = 'C']")
	if change == "" || !important {
		t.Fatalf("从无到有的过滤条件必须报警，change=%q important=%v", change, important)
	}
	if !strings.Contains(change, "过滤条件新增") {
		t.Errorf("应标记为「过滤条件新增」，实际:\n%s", change)
	}
}

// 过滤条件被移除：口径放宽，行数会变多，落库量要重新评估——同样是重要变化。
func TestDiffFilterReportsRemoval(t *testing.T) {
	change, important := diffFilter("[null]", "")
	if change == "" || !important {
		t.Fatalf("过滤条件移除必须报警，change=%q important=%v", change, important)
	}
}

// 没变就一个字都不要说：每次扫描都喊一次，告警就废了。
func TestDiffFilterSilentWhenUnchanged(t *testing.T) {
	same := "[(org.number = '1202')]"
	if change, important := diffFilter(same, same); change != "" || important {
		t.Fatalf("过滤条件未变时不该有任何输出，change=%q important=%v", change, important)
	}
	if change, important := diffFilter("", ""); change != "" || important {
		t.Fatalf("两边都没有过滤条件时不该有输出，change=%q important=%v", change, important)
	}
}

// 最关键的一条：0 行不能报成「字段全部消失」。
//
// 如果照常走字段 diff，5038 行的基线对上 0 行的现状，会打印出 27 条
// 「✗ 消失」——那是在说"对方删了投影列"，而真实原因是接口没返回数据。
// 报告方向错了，比不报更糟：人会顺着错的方向查很久。
func TestReportDiffZeroRowsDoesNotClaimFieldsVanished(t *testing.T) {
	old := &fingerprint{
		ScannedAt:   "2026-09-18 11:54:00",
		Filter:      "[null]",
		Rows:        5038,
		UniqueCodes: 5036,
		Fields: map[string]fieldFP{
			"number": {Kind: "scalar", Present: 5038, NonZero: 5036},
			"name":   {Kind: "scalar", Present: 5038, NonZero: 5038},
		},
		Nested: map[string]map[string]fieldFP{
			"entryentity": {"position": {Kind: "scalar", Present: 5038, NonZero: 4900}},
		},
	}
	cur := &fingerprint{
		ScannedAt: "2026-09-18 12:03:00",
		Filter:    "[(entryentity.orgstructure.number = '1202' OR entryentity.orgstructure.number = '1201')]",
		Rows:      0,
		Fields:    map[string]fieldFP{},
		Nested:    map[string]map[string]fieldFP{},
	}

	var important bool
	out := captureStdout(t, func() { important = reportDiff(old, cur, false) })

	if strings.Contains(out, "✗ 消失") {
		t.Errorf("0 行不该报「字段消失」——那会把人引向投影问题，真实原因是查询口径：\n%s", out)
	}
	if !strings.Contains(out, "0 行") {
		t.Errorf("必须明确说出本次 0 行：\n%s", out)
	}
	if !strings.Contains(out, "entryentity.orgstructure") {
		t.Errorf("必须把服务端生效的过滤条件打出来，否则无从判断原因：\n%s", out)
	}
	if !important {
		t.Error("5038 行变 0 行是必须人工确认的变化")
	}
}

// 过滤条件变了、但字段一个没动时，important 不能被吞掉。
//
// 这里曾经有个真实的 bug：reportDiff 在 blocks 为空时直接 return false，
// 于是"只有过滤条件变了"这种最隐蔽的情况反而一声不吭。
func TestReportDiffFilterOnlyChangeStillImportant(t *testing.T) {
	fields := map[string]fieldFP{"number": {Kind: "scalar", Present: 227, NonZero: 227}}
	nested := map[string]map[string]fieldFP{"finentry": {"a": {Kind: "scalar", Present: 200, NonZero: 200}}}

	old := &fingerprint{ScannedAt: "t1", Filter: "[null]", Rows: 227, UniqueCodes: 200,
		Fields: fields, Nested: nested}
	cur := &fingerprint{ScannedAt: "t2", Filter: "[(org.number = '1202')]", Rows: 227, UniqueCodes: 200,
		Fields: fields, Nested: nested}

	var important bool
	out := captureStdout(t, func() { important = reportDiff(old, cur, false) })

	if !important {
		t.Fatal("过滤条件变了就必须返回 important=true，哪怕字段完全没变")
	}
	if !strings.Contains(out, "过滤条件") {
		t.Errorf("报告里要出现过滤条件变更：\n%s", out)
	}
	if strings.Contains(out, "无变化") {
		t.Errorf("过滤条件变了就不能说「无变化」：\n%s", out)
	}
}

// 什么都没变时，输出里不该出现任何 ⚠，也不该被判为重要变化。
func TestReportDiffQuietWhenNothingChanged(t *testing.T) {
	fp := &fingerprint{
		ScannedAt: "t1", Filter: "[(org.number = '1202')]", Rows: 227, UniqueCodes: 200,
		Fields: map[string]fieldFP{"number": {Kind: "scalar", Present: 227, NonZero: 227}},
		Nested: map[string]map[string]fieldFP{},
	}
	clone := &fingerprint{
		ScannedAt: "t2", Filter: fp.Filter, Rows: fp.Rows, UniqueCodes: fp.UniqueCodes,
		Fields: fp.Fields, Nested: fp.Nested,
	}

	var important bool
	out := captureStdout(t, func() { important = reportDiff(fp, clone, false) })

	if important {
		t.Errorf("无变化时不该报重要变化:\n%s", out)
	}
	if !strings.Contains(out, "无变化") {
		t.Errorf("应明确说无变化:\n%s", out)
	}
	if strings.Contains(out, "⚠") {
		t.Errorf("无变化时不该出现警告符号:\n%s", out)
	}
}

// -target 展开与基线文件名必须稳定：基线文件路径一旦变了，
// 下次扫描就会「首次建基线」，把比对基准悄悄重置掉。
func TestResolveSources(t *testing.T) {
	cfg := &config.Config{}
	cfg.Kingdee.QueryPath = "/v2/gcgs/fa/fa_asset_card/Select_AssetCard"
	cfg.Kingdee.PersonnelQueryPath = "/v2/gcgs/base/bos_user/query-personnel"
	cfg.Kingdee.DepartmentQueryPath = "/v2/gcgs/base/bos_adminorg/query-department"

	t.Run("默认只扫资产卡且基线文件名不变", func(t *testing.T) {
		srcs, err := resolveSources(cfg, "asset", "docs", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(srcs) != 1 {
			t.Fatalf("期望 1 个目标，实际 %d", len(srcs))
		}
		if srcs[0].baseline != "docs/kingdee-fields-baseline.json" {
			t.Errorf("资产卡基线路径变了，会让历史基线失效: %s", srcs[0].baseline)
		}
	})

	t.Run("人员", func(t *testing.T) {
		srcs, err := resolveSources(cfg, "personnel", "docs", "")
		if err != nil {
			t.Fatal(err)
		}
		if srcs[0].path != "/v2/gcgs/base/bos_user/query-personnel" {
			t.Errorf("人员接口路径不对: %s", srcs[0].path)
		}
		if srcs[0].baseline != "docs/kingdee-personnel-baseline.json" {
			t.Errorf("人员基线路径不对: %s", srcs[0].baseline)
		}
		// 人员接口只要求 data 存在，里面传什么都不生效
		if len(srcs[0].dataBody) != 0 {
			t.Errorf("人员接口的 data 应为空对象，实际 %v", srcs[0].dataBody)
		}
	})

	t.Run("all", func(t *testing.T) {
		srcs, err := resolveSources(cfg, "all", "docs", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(srcs) != 3 {
			t.Fatalf("期望 3 个目标，实际 %d", len(srcs))
		}
	})

	t.Run("部门", func(t *testing.T) {
		srcs, err := resolveSources(cfg, "dept", "docs", "")
		if err != nil {
			t.Fatal(err)
		}
		if srcs[0].path != "/v2/gcgs/base/bos_adminorg/query-department" {
			t.Errorf("部门接口路径不对: %s", srcs[0].path)
		}
		if srcs[0].baseline != "docs/kingdee-dept-baseline.json" {
			t.Errorf("部门基线路径不对: %s", srcs[0].baseline)
		}
	})

	// -target=all 时 -baseline 没有意义（两个目标共用一个文件会把彼此的基线覆盖掉），
	// 必须报错而不是静默忽略。
	t.Run("all 配 -baseline 应报错", func(t *testing.T) {
		if _, err := resolveSources(cfg, "all", "docs", "x.json"); err == nil {
			t.Fatal("应当报错")
		}
	})

	t.Run("未知 target", func(t *testing.T) {
		if _, err := resolveSources(cfg, "nope", "docs", ""); err == nil {
			t.Fatal("应当报错")
		}
	})
}
