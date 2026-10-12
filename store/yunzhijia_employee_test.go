package store

import (
	"fmt"
	"testing"
	"time"
)

// TestSetEmployeeOpenIDByEmpNo_CountGuard 覆盖工号回填的三种命中情况（EmpNoMatch）：
//
//	EmpNoNotFound（查无此人）→ 不写，返回 EmpNoNotFound
//	EmpNoUnique（工号唯一）→ 写入，返回 EmpNoUnique
//	EmpNoAmbiguous（工号重复，≥2 行）→ **不写**，返回 EmpNoAmbiguous
//	    （宁可漏填，也绝不把 openId 填到错误的人头上）
//
// 为什么必须把「查无此人」与「工号重复」区分开：两者以前都被压成 n==0，
// 调用方只能一并当成失败计数——而云之家通讯录（约 4465 人）远大于本系统花名册
// （约 137 人），「查无此人」是绝大多数、也是**完全正常**的情况。不区分就会把
// 正常情况误报成失败（退出码恒为 1 + 每人一条 sync_run_error）。
// 故这里**三种情况都要保留断言**，尤其不能删掉「查无此人」那条。
//
// 为什么必须覆盖工号重复这一档：工号 emp_no 没有唯一索引，一旦撞上重复工号，
// 直接 `UPDATE ... WHERE emp_no=?` 会把 openId 写到两个人身上，通知就发给了错误的人。
// 这是静默故障——不会报错、不会让别的测试变红——所以单钉一遍。
//
// 连真库跑（沿用 testDSN 约定）；探针员工用 ZZ-OPENID- 前缀，末尾 defer 清理，不留残留。
func TestSetEmployeeOpenIDByEmpNo_CountGuard(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	uniqueNo := "ZZ-OPENID-ONE-" + suffix
	dupNo := "ZZ-OPENID-DUP-" + suffix
	missingNo := "ZZ-OPENID-NONE-" + suffix

	// 造 1 行：工号唯一
	res1, err := st.db.Exec(`INSERT INTO employee (emp_no, name, active) VALUES (?, 'ZZ 探针-唯一', 1)`, uniqueNo)
	if err != nil {
		t.Fatalf("建唯一工号探针失败: %v", err)
	}
	id1, _ := res1.LastInsertId()

	// 造 2 行：工号重复
	res2, err := st.db.Exec(`INSERT INTO employee (emp_no, name, active) VALUES (?, 'ZZ 探针-重复A', 1)`, dupNo)
	if err != nil {
		t.Fatalf("建重复工号探针A失败: %v", err)
	}
	id2, _ := res2.LastInsertId()
	res3, err := st.db.Exec(`INSERT INTO employee (emp_no, name, active) VALUES (?, 'ZZ 探针-重复B', 1)`, dupNo)
	if err != nil {
		t.Fatalf("建重复工号探针B失败: %v", err)
	}
	id3, _ := res3.LastInsertId()

	// 清理：按工号精确删除，不留残留。
	defer func() {
		_, _ = st.db.Exec(`DELETE FROM employee WHERE emp_no IN (?, ?, ?)`, uniqueNo, dupNo, missingNo)
	}()

	// 情况 0：查无此人 → EmpNoNotFound，不写
	match, err := st.SetEmployeeOpenIDByEmpNo(missingNo, "openid-none")
	if err != nil {
		t.Fatalf("查无此人场景出错: %v", err)
	}
	if match != EmpNoNotFound {
		t.Errorf("查无此人应返回 EmpNoNotFound，实际 %d", match)
	}

	// 情况 1：恰好 1 行 → EmpNoUnique，写入
	match, err = st.SetEmployeeOpenIDByEmpNo(uniqueNo, "openid-one")
	if err != nil {
		t.Fatalf("唯一工号场景出错: %v", err)
	}
	if match != EmpNoUnique {
		t.Errorf("唯一工号应返回 EmpNoUnique，实际 %d", match)
	}
	if got, err := st.EmployeeOpenID(id1); err != nil || got != "openid-one" {
		t.Errorf("唯一工号应写入 openid-one，实际 open_id=%q err=%v", got, err)
	}

	// 情况 2：≥2 行 → EmpNoAmbiguous，两行都不得被写
	match, err = st.SetEmployeeOpenIDByEmpNo(dupNo, "openid-dup")
	if err != nil {
		t.Fatalf("工号重复场景出错: %v", err)
	}
	if match != EmpNoAmbiguous {
		t.Errorf("工号重复应返回 EmpNoAmbiguous，实际 %d", match)
	}
	for _, id := range []int64{id2, id3} {
		if got, err := st.EmployeeOpenID(id); err != nil || got != "" {
			t.Errorf("工号重复时不得写入：id=%d open_id=%q err=%v", id, got, err)
		}
	}
}

// TestCountEmployeeOpenIDCoverage 钉住覆盖率统计的口径：只数 active=1 的在册员工，
// 且只有 open_id 非空才算「已覆盖」。
//
// 口径写错的后果很重——这个数直接决定「能不能打开 todo_enabled」：
//   - 把 active=0（离职）的人算进分母 ⇒ 覆盖率被稀释、看着"还有救"，而在册员工里其实一个都没有；
//   - 把 open_id 为空的也算作已覆盖 ⇒ 直接给出「可以开启」的错误结论。
//
// 库里已有真实数据，故断言**增量**而非绝对值。
//
// ⚠️ 代价：本测试对**并发写**敏感。断言的是「测试前后差值」，若同期有其它进程改动 employee 表，
// 基线就会被污染，表现为增量偏小甚至为负（实测见过 `已覆盖人数增量 = -1`）。
//
// **已知会误报的场景**：`go test ./...` 是**跨包并行**的，而 syncer 包的测试会写同一张 employee 表
// （见 testDSN：默认取 ../config.yaml 的 DSN，store 与 syncer 共用同一个库）。
// 故**跑全量测试请用 `go test -p 1 ./...`**（包级串行）。单独跑本包（`go test ./store/`）不受影响。
//
// 这是一个**既有**问题，与云之家流程中心改动无关；登记它是因为它已经误导过一次——
// 一次 `go test ./...` 报红、单独重跑却通过，容易让人误判「测试全绿」或「改动引入了失败」。
// 彻底的修法是让本测试用独立 schema，成本较高，暂以约定规避。
func TestCountEmployeeOpenIDCoverage(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	beforeWith, beforeTotal, err := st.CountEmployeeOpenIDCoverage()
	if err != nil {
		t.Fatalf("统计覆盖率失败: %v", err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var ids []int64
	ins := func(empNo, name string, active int, openID string) {
		t.Helper()
		res, err := st.db.Exec(
			`INSERT INTO employee (emp_no, name, active, open_id) VALUES (?, ?, ?, ?)`,
			empNo, name, active, openID)
		if err != nil {
			t.Fatalf("建探针员工失败 (%s): %v", empNo, err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	// 2 名在册且有 open_id → 分子分母都算
	ins("ZZ-COV-YES-A-"+suffix, "ZZ 覆盖-有A", 1, "openid-a-"+suffix)
	ins("ZZ-COV-YES-B-"+suffix, "ZZ 覆盖-有B", 1, "openid-b-"+suffix)
	// 1 名在册但 open_id 为空 → 只算分母
	ins("ZZ-COV-BLANK-"+suffix, "ZZ 覆盖-空", 1, "")
	// 1 名离职但有 open_id → 分子分母**都不该算**
	ins("ZZ-COV-INACTIVE-"+suffix, "ZZ 覆盖-离职", 0, "openid-c-"+suffix)
	defer func() {
		for _, id := range ids {
			_, _ = st.db.Exec("DELETE FROM employee WHERE id = ?", id)
		}
	}()

	with, total, err := st.CountEmployeeOpenIDCoverage()
	if err != nil {
		t.Fatalf("统计覆盖率失败: %v", err)
	}
	if delta, want := with-beforeWith, int64(2); delta != want {
		t.Errorf("已覆盖人数增量 = %d，期望 %d（2 名在册有 openId 应计入；离职那名与空值都不该计入）",
			delta, want)
	}
	if delta, want := total-beforeTotal, int64(3); delta != want {
		t.Errorf("在册总人数增量 = %d，期望 %d（3 名 active=1；离职那名不该计入分母）", delta, want)
	}
}
