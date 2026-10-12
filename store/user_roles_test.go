package store

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"asset-mgr/model"
)

// —— 多角色落库不变量（连真库；连不上则 Skip）——
//
// 钉死两条硬约定，它们是「role / roles 双列不漂移」与「FIND_IN_SET 能查到」的前提：
//  1. roles 列文本**不含空格**（api/notify_plan.go 的 FIND_IN_SET 依赖无空格）；
//  2. role 列恒等于 roles 列的第一个角色（主角色）——CreateUser / UpdateUser 两列同一条 SQL 一起写。
//
// 本项目集成测试会连库，运行请用 `go test -p 1 ./...` 串行，避免并发冲突。

func TestUserRolesColumnInvariants(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	// ⚠️ 必须用 t.Cleanup 关连接，**不能**写成 defer st.Close()：
	// Go 的 defer 在测试函数返回时执行，**早于** t.Cleanup 注册的回调，
	// 于是本文件下面那些 `t.Cleanup(删除测试账号)` 会因连接池已关而**静默失败**，
	// 测试数据就永久留在库里（实测积了 150+ 行 qa-* 账号，2026-09-24 发现）。
	// t.Cleanup 是 LIFO：这里最先注册 ⇒ 最后执行 ⇒ 删除语句跑完才关连接。
	// 同款约定与理由见 store/asset_scope_union_test.go 的注释。
	t.Cleanup(func() { st.Close() })

	ns := time.Now().UnixNano()
	username := fmt.Sprintf("qa-roles-%d", ns)
	// 故意乱序 + 重复 + 夹空白：CreateUser 内部走 NormalizeRoles，应按声明顺序排序去重。
	roles := []string{"employee", "maintainer", "admin", "employee", " maintainer "}

	id, err := st.CreateUser(username, "qa-pass-123", "QA多角色", roles, 0, 0, true)
	if err != nil {
		t.Fatalf("建多角色账号失败: %v", err)
	}
	t.Cleanup(func() { st.db.Exec("DELETE FROM sys_user WHERE id = ?", id) })

	var roleCol, rolesCol string
	if err := st.db.QueryRow("SELECT role, roles FROM sys_user WHERE id = ?", id).
		Scan(&roleCol, &rolesCol); err != nil {
		t.Fatalf("读回 role/roles 失败: %v", err)
	}

	// ① roles 列不得含空格。
	if strings.Contains(rolesCol, " ") {
		t.Errorf("roles 列不得含空格（FIND_IN_SET 依赖），实际 %q", rolesCol)
	}
	// ② roles 列 == FormatRoles(输入)（归一化 + 声明顺序：[admin, maintainer, employee]）。
	wantRoles := model.FormatRoles(roles)
	if rolesCol != wantRoles {
		t.Errorf("roles 列应为 %q，实际 %q", wantRoles, rolesCol)
	}
	// ③ role 列 == roles 列的第一个元素（主角色）。
	parsed := model.ParseRoles(rolesCol)
	if len(parsed) == 0 {
		t.Fatalf("roles 列解析为空：%q", rolesCol)
	}
	if roleCol != parsed[0] {
		t.Errorf("role 列应 == roles 的第一个角色 %q，实际 %q", parsed[0], roleCol)
	}
	// ④ 回读 User：Roles 与 role 与库内一致，且 EffectiveRoles 等于解析结果。
	u, err := st.GetUser(id)
	if err != nil || u == nil {
		t.Fatalf("GetUser 失败: %v", err)
	}
	if !reflect.DeepEqual(u.Roles, parsed) {
		t.Errorf("GetUser().Roles = %v, want %v", u.Roles, parsed)
	}
	if u.Role != roleCol {
		t.Errorf("GetUser().Role = %q, want %q", u.Role, roleCol)
	}
	if !reflect.DeepEqual(u.EffectiveRoles(), parsed) {
		t.Errorf("EffectiveRoles() = %v, want %v", u.EffectiveRoles(), parsed)
	}
}

func TestUpdateUserRolesColumnInvariants(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ns := time.Now().UnixNano()
	username := fmt.Sprintf("qa-roles-upd-%d", ns)
	id, err := st.CreateUser(username, "qa-pass-123", "QA多角色更新", []string{"employee"}, 0, 0, true)
	if err != nil {
		t.Fatalf("建账号失败: %v", err)
	}
	t.Cleanup(func() { st.db.Exec("DELETE FROM sys_user WHERE id = ?", id) })

	// 改成多角色（保持原密码：password 传空）。
	newRoles := []string{"counter", "employee"}
	if err := st.UpdateUser(id, "QA多角色更新", newRoles, 0, 0, true, ""); err != nil {
		t.Fatalf("更新账号失败: %v", err)
	}

	var roleCol, rolesCol string
	if err := st.db.QueryRow("SELECT role, roles FROM sys_user WHERE id = ?", id).
		Scan(&roleCol, &rolesCol); err != nil {
		t.Fatalf("读回 role/roles 失败: %v", err)
	}
	if strings.Contains(rolesCol, " ") {
		t.Errorf("更新后 roles 列不得含空格，实际 %q", rolesCol)
	}
	if want := model.FormatRoles(newRoles); rolesCol != want {
		t.Errorf("更新后 roles 列应为 %q，实际 %q", want, rolesCol)
	}
	parsed := model.ParseRoles(rolesCol)
	if len(parsed) == 0 || roleCol != parsed[0] {
		t.Errorf("更新后 role 列应 == roles 第一个角色 %q，实际 %q", parsed[0], roleCol)
	}
}

// TestCountEnabledAdminsCountsRoles 验证「统计启用中的管理员」按 roles 列算：
// 用增量（delta）断言，避免依赖库里既有的管理员数量。
func TestCountEnabledAdminsCountsRoles(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	base, err := st.CountEnabledAdmins()
	if err != nil {
		t.Fatalf("读基线管理员数失败: %v", err)
	}

	ns := time.Now().UnixNano()
	// 启用且含 admin → 计入。
	idA, err := st.CreateUser(fmt.Sprintf("qa-cnt-admin-%d", ns), "qa-pass-123", "QA-admin", []string{"admin"}, 0, 0, true)
	if err != nil {
		t.Fatalf("建 admin 账号失败: %v", err)
	}
	t.Cleanup(func() { st.db.Exec("DELETE FROM sys_user WHERE id = ?", idA) })
	if got, _ := st.CountEnabledAdmins(); got != base+1 {
		t.Errorf("启用 admin 后应 +1：base=%d got=%d", base, got)
	}

	// 停用且含 admin → 不计。
	idD, err := st.CreateUser(fmt.Sprintf("qa-cnt-admin-off-%d", ns), "qa-pass-123", "QA-admin-off", []string{"admin"}, 0, 0, false)
	if err != nil {
		t.Fatalf("建停用 admin 账号失败: %v", err)
	}
	t.Cleanup(func() { st.db.Exec("DELETE FROM sys_user WHERE id = ?", idD) })
	if got, _ := st.CountEnabledAdmins(); got != base+1 {
		t.Errorf("停用的 admin 不应计入：base=%d got=%d", base, got)
	}

	// 启用但非 admin → 不计。
	idE, err := st.CreateUser(fmt.Sprintf("qa-cnt-emp-%d", ns), "qa-pass-123", "QA-emp", []string{"employee"}, 0, 0, true)
	if err != nil {
		t.Fatalf("建 employee 账号失败: %v", err)
	}
	t.Cleanup(func() { st.db.Exec("DELETE FROM sys_user WHERE id = ?", idE) })
	if got, _ := st.CountEnabledAdmins(); got != base+1 {
		t.Errorf("非 admin 账号不应计入：base=%d got=%d", base, got)
	}
}

// —— sys_user.roles 单一写入者守卫 ——
//
// 不变量：sys_user.roles 必须**只**由 store/user.go 写，且与 role 列在**同一条 SQL** 里一起写。
// 一旦别处冒出第二个写入者，role 与 roles 迟早漂移（旧 token / 旧前端读 role，FIND_IN_SET 查 roles），
// 而这类故障**不会让任何其它测试变红**——正是本守卫存在的理由（与 category_guard_test.go 同思路）。
//
// 用**扫源码**而非连库：连库只能证明「当前这条语句没写 roles」，测不到将来新增的写入分支。
// 扫描用 filepath.Glob 通配（**不硬编码文件列表**），新增文件自动纳入。

// rolesWritersAllowed 是允许写 sys_user.roles 的非测试文件白名单（值=放行理由）。
var rolesWritersAllowed = map[string]string{
	"store/user.go": "账号增改的唯一业务写入者：roles 与 role 在同一条 INSERT/UPDATE 里一起写",
	// mysql.go 是运行时迁移里的一次性回填（addColumnIfMissingBackfill，只在建列时执行一次），
	// 它只从 role 复制到 roles、不改 role，是建列播种而非业务写入者，不构成漂移来源。
	// 若不排除它，守卫会把它自己的迁移回填误判成「第二个写入者」。
	"store/mysql.go": "运行时迁移的一次性回填 role→roles（只在建列时执行一次），非业务写入者",
}

// TestNoStraySysUserRolesWriter 断言：除白名单外，没有任何非测试文件写 sys_user.roles。
func TestNoStraySysUserRolesWriter(t *testing.T) {
	// 覆盖全仓：根目录 + 一级 + 二级子目录（api/ store/ model/ cmd/*/ integration/*/ …）。
	// 用通配而非硬编码文件列表——将来新增文件才会被自动纳入（守卫存在的唯一理由）。
	patterns := []string{"../*.go", "../*/*.go", "../*/*/*.go"}
	var files []string
	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err != nil {
			t.Fatalf("glob %q 失败：%v", pat, err)
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		t.Fatal("未扫到任何 .go 文件，glob 模式可能已失效")
	}

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue // 测试文件可自由造数据，不在守卫范围
		}
		// 先统一成斜杠再剥前缀：Windows 下 Glob 返回 "..\store\user.go"，
		// 若先 TrimPrefix("../") 会剥不掉，白名单匹配失败（踩过）。
		rel := strings.TrimPrefix(filepath.ToSlash(f), "../")
		if reason, ok := rolesWritersAllowed[rel]; ok {
			_ = reason // 白名单（理由见 map 注释）
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", f, err)
		}
		// 归一化：空白压成单空格、转小写，使跨行/缩进也能命中。
		norm := strings.ToLower(strings.Join(strings.Fields(string(b)), " "))

		// 只截取「列清单片段」再匹配 roles，**不能**对整文件 Contains("roles")——
		// 否则任何一处**读**该列（SELECT ... roles）都会误报。
		if frag := columnFragmentAfter(norm, "insert into sys_user ("); strings.Contains(frag, "roles") {
			t.Errorf("%s：对 sys_user 的 INSERT 列清单含 roles —— "+
				"roles 必须只由 store/user.go 与 role 同一条 SQL 一起写（见 docs/多角色改造方案.md §1）", f)
		}
		if frag := columnFragmentAfter(norm, "update sys_user set "); strings.Contains(frag, "roles") {
			t.Errorf("%s：对 sys_user 的 UPDATE SET 列表含 roles —— "+
				"roles 必须只由 store/user.go 与 role 同一条 SQL 一起写（见 docs/多角色改造方案.md §1）", f)
		}
	}
}

// columnFragmentAfter 返回 norm 中每一处 needle 之后、到语句边界（" where " 或 ")"）为止的片段，
// 用空格连接。只截取列清单片段，避免把对 roles 的读误判成写。
func columnFragmentAfter(norm, needle string) string {
	var out []string
	rest := norm
	for {
		i := strings.Index(rest, needle)
		if i < 0 {
			break
		}
		rest = rest[i+len(needle):]
		end := len(rest)
		for _, stop := range []string{" where ", ")"} {
			if j := strings.Index(rest, stop); j >= 0 && j < end {
				end = j
			}
		}
		out = append(out, rest[:end])
	}
	return strings.Join(out, " ")
}

// —— 废弃角色名就地迁移（连真库；连不上则 Skip）——
//
// 覆盖 migrateLegacyRoleNames()：改名 + 幂等 + 垃圾值只警告不改写 + 另一个废弃名也生效。
// 这是 2026-09-24 实测到的 P0：`viewer` 不在 model.Roles 里，若不就地改名，
// 存量账号会退化成空角色集合 ⇒ api/scope.go 零值 scope 被当「不限」⇒ 读到全量资产/维修单。
func TestMigrateLegacyRoleNames(t *testing.T) {
	st, err := New(testDSN(t))
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ns := time.Now().UnixNano()
	// ins 造一行「存量迁移前」的账号：role 与 roles 都等于给定值（模拟 ALTER 回填后的状态）。
	ins := func(role string) int64 {
		t.Helper()
		username := fmt.Sprintf("qa-legacy-%d-%s", ns, role)
		res, err := st.db.Exec(`INSERT INTO sys_user (username, password_hash, real_name, role, roles, enabled)
			VALUES (?, '', 'QA遗留角色', ?, ?, 1)`, username, role, role)
		if err != nil {
			t.Fatalf("建探针账号失败: %v", err)
		}
		id, _ := res.LastInsertId()
		t.Cleanup(func() { st.db.Exec("DELETE FROM sys_user WHERE id = ?", id) })
		return id
	}
	read := func(id int64) (string, string) {
		t.Helper()
		var role, roles string
		if err := st.db.QueryRow("SELECT role, roles FROM sys_user WHERE id = ?", id).Scan(&role, &roles); err != nil {
			t.Fatalf("读回 role/roles 失败: %v", err)
		}
		return role, roles
	}
	legacyRows := func() int {
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM sys_user
			WHERE role IN ('viewer','repair_tech')
			   OR FIND_IN_SET('viewer', roles) > 0 OR FIND_IN_SET('repair_tech', roles) > 0`).Scan(&n); err != nil {
			t.Fatalf("统计遗留角色名失败: %v", err)
		}
		return n
	}

	idViewer := ins("viewer")
	idTech := ins("repair_tech")
	idBogus := ins("bogus") // 既非现行角色也非废弃名 → 只警告、不改写

	// —— 第一次迁移 ——
	if err := st.migrateLegacyRoleNames(); err != nil {
		t.Fatalf("migrateLegacyRoleNames 第一次失败: %v", err)
	}
	if role, roles := read(idViewer); role != "employee" || roles != "employee" {
		t.Errorf("viewer 行应两列都变 employee，实际 role=%q roles=%q", role, roles)
	}
	if role, roles := read(idTech); role != "maintainer" || roles != "maintainer" {
		t.Errorf("repair_tech 行应两列都变 maintainer，实际 role=%q roles=%q", role, roles)
	}
	if role, roles := read(idBogus); role != "bogus" || roles != "bogus" {
		t.Errorf("bogus 行不应被改写（只警告），实际 role=%q roles=%q", role, roles)
	}

	// 改完再查应 0 命中废弃名（幂等的观测点）。
	n1 := legacyRows()
	t.Logf("第一次迁移后，遗留废弃角色名行数 = %d（期望 0）", n1)
	if n1 != 0 {
		t.Errorf("迁移后不应再有废弃角色名，实际 %d 行", n1)
	}

	// —— 第二次迁移：幂等，不改任何行 ——
	v1, vr1 := read(idViewer)
	t1, tr1 := read(idTech)
	b1, br1 := read(idBogus)
	if err := st.migrateLegacyRoleNames(); err != nil {
		t.Fatalf("migrateLegacyRoleNames 第二次失败（应幂等）: %v", err)
	}
	if r, rs := read(idViewer); r != v1 || rs != vr1 {
		t.Errorf("第二次迁移改动了 viewer 行：%q/%q → %q/%q", v1, vr1, r, rs)
	}
	if r, rs := read(idTech); r != t1 || rs != tr1 {
		t.Errorf("第二次迁移改动了 repair_tech 行：%q/%q → %q/%q", t1, tr1, r, rs)
	}
	if r, rs := read(idBogus); r != b1 || rs != br1 {
		t.Errorf("第二次迁移改动了 bogus 行：%q/%q → %q/%q", b1, br1, r, rs)
	}
	n2 := legacyRows()
	t.Logf("第二次迁移后，遗留废弃角色名行数 = %d（期望 0；即 0 行被改）", n2)
	if n2 != 0 {
		t.Errorf("第二次迁移后仍残留废弃角色名 %d 行", n2)
	}
}
