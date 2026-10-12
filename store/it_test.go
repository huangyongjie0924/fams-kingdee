package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"asset-mgr/model"
)

// —— IT 信息设备台账的数据层测试（2026-10-10 需求）——

// TestITValueExprCardIDIsNull 钉死「Go 侧 0 = 未关联 / 库里 NULL = 未关联」的双向约定。
//
// ⚠️ 这是整个表设计的承重墙：uk_it_card 唯一索引要同时满足
//   ① 一张资产卡最多一条 IT 记录；
//   ② 任意多条「资产卡上没有的设备」共存。
// 靠的就是 MySQL 唯一索引**对 NULL 不去重、对 0 去重**这一语义。
// 一旦 card_id 被写成字面量 0，第二台无卡设备就会撞唯一键——而错误信息是
// 「Duplicate entry '0' for key uk_it_card」，与「这张卡已登记过」完全不同，很难联想到根因。
func TestITValueExprCardIDIsNull(t *testing.T) {
	if got := itValueExpr("card_id"); got != "NULLIF(?, 0)" {
		t.Fatalf("card_id 的写入表达式必须是 NULLIF(?, 0)，实际 %q", got)
	}
	// 其余列一律普通占位符——防止有人图省事把所有列都套上 NULLIF。
	for _, col := range itWriteColumns {
		if col == "card_id" {
			continue
		}
		if got := itValueExpr(col); got != "?" {
			t.Errorf("列 %q 的写入表达式应为 ?，实际 %q", col, got)
		}
	}
	// 读侧必须有对应的 COALESCE，否则 NULL 会扫进 int64 直接报错。
	if !strings.Contains(itSelect, "COALESCE(c.card_id, 0)") {
		t.Fatalf("itSelect 必须用 COALESCE(c.card_id, 0) 读 card_id，否则未关联的行会让 Scan 失败")
	}
}

// TestITScopeCondsUnion 范围条件必须取**并集**，且参数顺序与条件顺序一致。
func TestITScopeCondsUnion(t *testing.T) {
	// 零值 → 无任何条件。
	conds, args := itScopeConds(model.ITScope{})
	if len(conds) != 0 || len(args) != 0 {
		t.Fatalf("零值 scope 不应产出任何条件，实际 conds=%v args=%v", conds, args)
	}

	// 单维度。
	conds, args = itScopeConds(model.ITScope{SelfEmpID: 6001})
	if len(conds) != 1 || conds[0] != "c.user_emp_id = ?" {
		t.Fatalf("self 维度应产出单条条件，实际 %v", conds)
	}
	if len(args) != 1 || args[0] != int64(6001) {
		t.Fatalf("self 维度参数应为 6001，实际 %v", args)
	}

	// 两维同时非零（多角色）→ 必须 OR 而不是两条 AND。
	// ⚠️ 写成两条独立条件会被调用方用 AND 拼起来变成**交集**，
	// 部门负责人兼员工会几乎看不到数据。这个坑 AssetScope 已经踩过一次。
	conds, args = itScopeConds(model.ITScope{SelfEmpID: 6001, DeptIDs: []int64{1201, 1202}})
	if len(conds) != 1 {
		t.Fatalf("两维应合并为一条 (A OR B) 条件，实际 %v", conds)
	}
	if !strings.Contains(conds[0], " OR ") {
		t.Fatalf("两维条件必须用 OR 连接，实际 %q", conds[0])
	}
	// 参数顺序：self 先、dept 后（与 parts 的追加顺序一致，错了会错位绑定）。
	want := []any{int64(6001), int64(1201), int64(1202)}
	if len(args) != len(want) {
		t.Fatalf("参数个数应为 %d，实际 %v", len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("参数顺序错位：期望 %v，实际 %v", want, args)
		}
	}
}

// TestITBuildWhereOptionalFilters 三个可选筛选项的「不筛选」语义必须与「筛 0/false」区分开。
//
// 这是本仓反复踩的一类坑：bool 的零值 false 无法区分「不筛选」与「只要非信创」，
// 故 ListQuery.Repairable 用了指针，ITListQuery 的 IsXinchuang / Linked 同理。
func TestITBuildWhereOptionalFilters(t *testing.T) {
	// nil = 不筛选：WHERE 里不能出现这两列的任何条件。
	where, _ := itBuildWhere(model.ITListQuery{}, model.ITScope{})
	if strings.Contains(where, "is_xinchuang") {
		t.Errorf("IsXinchuang 为 nil 时不应产出条件，实际 %q", where)
	}
	if strings.Contains(where, "card_id IS") {
		t.Errorf("Linked 为 nil 时不应产出条件，实际 %q", where)
	}

	// 指向 false = 「只要非信创」，必须真的产出条件。
	f := false
	where, args := itBuildWhere(model.ITListQuery{IsXinchuang: &f}, model.ITScope{})
	if !strings.Contains(where, "c.is_xinchuang = ?") {
		t.Errorf("IsXinchuang=&false 应产出条件，实际 %q", where)
	}
	if len(args) != 1 || args[0] != false {
		t.Errorf("IsXinchuang=&false 的参数应为 false，实际 %v", args)
	}

	// Linked=&false = 「只要资产卡上没有的设备」——用户明确点出的核心场景，必须能一键筛出。
	where, args = itBuildWhere(model.ITListQuery{Linked: &f}, model.ITScope{})
	if !strings.Contains(where, "c.card_id IS NULL") {
		t.Errorf("Linked=&false 应产出 card_id IS NULL，实际 %q", where)
	}
	if len(args) != 0 {
		t.Errorf("Linked 条件不带参数，实际 %v", args)
	}

	// Linked=&true = 只要已关联的。
	tr := true
	where, _ = itBuildWhere(model.ITListQuery{Linked: &tr}, model.ITScope{})
	if !strings.Contains(where, "c.card_id IS NOT NULL") {
		t.Errorf("Linked=&true 应产出 card_id IS NOT NULL，实际 %q", where)
	}
}

// TestITBuildWhereCardIDFilter 按关联卡精确筛选：0 = 不筛选，>0 = 等值。
//
// ⚠️ 这里必须用**原始列** c.card_id 而不是 COALESCE(card_id,0)：
// 写成 COALESCE(...) = 0 会把「不筛选」变成「只要未关联的」，语义完全反了。
func TestITBuildWhereCardIDFilter(t *testing.T) {
	where, args := itBuildWhere(model.ITListQuery{CardID: 0}, model.ITScope{})
	if strings.Contains(where, "card_id") {
		t.Errorf("CardID=0 应表示不筛选，实际 %q", where)
	}

	where, args = itBuildWhere(model.ITListQuery{CardID: 159}, model.ITScope{})
	if !strings.Contains(where, "c.card_id = ?") {
		t.Errorf("CardID=159 应产出等值条件，实际 %q", where)
	}
	if strings.Contains(where, "COALESCE(c.card_id") {
		t.Errorf("CardID 筛选不能用 COALESCE（0 会被当成「只要未关联的」），实际 %q", where)
	}
	if len(args) != 1 || args[0] != int64(159) {
		t.Errorf("CardID 参数应为 159，实际 %v", args)
	}
}

// —— 以下为需要真库的用例（走 testDSN 的安全阀，库名不含 test 即 Skip）——

// itProbe 造一条 IT 记录，返回 id；t.Cleanup 自动删。
func itProbe(t *testing.T, st *Store, e *model.ITEquipment) int64 {
	t.Helper()
	id, err := st.CreateITEquipment(e, "qa")
	if err != nil {
		t.Fatalf("建 IT 探针记录失败: %v", err)
	}
	t.Cleanup(func() { st.DB().Exec("DELETE FROM it_equipment WHERE id=?", id) })
	return id
}

// TestITEquipmentCardlessMultipleCoexist 是本需求最关键的**真库**断言：
// 多条「资产卡上没有的设备」（card_id = 0 / 库里 NULL）必须能共存。
//
// 用户明确提出「实际管理中存在…资产卡上无的设备」，若这条不成立，
// 整个方案就不能落地（第二台无卡设备会插不进去）。
// 单测回答不了这个问题——它取决于 MySQL 对唯一索引中 NULL 的处理，必须连真库。
func TestITEquipmentCardlessMultipleCoexist(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ns := time.Now().UnixNano()
	var ids []int64
	for i := 0; i < 3; i++ {
		id := itProbe(t, st, &model.ITEquipment{
			CardID:     0, // 未关联 —— 关键
			AssetCode:  fmt.Sprintf("QA-IT-CARDLESS-%d-%d", ns, i),
			DeviceType: "网络设备",
			Status:     model.ITStatusIdle,
			BrandModel: fmt.Sprintf("QA 无卡设备 %d", i),
		})
		ids = append(ids, id)
	}

	// 三条都必须真的落库（不是「插入没报错但行被合并」）。
	for _, id := range ids {
		got, err := st.GetITEquipment(id)
		if err != nil {
			t.Fatalf("回读 id=%d 失败: %v", id, err)
		}
		if got == nil {
			t.Fatalf("id=%d 应存在（三条无卡设备必须都能落库）", id)
		}
		if got.CardID != 0 {
			t.Errorf("id=%d 的 CardID 应为 0（未关联），实际 %d", id, got.CardID)
		}
	}

	// 用 Linked=&false 能一次筛出「无卡设备」。
	f := false
	res, err := st.ListITEquipment(model.ITListQuery{Linked: &f, PageSize: 500}, model.ITScope{})
	if err != nil {
		t.Fatalf("按 Linked=false 查询失败: %v", err)
	}
	found := 0
	for _, e := range res.Items {
		if e.CardID == 0 {
			found++
		}
	}
	if found < 3 {
		t.Errorf("按「无资产卡设备」筛选应至少命中刚建的 3 条，实际 %d（共 %d 条）", found, res.Total)
	}
	// 反向：Linked=true 的结果里不能出现无卡记录。
	tr := true
	res2, err := st.ListITEquipment(model.ITListQuery{Linked: &tr, PageSize: 500}, model.ITScope{})
	if err != nil {
		t.Fatalf("按 Linked=true 查询失败: %v", err)
	}
	for _, e := range res2.Items {
		if e.CardID == 0 {
			t.Errorf("Linked=true 的结果里出现了无卡记录 id=%d", e.ID)
		}
	}
}

// TestITEquipmentOnePerCard 一张资产卡最多一条 IT 记录（uk_it_card）。
// 第二次关联同一张卡必须被数据库挡住——不能只靠应用层判重（并发下会漏）。
func TestITEquipmentOnePerCard(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ns := time.Now().UnixNano()
	// 造一张探针资产卡（IT 记录的关联目标）。
	res, err := st.DB().Exec(
		`INSERT INTO asset_card (asset_code, name, status, biz_status, category_id, created_by)
		 VALUES (?, ?, ?, '', 0, 'qa')`,
		fmt.Sprintf("QA-IT-CARD-%d", ns), "QA IT 关联探针卡", model.StatusInUse)
	if err != nil {
		t.Fatalf("建探针资产卡失败: %v", err)
	}
	cardID, _ := res.LastInsertId()
	t.Cleanup(func() { st.DB().Exec("DELETE FROM asset_card WHERE id=?", cardID) })

	itProbe(t, st, &model.ITEquipment{
		CardID: cardID, AssetCode: fmt.Sprintf("QA-IT-CARD-%d", ns),
		Status: model.ITStatusInUse, BrandModel: "第一条",
	})

	// 第二条 → 必须撞唯一键。断言错误里含 Duplicate（api 层的 isDuplicate 也认这个串）。
	_, err = st.CreateITEquipment(&model.ITEquipment{
		CardID: cardID, AssetCode: fmt.Sprintf("QA-IT-CARD-%d", ns),
		Status: model.ITStatusInUse, BrandModel: "第二条",
	}, "qa")
	if err == nil {
		t.Fatalf("同一张资产卡插入第二条 IT 记录应失败（uk_it_card），实际成功")
	}
	if !strings.Contains(err.Error(), "Duplicate entry") {
		t.Errorf("错误信息应含 Duplicate entry（api 层据此返回 409），实际: %v", err)
	}

	// 按卡查得到那一条。
	got, err := st.GetITEquipmentByCardID(cardID)
	if err != nil {
		t.Fatalf("按卡查询失败: %v", err)
	}
	if got == nil {
		t.Fatalf("按卡应能查到记录")
	}
	if got.BrandModel != "第一条" {
		t.Errorf("按卡查到的应是第一条，实际 %q", got.BrandModel)
	}
	// card_id <= 0 不是可查条件，直接返回 nil（不报错）。
	if g, err := st.GetITEquipmentByCardID(0); err != nil || g != nil {
		t.Errorf("GetITEquipmentByCardID(0) 应返回 (nil, nil)，实际 (%v, %v)", g, err)
	}
}

// TestITEquipmentSameEmployeeMultiple 员工一人多机：同一使用人的多条记录必须能共存。
func TestITEquipmentSameEmployeeMultiple(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ns := time.Now().UnixNano()
	const empID = int64(987654321) // 探针员工 id，不依赖 employee 表存在（无外键）

	for _, typ := range []string{"台式机", "笔记本", "平板"} {
		itProbe(t, st, &model.ITEquipment{
			CardID: 0, AssetCode: fmt.Sprintf("QA-IT-MULTI-%d-%s", ns, typ),
			DeviceType: typ, UserEmpID: empID, Status: model.ITStatusInUse,
		})
	}

	res, err := st.ListITEquipment(model.ITListQuery{UserEmpID: empID, PageSize: 100}, model.ITScope{})
	if err != nil {
		t.Fatalf("按使用人查询失败: %v", err)
	}
	if res.Total < 3 {
		t.Fatalf("同一使用人应有至少 3 条记录（一人多机），实际 %d", res.Total)
	}
}

// TestITEquipmentScopeNarrowedInSQL 可见范围必须进 WHERE（分页/总数才准），
// 而不是取回来再在 Go 里过滤。
func TestITEquipmentScopeNarrowedInSQL(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ns := time.Now().UnixNano()
	empID := int64(987654322)
	otherID := int64(987654323)
	itProbe(t, st, &model.ITEquipment{
		CardID: 0, AssetCode: fmt.Sprintf("QA-IT-SCOPE-MINE-%d", ns),
		UserEmpID: empID, Status: model.ITStatusInUse,
	})
	itProbe(t, st, &model.ITEquipment{
		CardID: 0, AssetCode: fmt.Sprintf("QA-IT-SCOPE-OTHER-%d", ns),
		UserEmpID: otherID, Status: model.ITStatusInUse,
	})

	sc := model.ITScope{SelfEmpID: empID}
	res, err := st.ListITEquipment(model.ITListQuery{PageSize: 500}, sc)
	if err != nil {
		t.Fatalf("带范围查询失败: %v", err)
	}
	if res.Total == 0 {
		t.Fatalf("应至少命中自己那条")
	}
	for _, e := range res.Items {
		if e.UserEmpID != empID {
			t.Errorf("受限范围的结果里出现了别人的记录 id=%d emp=%d", e.ID, e.UserEmpID)
		}
	}

	// 计数接口必须与列表同一口径。
	total, _, _, err := st.ITEquipmentCount(sc)
	if err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if total != res.Total {
		t.Errorf("ITEquipmentCount(%d) 与 ListITEquipment.Total(%d) 口径不一致", total, res.Total)
	}
}

// TestITEquipmentImportUpsert 导入的「新增 / 更新」分流与整批事务。
func TestITEquipmentImportUpsert(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ns := time.Now().UnixNano()
	code := fmt.Sprintf("QA-IT-IMPORT-%d", ns)

	// 第一次：新增。
	created, updated, err := st.ImportITEquipment([]model.ITImportItem{{
		ExistingID: 0,
		Equipment: model.ITEquipment{
			CardID: 0, AssetCode: code, DeviceType: "笔记本",
			Status: model.ITStatusInUse, MemoryGB: 16, BrandModel: "导入第一版",
		},
	}}, "qa")
	if err != nil {
		t.Fatalf("导入新增失败: %v", err)
	}
	if created != 1 || updated != 0 {
		t.Fatalf("首次导入应为 created=1 updated=0，实际 created=%d updated=%d", created, updated)
	}
	t.Cleanup(func() { st.DB().Exec("DELETE FROM it_equipment WHERE asset_code=?", code) })

	list, err := st.FindITEquipmentByAssetCode(code)
	if err != nil {
		t.Fatalf("按编码查询失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("按编码应查到 1 条，实际 %d", len(list))
	}
	firstID := list[0].ID

	// 第二次：按编码匹配到同一条 → 更新，不产生重复。
	created, updated, err = st.ImportITEquipment([]model.ITImportItem{{
		ExistingID: firstID,
		Equipment: model.ITEquipment{
			CardID: 0, AssetCode: code, DeviceType: "笔记本",
			Status: model.ITStatusInUse, MemoryGB: 32, BrandModel: "导入第二版",
		},
	}}, "qa")
	if err != nil {
		t.Fatalf("导入更新失败: %v", err)
	}
	if created != 0 || updated != 1 {
		t.Fatalf("二次导入应为 created=0 updated=1，实际 created=%d updated=%d", created, updated)
	}
	// 关键：仍然只有一条（反复导同一份文件不会重复）。
	again, err := st.FindITEquipmentByAssetCode(code)
	if err != nil {
		t.Fatalf("复查失败: %v", err)
	}
	if len(again) != 1 {
		t.Fatalf("更新后仍应只有 1 条，实际 %d —— 导入产生了重复", len(again))
	}
	if again[0].MemoryGB != 32 || again[0].BrandModel != "导入第二版" {
		t.Errorf("更新未生效：%+v", again[0])
	}

	// 空编码不作为匹配键（库里所有无卡设备的编码都是空串，拿它查会「匹配到几十条」）。
	empty, err := st.FindITEquipmentByAssetCode("")
	if err != nil {
		t.Fatalf("空编码查询应不报错: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("空编码不应作为匹配键，实际返回 %d 条", len(empty))
	}
}

// TestITEquipmentDelete 物理删除：删掉即不存在，重复删报 ErrNoRows。
func TestITEquipmentDelete(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ns := time.Now().UnixNano()
	id, err := st.CreateITEquipment(&model.ITEquipment{
		CardID: 0, AssetCode: fmt.Sprintf("QA-IT-DEL-%d", ns),
		Status: model.ITStatusInUse,
	}, "qa")
	if err != nil {
		t.Fatalf("建记录失败: %v", err)
	}

	if err := st.DeleteITEquipment(id); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	got, err := st.GetITEquipment(id)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got != nil {
		t.Errorf("删除后应查不到，实际 %+v", got)
	}
	// 再删一次 → ErrNoRows（api 层据此返回 404）。
	if err := st.DeleteITEquipment(id); err == nil {
		t.Errorf("重复删除应返回错误（sql.ErrNoRows），实际 nil")
	}
}

// TestITEquipmentUpdateNoOp 内容完全一致时 UpdateITEquipment 不应报「不存在」。
//
// RowsAffected 在「值没变」时是 0，与「行不存在」同形；
// 实现里用一次存在性查询区分，本测试把这个区分钉住。
func TestITEquipmentUpdateNoOp(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ns := time.Now().UnixNano()
	e := &model.ITEquipment{
		CardID: 0, AssetCode: fmt.Sprintf("QA-IT-NOOP-%d", ns),
		DeviceType: "台式机", Status: model.ITStatusInUse, MemoryGB: 8, BrandModel: "无操作",
	}
	id := itProbe(t, st, e)

	// 用完全相同的值再存一次。
	same := *e
	if err := st.UpdateITEquipment(id, &same, "qa"); err != nil {
		t.Fatalf("内容一致时更新应成功（幂等），实际: %v", err)
	}
	// 不存在的 id 必须报 ErrNoRows。
	if err := st.UpdateITEquipment(999999999, &same, "qa"); err == nil {
		t.Errorf("不存在的 id 应返回 sql.ErrNoRows，实际 nil")
	}
}
