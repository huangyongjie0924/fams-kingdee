package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	"asset-mgr/config"
)

// TestCheckConfirm 守住「真跑前必须确认库名」这道闸。
//
// 这个工具会删掉全部部门与员工。这道闸失效的表现不是报错，
// 而是在没人确认的情况下把库删干净——所以它值得单独钉一遍。
func TestCheckConfirm(t *testing.T) {
	cases := []struct {
		name     string
		confirm  string
		database string
		wantErr  bool
	}{
		{"空确认必须拒绝", "", "asset", true},
		{"只有空白也必须拒绝", "   ", "asset", true},
		{"库名不匹配必须拒绝", "asset_test", "asset", true},
		{"大小写不同必须拒绝", "ASSET", "asset", true},
		{"写成别的库名必须拒绝", "asset_rst_purge", "asset", true},
		{"完全一致才放行", "asset", "asset", false},
		{"库名里的下划线要能对上", "asset_rst_purge", "asset_rst_purge", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkConfirm(c.confirm, c.database)
			if c.wantErr && err == nil {
				t.Errorf("confirm=%q database=%q 应当被拒绝，却放行了", c.confirm, c.database)
			}
			if !c.wantErr && err != nil {
				t.Errorf("confirm=%q database=%q 应当放行，却被拒绝: %v", c.confirm, c.database, err)
			}
		})
	}
}

// TestPurgeOrderPutsMapFirst 钉住清理顺序：映射必须在三张表之前删。
//
// 反过来的话，中途失败会留下「表已空、映射还在」的最坏状态——
// 之后组织同步会拿映射里的旧 ID 去 UPDATE，UPDATE 影响 0 行且不报错，
// 那些行就再也建不出来了。实测过这个状态：公司 0/2、部门 59/88、员工 71/136。
func TestPurgeOrderPutsMapFirst(t *testing.T) {
	want := []string{"department", "employee", "company"}
	if len(purgeTables) != len(want) {
		t.Fatalf("要清的表变成了 %v", purgeTables)
	}
	for i := range want {
		if purgeTables[i] != want[i] {
			t.Errorf("第 %d 张表应为 %s，实际 %s", i+1, want[i], purgeTables[i])
		}
	}

	// 三类映射一个都不能少：漏掉哪一类，那一类的主数据就会在重建时"凭空消失"。
	if len(mapKinds) != 3 {
		t.Fatalf("要清的映射类别变成了 %v", mapKinds)
	}
	seen := map[string]bool{}
	for _, k := range mapKinds {
		seen[k] = true
	}
	for _, k := range []string{"company", "department", "employee"} {
		if !seen[k] {
			t.Errorf("映射类别漏了 %s —— 它的主数据会在重建时全部消失", k)
		}
	}
}

// testDB 打开配置里的库用于只读断言。拿不到就 Skip，
// 与 store 包里需要数据库的测试同一套约定（默认不依赖外部库）。
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "config.yaml"))
	if err != nil {
		t.Skipf("读不到 ../../config.yaml，跳过需要数据库的测试: %v", err)
	}
	db, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		t.Skipf("连接数据库失败，跳过: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Skipf("数据库不可达，跳过: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestInspectSplitsVisibleAndSoftDeletedCards 守住「软删除卡不进断链统计」。
//
// 这个体检报告是重建之后唯一的验收依据。把软删除的卡算进来，会报出一批
// 界面上根本看不到、也不需要处理的卡，让人以为影响面比实际大——
// 实测就报过"4 张卡断链"，其中 2 张早已软删。
//
// 只读断言，可以对着开发库跑。
func TestInspectSplitsVisibleAndSoftDeletedCards(t *testing.T) {
	db := testDB(t)

	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM asset_card").Scan(&total); err != nil {
		t.Fatalf("统计 asset_card 失败: %v", err)
	}

	r, err := inspect(db)
	if err != nil {
		t.Fatalf("体检失败: %v", err)
	}

	if r.Cards+r.SoftDeletedCards != total {
		t.Errorf("可见 %d + 软删 %d 应等于总数 %d，说明有一类被漏统计",
			r.Cards, r.SoftDeletedCards, total)
	}

	// 断链明细只能出现在可见卡上：条数超过可见卡数就说明把软删除的也算进来了。
	broken := r.CardDeptBroken + r.CardEmpBroken + r.CardCompanyBroken
	if r.Cards == 0 && broken > 0 {
		t.Errorf("可见卡为 0 却有 %d 条断链，统计口径不对", broken)
	}
	if len(r.BrokenCards) == 0 && broken > 0 {
		t.Errorf("汇总报 %d 条断链但明细为空，两者口径不一致", broken)
	}
	if len(r.BrokenCards) > 0 && broken == 0 {
		t.Errorf("明细有 %d 条但汇总为 0，两者口径不一致", len(r.BrokenCards))
	}
}
