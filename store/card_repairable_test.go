package store

import (
	"fmt"
	"testing"
	"time"

	"asset-mgr/model"
)

// TestCardCategoryRepairableDerivation 钉死需求 A 的派生字段链路：
// cardSelect 的 COALESCE(cat.repairable, 0) → scanCard → AssetCard.CategoryRepairable。
//
// 为什么必须连真库：这是横跨 SQL 与 Go 扫描的一列，单测回答不了——
//   ① 可维修分类 → true；
//   ② 不可维修分类 → false；
//   ③ category_id=0（「未设置」哨兵值）→ LEFT JOIN 未匹配、cat.repairable 为 SQL NULL，
//      必须被 COALESCE 兜住并扫成 false，**绝不能报错**（否则整张列表接口 500）。
// 第 ③ 条正是架构文档 §1.3 强调的「防御性必要」点，本测试把它变成可执行验收。
//
// 自清理：探针卡编码以 ZZ-REPAIR-CAT- 前缀，跑完连同履历一并删除，库里不留痕。
func TestCardCategoryRepairableDerivation(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	// 取一个可维修分类与一个不可维修分类（需求 A 的前置数据）。
	var repairableID, nonRepairableID int64
	if err := st.db.QueryRow("SELECT id FROM asset_category WHERE repairable = 1 ORDER BY id LIMIT 1").Scan(&repairableID); err != nil {
		t.Skipf("库里没有可维修分类，跳过（需求 A 前置未就绪）: %v", err)
	}
	if err := st.db.QueryRow("SELECT id FROM asset_category WHERE repairable = 0 ORDER BY id LIMIT 1").Scan(&nonRepairableID); err != nil {
		t.Skipf("库里没有不可维修分类，跳过: %v", err)
	}

	ns := time.Now().UnixNano()
	mkCode := func(tag string) string { return fmt.Sprintf("ZZ-REPAIR-CAT-%s-%d", tag, ns) }
	codeRep, codeNon, codeSentinel := mkCode("REP"), mkCode("NON"), mkCode("ZERO")

	cleanup := func(code string) {
		var cardID int64
		_ = st.db.QueryRow("SELECT id FROM asset_card WHERE asset_code = ?", code).Scan(&cardID)
		if cardID == 0 {
			return
		}
		_, _ = st.db.Exec("DELETE FROM asset_history WHERE card_id = ?", cardID)
		_, _ = st.db.Exec("DELETE FROM asset_card WHERE id = ?", cardID)
	}
	for _, c := range []string{codeRep, codeNon, codeSentinel} {
		cleanup(c)
		defer cleanup(c)
	}

	insert := func(code string, categoryID int64) {
		t.Helper()
		if _, err := st.db.Exec(`INSERT INTO asset_card (asset_code, name, status, biz_status, category_id, created_by)
			VALUES (?, ?, ?, '', ?, 'test')`, code, "可维修派生字段探针", model.StatusInUse, categoryID); err != nil {
			t.Fatalf("建探针卡 %s 失败: %v", code, err)
		}
	}
	insert(codeRep, repairableID)
	insert(codeNon, nonRepairableID)
	insert(codeSentinel, 0) // 哨兵值：LEFT JOIN 未匹配，cat.repairable 为 NULL

	// ① 可维修分类 → true
	got, err := st.GetCardByCode(codeRep)
	if err != nil || got == nil {
		t.Fatalf("读回可维修分类探针卡失败: %v", err)
	}
	if !got.CategoryRepairable {
		t.Errorf("可维修分类的资产 CategoryRepairable 应为 true，实际 false（category_id=%d）", repairableID)
	}

	// ② 不可维修分类 → false
	got, err = st.GetCardByCode(codeNon)
	if err != nil || got == nil {
		t.Fatalf("读回不可维修分类探针卡失败: %v", err)
	}
	if got.CategoryRepairable {
		t.Errorf("不可维修分类的资产 CategoryRepairable 应为 false，实际 true（category_id=%d）", nonRepairableID)
	}

	// ③ 哨兵值 category_id=0 → 不得报错，且为 false（COALESCE 防御）
	got, err = st.GetCardByCode(codeSentinel)
	if err != nil {
		t.Fatalf("category_id=0 的资产读回报错（COALESCE 未生效？）: %v", err)
	}
	if got == nil {
		t.Fatalf("category_id=0 的探针卡应能读回")
	}
	if got.CategoryRepairable {
		t.Errorf("category_id=0（未设置）应判为不可维修（false），实际 true")
	}

	// ④ 列表接口同源：三张探针卡都走 cardSelect，均不得报错且标志正确
	lr, err := st.ListCards(model.ListQuery{Keyword: "ZZ-REPAIR-CAT-", Page: 1, PageSize: 50}, model.AssetScope{})
	if err != nil {
		t.Fatalf("列表查询报错（cardSelect 增列后列数不匹配？）: %v", err)
	}
	want := map[string]bool{codeRep: true, codeNon: false, codeSentinel: false}
	for _, it := range lr.Items {
		if exp, ok := want[it.AssetCode]; ok {
			if it.CategoryRepairable != exp {
				t.Errorf("列表里 %s 的 CategoryRepairable 应为 %v，实际 %v", it.AssetCode, exp, it.CategoryRepairable)
			}
			delete(want, it.AssetCode)
		}
	}
	if len(want) != 0 {
		t.Errorf("列表应能查到全部探针卡，缺失: %v", want)
	}
}
