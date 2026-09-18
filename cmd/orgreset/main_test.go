package main

import "testing"

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
