package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"asset-mgr/config"
	"asset-mgr/model"
)

// TestColumnValueAlignment 守住「列名列表」与「值列表」一一对应。
//
// 这两组是分开维护的：加了一列却忘了加值，拼出来的 INSERT 列数与值数对不上。
// 而 INSERT 只在「同步到一个本地还没有的资产编码」时才走，
// 日常增量同步根本跑不到，能在仓库里躺很久不暴露。
func TestColumnValueAlignment(t *testing.T) {
	c := &model.AssetCard{}

	if got, want := len(ownedCardValues(c)), len(kingdeeOwnedColumns); got != want {
		t.Fatalf("kingdeeOwnedColumns 有 %d 列，ownedCardValues 却给了 %d 个值", want, got)
	}
	if got, want := len(cardInitValues(c)), len(cardInitColumns); got != want {
		t.Fatalf("cardInitColumns 有 %d 列，cardInitValues 却给了 %d 个值", want, got)
	}

	// 两组列不能重叠：INSERT 时会被拼进同一个列清单，重复列名直接报 SQL 错
	owned := make(map[string]bool, len(kingdeeOwnedColumns))
	for _, col := range kingdeeOwnedColumns {
		owned[col] = true
	}
	for _, col := range cardInitColumns {
		if owned[col] {
			t.Fatalf("列 %s 同时出现在 kingdeeOwnedColumns 与 cardInitColumns，INSERT 会重复", col)
		}
	}
}

// TestMergeOwnedFieldsSkipsFinanceWithoutFinEntry 守住「星瀚没给明细就一个财务字段都不碰」。
//
// 这是同编码重复行踩出来的坑：227 行只对应 200 个资产编码，多出来的那 27 行
// finentry 是 null。syncer 会给这种行预填**资产类别的默认**使用期限 / 残值率
// （如房屋类 240 期、5%），于是它们带着非零的假值通过「大于 0 才覆盖」，
// 把前一行刚从星瀚同步下来的真值（如 107 期、3%）又盖回去。
func TestMergeOwnedFieldsSkipsFinanceWithoutFinEntry(t *testing.T) {
	dst := &model.AssetCard{
		FinOriginalValue:    359265.08,
		FinAccumDepreciaton: 44908.14,
		FinNetValue:         314356.94,
		FinUseMonths:        107,
		FinResidualRate:     3,
	}
	// 明细为 null 的重复行：金额解析成 0，使用期限/残值率被类别默认值填成了 240 / 5
	src := &model.AssetCard{
		FinEntryPresent:  false,
		FinUseMonths:     240,
		FinResidualRate:  5,
		FinOriginalValue: 0,
	}

	mergeOwnedFields(dst, src)

	if dst.FinUseMonths != 107 {
		t.Errorf("使用期限被重复行覆盖：期望 107，得到 %d", dst.FinUseMonths)
	}
	if dst.FinResidualRate != 3 {
		t.Errorf("残值率被重复行覆盖：期望 3，得到 %v", dst.FinResidualRate)
	}
	if dst.FinOriginalValue != 359265.08 {
		t.Errorf("原值被重复行覆盖：期望 359265.08，得到 %v", dst.FinOriginalValue)
	}
	if dst.FinAccumDepreciaton != 44908.14 || dst.FinNetValue != 314356.94 {
		t.Errorf("累计折旧 / 净值被重复行覆盖：%v / %v", dst.FinAccumDepreciaton, dst.FinNetValue)
	}
}

// TestMergeOwnedFieldsTakesFinanceFromFinEntry 守住反向：明细带了就以星瀚为准。
func TestMergeOwnedFieldsTakesFinanceFromFinEntry(t *testing.T) {
	dst := &model.AssetCard{
		FinOriginalValue: 100, // 人工填的旧值
		FinUseMonths:     240, // 类别默认值
		FinResidualRate:  5,
	}
	src := &model.AssetCard{
		FinEntryPresent:     true,
		FinOriginalValue:    359265.08,
		FinAccumDepreciaton: 44908.14,
		FinNetValue:         314356.94,
		FinUseMonths:        24,
		FinResidualRate:     3,
	}

	mergeOwnedFields(dst, src)

	if dst.FinOriginalValue != 359265.08 || dst.FinAccumDepreciaton != 44908.14 || dst.FinNetValue != 314356.94 {
		t.Errorf("金额未按星瀚覆盖：%v / %v / %v",
			dst.FinOriginalValue, dst.FinAccumDepreciaton, dst.FinNetValue)
	}
	if dst.FinUseMonths != 24 {
		t.Errorf("使用期限未按星瀚覆盖：期望 24，得到 %d", dst.FinUseMonths)
	}
	if dst.FinResidualRate != 3 {
		t.Errorf("残值率未按星瀚覆盖：期望 3，得到 %v", dst.FinResidualRate)
	}
}

// TestMergeOwnedFieldsTaxOnlyWhenPresent 守住含税金额 / 税额的「部分覆盖」语义。
//
// 星瀚只对 22/200 张卡提供税额，其余 178 张是空（不是 0）。
// 那 178 张的含税金额必须保持人工维护——不能因为税额是 0 就把
// 「含税金额 = 原值」写进去，那 178 张的原值未必是含税口径。
func TestMergeOwnedFieldsTaxOnlyWhenPresent(t *testing.T) {
	t.Run("星瀚给了税额", func(t *testing.T) {
		dst := &model.AssetCard{}
		src := &model.AssetCard{
			FinEntryPresent:  true,
			FinOriginalValue: 5574.34,
			FinTax:           724.66,
			FinAmountWithTax: 6299.00, // 原值 + 税额
		}
		mergeOwnedFields(dst, src)
		if dst.FinTax != 724.66 {
			t.Errorf("税额未落库：期望 724.66，得到 %v", dst.FinTax)
		}
		if dst.FinAmountWithTax != 6299.00 {
			t.Errorf("含税金额未落库：期望 6299.00，得到 %v", dst.FinAmountWithTax)
		}
	})

	t.Run("星瀚没给税额", func(t *testing.T) {
		dst := &model.AssetCard{FinAmountWithTax: 8888.88, FinTax: 999.99} // 人工填的
		src := &model.AssetCard{
			FinEntryPresent:  true,
			FinOriginalValue: 359265.08,
			FinTax:           0,
			FinAmountWithTax: 0,
		}
		mergeOwnedFields(dst, src)
		if dst.FinTax != 999.99 {
			t.Errorf("税额被抹掉了：期望保留 999.99，得到 %v", dst.FinTax)
		}
		if dst.FinAmountWithTax != 8888.88 {
			t.Errorf("含税金额被抹掉了：期望保留 8888.88，得到 %v", dst.FinAmountWithTax)
		}
		if dst.FinOriginalValue != 359265.08 {
			t.Errorf("原值该照常覆盖：期望 359265.08，得到 %v", dst.FinOriginalValue)
		}
	})
}

// dupRowCard 构造一张「同编码重复行」里的卡。
//
// 真实数据里同编码的两行差异只在 id / finentry / bizstatus 上，其余字段完全一致。
// 所以这里让两行由同一个构造函数产生，只在测试里手动改财务部分——
// 否则 diffCard 会因为测试数据本身造得不真实而报出一堆无关差异。
func dupRowCard() *model.AssetCard {
	return &model.AssetCard{
		AssetCode:  "12020302000003",
		Name:       "乐荟科创中心3栋3层C2户",
		CategoryID: 2177723475102284800,
		Unit:       "平方米",
		Quantity:   194.52,
		UseStatus:  "正常使用",
		Status:     "在用",
		Location:   "深圳办事处",
		Source:     "kingdee",
	}
}

// TestReplayOverlayDoesNotDoubleCountDuplicateRows 守住 dry-run 计数不再高估。
//
// dry-run 不写库，于是同一编码的两行会各自与**同一份**旧数据比对，
// 每比一次都算一次 affected —— 实测 2 倍高估（预测 updated=30 / 实际 15）。
// overlay 让第二行跟第一行的模拟结果比，没有新信息就不该再计一次。
func TestReplayOverlayDoesNotDoubleCountDuplicateRows(t *testing.T) {
	code := "12020302000003"
	overlay := map[string]*model.AssetCard{}

	// 第一行：带财务明细（真实数据里 id 较小、finentry 有值的那份）
	first := dupRowCard()
	first.FinEntryPresent = true
	first.FinOriginalValue = 359265.08
	first.FinNetValue = 314356.94
	overlay[code] = first

	// 第二行：同编码，finentry 为 null（bizstatus=ADD 的那份）
	second := dupRowCard()

	if _, changed := replayOverlay(overlay, second); changed {
		t.Error("第二行没带来新信息，不该再算一次 updated —— 这正是 dry-run 高估的根源")
	}
	if got := overlay[code].FinOriginalValue; got != 359265.08 {
		t.Errorf("第二行抹掉了已同步的财务信息：期望 359265.08，得到 %v", got)
	}

	// 反过来：第二行确实带新信息时，必须照常计一次
	third := dupRowCard()
	third.Name = "乐荟科创中心3栋3层C2户（改名）"
	if _, changed := replayOverlay(overlay, third); !changed {
		t.Error("第二行有真实差异时应计 updated，不能因为去重就漏报")
	}
}

// TestReplayOverlayIsolatesByAssetCode 守住 overlay 按编码隔离。
//
// 如果键用错了（比如用 id 或全局单值），不同资产之间会串味，
// 表现是"某些卡明明变了却报 unchanged"——比高估更危险，因为它会漏报。
func TestReplayOverlayIsolatesByAssetCode(t *testing.T) {
	overlay := map[string]*model.AssetCard{}

	a := dupRowCard()
	a.AssetCode = "CODE-A"
	a.FinEntryPresent = true
	a.FinOriginalValue = 100
	overlay["CODE-A"] = a

	b := dupRowCard()
	b.AssetCode = "CODE-B"
	b.FinEntryPresent = true
	b.FinOriginalValue = 200

	// CODE-B 首次模拟：不能读到 CODE-A 的状态
	replayOverlay(overlay, b)

	if got := overlay["CODE-A"].FinOriginalValue; got != 100 {
		t.Errorf("CODE-A 被 CODE-B 串味：期望 100，得到 %v", got)
	}
	if got := overlay["CODE-B"].FinOriginalValue; got != 200 {
		t.Errorf("CODE-B 状态不对：期望 200，得到 %v", got)
	}
}

// TestReplayOverlayHandlesUnseenCode 守住首次模拟不 panic。
//
// overlay 里没有该编码时 sim 是 nil，直接 *sim 会解引用空指针。
// 调用方目前总在 ok 判断后才进，但把函数抽出来单独用时就未必了。
func TestReplayOverlayHandlesUnseenCode(t *testing.T) {
	overlay := map[string]*model.AssetCard{}

	c := dupRowCard()
	merged, changed := replayOverlay(overlay, c)

	if merged == nil {
		t.Fatal("首次模拟不该返回 nil")
	}
	if !changed {
		t.Error("首次模拟相对空卡必然有内容，changed 应为 true")
	}
	if got := overlay[c.AssetCode]; got == nil || got.FinEntryPresent != c.FinEntryPresent {
		t.Error("首次模拟后该编码应已写入 overlay")
	}
}

// TestRememberToleratesNilOverlay 守住 overlay 为 nil 时退化为旧行为而不是 panic。
//
// 非 dry-run 路径传的就是 nil（那边根本不需要模拟层），写 nil map 会直接 panic。
func TestRememberToleratesNilOverlay(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("overlay 为 nil 时不该 panic，实际: %v", r)
		}
	}()

	remember(nil, "CODE-X", &model.AssetCard{AssetCode: "CODE-X"})

	// nil overlay 读也必须是安全的（replayOverlay 里会读）
	if _, changed := replayOverlay(nil, dupRowCard()); !changed {
		t.Error("nil overlay 下应退化为首次模拟")
	}
}

// testDSN 取集成测试用的 DSN：优先环境变量，其次项目根的 config.yaml。
// 两者都拿不到就 Skip —— 默认不依赖外部数据库，避免 CI 上变红。
func testDSN(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("ASSET_MGR_TEST_DSN"); dsn != "" {
		return dsn
	}
	cfg, err := config.Load(filepath.Join("..", "config.yaml"))
	if err != nil {
		t.Skipf("未设置 ASSET_MGR_TEST_DSN，且读不到 ../config.yaml，跳过需要数据库的测试: %v", err)
	}
	return cfg.DSN()
}

// TestDryRunOverlayAgainstRealDB 用真实数据库验证 dry-run 的计数不再高估。
//
// 为什么必须连库：ApplyOwnedCardTx 的入参是 *sql.Tx，"同编码多行只计一次"
// 这条规则横跨「读旧值 → 比对 → 记 overlay」三步，纯单测只能覆盖最后一步
// （见 TestReplayOverlayDoesNotDoubleCountDuplicateRows）。
//
// 安全性：dry-run 分支在写库之前就 return，事务里不会产生任何写；
// 末尾 Rollback 只是兜底。所以这个测试对库是**只读**的，可以对着开发库跑。
func TestDryRunOverlayAgainstRealDB(t *testing.T) {
	dsn := testDSN(t)
	st, err := New(dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	// 用一个真实存在的编码：同编码重复行正是高估的来源
	const code = "12020302000003"
	var n int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM asset_card WHERE asset_code = ?", code).Scan(&n); err != nil {
		t.Fatalf("查询 asset_card 失败: %v", err)
	}
	if n == 0 {
		t.Skipf("库里没有 %s，跳过", code)
	}

	tx, err := st.db.Begin()
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 造一张与库里必然有差异的卡：名称对不上。
	// dry-run 不落库，所以这个假名称不会污染数据。
	probe := &model.AssetCard{AssetCode: code, Name: "dry-run-overlay-probe"}

	overlay := map[string]*model.AssetCard{}

	_, first, err := ApplyOwnedCardTx(tx, probe, "test", true, overlay)
	if err != nil {
		t.Fatalf("首次调用失败: %v", err)
	}
	if first != CardActionUpdated {
		t.Fatalf("与库不一致的卡首次应判 updated，实际 %q", first)
	}

	_, second, err := ApplyOwnedCardTx(tx, probe, "test", true, overlay)
	if err != nil {
		t.Fatalf("第二次调用失败: %v", err)
	}
	if second != CardActionUnchanged {
		t.Errorf("同编码第二行没带来新信息，应判 unchanged（这正是高估的根源），实际 %q", second)
	}

	// 对照：不传 overlay 时退化成旧行为，第二次仍判 updated。
	// 这条断言的意义是证明"修复确实来自 overlay"，而不是别的地方碰巧变了。
	_, legacy, err := ApplyOwnedCardTx(tx, probe, "test", true, nil)
	if err != nil {
		t.Fatalf("nil overlay 调用失败: %v", err)
	}
	if legacy != CardActionUpdated {
		t.Errorf("nil overlay 应退化为旧行为（重复计数），实际 %q", legacy)
	}

	// 收尾：显式回滚后再查一次，确认这个测试确实没留下任何写入。
	// 这条断言是给未来的人用的：如果哪天 dry-run 分支被改成"顺手写一下"，
	// 它会立刻炸，而不是悄悄把探针数据留在库里。
	if err := tx.Rollback(); err != nil && err != sql.ErrTxDone {
		t.Fatalf("回滚失败: %v", err)
	}
	var name string
	if err := st.db.QueryRow("SELECT name FROM asset_card WHERE asset_code = ?", code).Scan(&name); err != nil {
		t.Fatalf("复查失败: %v", err)
	}
	if name == "dry-run-overlay-probe" {
		t.Fatal("库被写脏了：探针名称落库了，dry-run 不该产生任何写入")
	}
}
