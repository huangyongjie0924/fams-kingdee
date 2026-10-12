package store

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"asset-mgr/model"
)

// —— 资产可见范围并集（多角色）的 SQL 级回归 ——
//
// store/card.go 的 scopeConds 把 AssetScope 翻译成 WHERE 片段。多角色（如 [部门负责人, 员工]）
// 下两个维度**同时非零**，此时必须产出**并集** `(c.user_emp_id = ? OR c.use_dept_id IN (?,?))`。
// 早期实现是「各自 append 成两个 cond，由 buildWhere 用 AND 拼」→ **交集**，部门负责人兼员工
// 会几乎看不到本部门资产；单角色时两个维度只有一个非零，所以只有多角色才暴露。
//
// 本文件同时钉住两件事：① 多角色产出并集且参数顺序正确；② 零值 / 单维度产出与改造前**逐字节一致**。

// TestBuildWhereScopeUnion 多角色：产出并集片段，且 args 顺序为 [SelfEmpID, DeptIDs...]。
// ⚠️ args 顺序错了会把参数错位绑定（比 SQL 形态错更隐蔽），故显式 DeepEqual。
func TestBuildWhereScopeUnion(t *testing.T) {
	where, args := buildWhere(model.ListQuery{}, model.AssetScope{SelfEmpID: 1, DeptIDs: []int64{2, 3}})

	const wantFrag = "(c.user_emp_id = ? OR c.use_dept_id IN (?,?))"
	if !strings.Contains(where, wantFrag) {
		t.Errorf("多角色应产出并集片段 %q，实际 WHERE=%q", wantFrag, where)
	}
	// 反面：不得是 AND 拼出来的交集形态。
	if strings.Contains(where, "c.user_emp_id = ? AND c.use_dept_id IN") {
		t.Errorf("多角色不得产出交集（AND）形态，实际 WHERE=%q", where)
	}
	wantWhere := " WHERE c.deleted_at IS NULL AND " + wantFrag
	if where != wantWhere {
		t.Errorf("WHERE 应逐字节为 %q，实际 %q", wantWhere, where)
	}
	wantArgs := []any{int64(1), int64(2), int64(3)}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args 应为 %v（顺序错会参数错位绑定），实际 %v", wantArgs, args)
	}
}

// TestBuildWhereScopeRegression 回归保护：零值 / 单维度 scope 的 SQL 必须与改造前**逐字一致**
// （不含 OR、不含括号分组）。admin 与单角色账号走的就是这些分支，SQL 变一个字都算回归。
func TestBuildWhereScopeRegression(t *testing.T) {
	cases := []struct {
		name string
		sc   model.AssetScope
		want string
		args []any
	}{
		{
			name: "零值 scope（admin，不限）",
			sc:   model.AssetScope{},
			want: " WHERE c.deleted_at IS NULL",
			args: []any{},
		},
		{
			name: "仅 SelfEmpID（员工）",
			sc:   model.AssetScope{SelfEmpID: 1},
			want: " WHERE c.deleted_at IS NULL AND c.user_emp_id = ?",
			args: []any{int64(1)},
		},
		{
			name: "仅 DeptIDs（部门负责人）",
			sc:   model.AssetScope{DeptIDs: []int64{2, 3}},
			want: " WHERE c.deleted_at IS NULL AND c.use_dept_id IN (?,?)",
			args: []any{int64(2), int64(3)},
		},
	}
	for _, c := range cases {
		where, args := buildWhere(model.ListQuery{}, c.sc)
		if where != c.want {
			t.Errorf("%s：WHERE=%q，期望 %q", c.name, where, c.want)
		}
		if !reflect.DeepEqual(args, c.args) {
			t.Errorf("%s：args=%v，期望 %v", c.name, args, c.args)
		}
		// 单维度 / 零值分支绝不能引入 OR 或括号分组。
		if strings.Contains(where, " OR ") {
			t.Errorf("%s：单维度/零值 SQL 不应出现 OR，实际 %q", c.name, where)
		}
	}
}

// TestListCardsScopeUnionAgainstRealDB 端到端：两张卡分别命中两个维度，ListCards 都要查得出来。
// 这才是「并集真的生效」的证明（前两个用例只证明 SQL 形态）。
//
// 安全性：走 testDSN 的安全阀——只有 DSN 指向**像测试库**的库（库名含 test）才会真跑，
// 否则整条用例 Skip（绝不连生产库 asset）。本用例自建自清，只碰 asset_code 带唯一前缀的行。
func TestListCardsScopeUnionAgainstRealDB(t *testing.T) {
	dsn := testDSN(t)
	st, err := New(dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	// ⚠️ 必须用 t.Cleanup 关连接，**不能**写成 defer st.Close()：
	// Go 的 defer 在测试函数返回时执行，**早于**下面 insert() 注册的 `t.Cleanup(删除探针卡)`，
	// 连接池先被关掉 ⇒ 删除语句静默失败 ⇒ 三张探针卡每次运行都残留 3 行（本用例曾长期如此）。
	// t.Cleanup 是 LIFO：这里最先注册 ⇒ 最后执行 ⇒ 删除跑完才关连接。
	// 同款约定与理由见 store/user_roles_test.go 的注释，勿「顺手简化」回去。
	t.Cleanup(func() { st.Close() })

	// 用探针部门 id 而非真实部门：scope 的 `use_dept_id IN (...)` 直接过滤卡上的快照列，
	// 不 JOIN department 表，故不需要该部门真实存在——这样测试库为空（无部门主数据）时也能真跑，
	// 不会被 Skip 成「假绿」。卡全部带唯一 prefix，结果集只含本用例造的三张。
	const probeDeptID = int64(880001)

	ns := time.Now().UnixNano()
	prefix := fmt.Sprintf("QA-SCOPE-UNION-%d", ns)
	const empID = int64(7701)

	insert := func(code string, userEmp, useDept int64) {
		t.Helper()
		_, err := st.DB().Exec(
			`INSERT INTO asset_card (asset_code, name, status, biz_status, category_id, user_emp_id, use_dept_id, created_by)
			 VALUES (?, ?, ?, '', 0, ?, ?, 'qa')`,
			code, "QA并集探针卡", model.StatusInUse, userEmp, useDept)
		if err != nil {
			t.Fatalf("建探针卡失败: %v", err)
		}
		t.Cleanup(func() { st.DB().Exec("DELETE FROM asset_card WHERE asset_code=?", code) })
	}
	// A 命中 SelfEmpID 维度；B 命中 DeptIDs 维度；C 两个维度都不命中（对照）。
	insert(prefix+"-A", empID, 0)
	insert(prefix+"-B", 0, probeDeptID)
	insert(prefix+"-C", 0, 0)

	res, err := st.ListCards(model.ListQuery{Keyword: prefix, Page: 1, PageSize: 50},
		model.AssetScope{SelfEmpID: empID, DeptIDs: []int64{probeDeptID}})
	if err != nil {
		t.Fatalf("ListCards 失败: %v", err)
	}
	got := map[string]bool{}
	for _, it := range res.Items {
		got[it.AssetCode] = true
	}
	if !got[prefix+"-A"] {
		t.Errorf("并集应查出命中 SelfEmpID 的卡 A，实际 items=%v", got)
	}
	if !got[prefix+"-B"] {
		t.Errorf("并集应查出命中 DeptIDs 的卡 B，实际 items=%v", got)
	}
	if got[prefix+"-C"] {
		t.Errorf("两个维度都不命中的卡 C 不该出现，实际 items=%v", got)
	}
}

// —— 第三维（有未结单维修单的资产）的 SQL 级回归 ——

// TestBuildWhereScopeUnionThreeWay 三维同时非零：产出 `(A OR B OR C)`，
// 且 args 顺序为 [SelfEmpID, DeptIDs..., RepairingCardIDs...]。
// ⚠️ args 顺序错了会把参数错位绑定（比 SQL 形态错更隐蔽），故显式 DeepEqual。
func TestBuildWhereScopeUnionThreeWay(t *testing.T) {
	where, args := buildWhere(model.ListQuery{}, model.AssetScope{
		SelfEmpID:        1,
		DeptIDs:          []int64{2, 3},
		RepairingCardIDs: []int64{100, 200},
	})

	const wantFrag = "(c.user_emp_id = ? OR c.use_dept_id IN (?,?) OR c.id IN (?,?))"
	wantWhere := " WHERE c.deleted_at IS NULL AND " + wantFrag
	if where != wantWhere {
		t.Errorf("三维 WHERE 应逐字节为 %q，实际 %q", wantWhere, where)
	}
	wantArgs := []any{int64(1), int64(2), int64(3), int64(100), int64(200)}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args 应为 %v（顺序错会参数错位绑定），实际 %v", wantArgs, args)
	}
}

// TestBuildWhereScopeRepairingOnly 只有 RepairingCardIDs 非零（employee 独有维度的单维形态）：
// 产出单条条件 `c.id IN (?,?)`，**不加括号分组**（与单维 self/dept 同形）。
func TestBuildWhereScopeRepairingOnly(t *testing.T) {
	where, args := buildWhere(model.ListQuery{}, model.AssetScope{RepairingCardIDs: []int64{100, 200}})

	wantWhere := " WHERE c.deleted_at IS NULL AND c.id IN (?,?)"
	if where != wantWhere {
		t.Errorf("单维（repairing）WHERE 应逐字节为 %q（不加括号），实际 %q", wantWhere, where)
	}
	if strings.Contains(where, "AND (") {
		t.Errorf("单维不应出现括号分组，实际 %q", where)
	}
	wantArgs := []any{int64(100), int64(200)}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args 应为 %v，实际 %v", wantArgs, args)
	}
}

// TestListOpenRepairCardIDsAgainstRealDB 真库用例：验证「未到终态」的筛选与 card_id>0 哨兵排除。
//
// 造 3 张卡各挂一条维修单：pending / repairing / done；再补一条 card_id=0 的 pending，
// 以及**第二条**指向 pending 那张卡的 pending（验证 DISTINCT 去重）。
// 断言：pending/repairing 两张卡在集合内、done 与 0 被排除、重复卡只出现一次。
//
// 安全性：走 testDSN 的安全阀——DSN 指向「像测试库」的库（库名含 test）才真跑，否则 Skip。
// 自建自清，只碰带唯一前缀的行，不依赖参考数据，也不会误删他行。
func TestListOpenRepairCardIDsAgainstRealDB(t *testing.T) {
	dsn := testDSN(t)
	st, err := New(dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	// ⚠️ 必须用 t.Cleanup 关连接，**不能**用 defer st.Close()：Go 的 defer 在测试函数返回时
	// 先于 t.Cleanup 执行，会把连接池关掉，导致下面的 t.Cleanup 删除语句静默失败、残留测试数据
	// （曾把 TestQADataHygiene 打红）。先注册本清理 → LIFO 使其**最后**执行，晚于所有删行清理。
	t.Cleanup(func() { st.Close() })

	ns := time.Now().UnixNano()
	prefix := fmt.Sprintf("QA-OPENREPAIR-%d", ns)
	// repair_order.code 是 VARCHAR(32)，上面这个前缀太长，维修单用短码。
	codeBase := fmt.Sprintf("QAOR%d", ns)

	insertCard := func(suffix string) int64 {
		t.Helper()
		res, err := st.DB().Exec(
			`INSERT INTO asset_card (asset_code, name, status, biz_status, category_id, user_emp_id, use_dept_id, created_by)
			 VALUES (?, ?, ?, '', 0, 0, 0, 'qa')`,
			prefix+"-"+suffix, "QA未结单探针卡", model.StatusInUse)
		if err != nil {
			t.Fatalf("建探针卡失败: %v", err)
		}
		id, _ := res.LastInsertId()
		t.Cleanup(func() { st.DB().Exec("DELETE FROM asset_card WHERE id=?", id) })
		return id
	}
	insertRepair := func(code string, cardID int64, status string) {
		t.Helper()
		if _, err := st.DB().Exec(
			`INSERT INTO repair_order (code, card_id, reporter_emp_id, status, created_by)
			 VALUES (?, ?, 0, ?, 'qa')`, code, cardID, status); err != nil {
			t.Fatalf("建维修单失败: %v", err)
		}
		t.Cleanup(func() { st.DB().Exec("DELETE FROM repair_order WHERE code=?", code) })
	}

	cardPending := insertCard("P")
	cardRepairing := insertCard("R")
	cardDone := insertCard("D")

	insertRepair(codeBase+"-p1", cardPending, model.RepairPending)
	insertRepair(codeBase+"-p2", cardPending, model.RepairPending) // 同卡第二条：验证去重
	insertRepair(codeBase+"-r1", cardRepairing, model.RepairRepairing)
	insertRepair(codeBase+"-d1", cardDone, model.RepairDone)
	insertRepair(codeBase+"-z1", 0, model.RepairPending) // card_id=0 哨兵：必须排除
	ids, err := st.ListOpenRepairCardIDs()
	if err != nil {
		t.Fatalf("ListOpenRepairCardIDs 失败: %v", err)
	}

	got := map[int64]int{}
	for _, id := range ids {
		got[id]++
	}
	if got[cardPending] == 0 {
		t.Errorf("pending 单对应的卡 %d 应在集合内，实际 %v", cardPending, ids)
	}
	if got[cardPending] > 1 {
		t.Errorf("同一张卡有两条未结单时应只出现一次（DISTINCT），实际出现 %d 次", got[cardPending])
	}
	if got[cardRepairing] == 0 {
		t.Errorf("repairing 单对应的卡 %d 应在集合内，实际 %v", cardRepairing, ids)
	}
	if got[cardDone] != 0 {
		t.Errorf("done 是终态，对应的卡 %d 不应在集合内，实际 %v", cardDone, ids)
	}
	if got[0] != 0 {
		t.Errorf("card_id=0 是「未设置」哨兵值，不应进入集合，实际 %v", ids)
	}
}
