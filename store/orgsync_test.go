package store

import "testing"

// 同名冲突判定是「本地手工行 ↔ 星瀚同步行」的唯一判据。
// 这三个条件少判一个，结论就会反过来：
//   - 不看 source：已托管的行被反复报成冲突
//   - 不看是否同一行：绝大多数按编码命中的行被误报（实测第一版就把 65 名员工全报了）
//   - 不看名称是否在星瀚：本地独有的部门被误报
func TestIsOrgNameConflict(t *testing.T) {
	// 「财务部」星瀚有 78 个节点，同步会落到本地行 15（按编码命中）和新建的占位 0
	synced := map[string]map[int64]bool{
		"财务部":     {15: true, 0: true},
		"品牌资产管理部": {7: true},
		"直播业务部":   {0: true}, // dry-run 下"将会新建"的占位
	}

	cases := []struct {
		name   string
		row    string
		source string
		id     int64
		want   bool
		why    string
	}{
		{
			name: "手工行就是同步目标", row: "财务部", source: "", id: 15, want: false,
			why: "按编码命中了同一行，同步会更新它，不是冲突",
		},
		{
			name: "手工行与同步目标是两条不同的行", row: "财务部", source: "", id: 99, want: true,
			why: "同名但 ID 不同，同步后库里会有两条「财务部」，要提醒人",
		},
		{
			name: "已托管的行不再报冲突", row: "品牌资产管理部", source: "kingdee", id: 999, want: false,
			why: "source=kingdee 说明这行本来就归同步管，ID 对不上是映射表的问题，不该报成同名冲突",
		},
		{
			name: "手工行与将要新建的节点同名", row: "直播业务部", source: "", id: 42, want: true,
			why: "dry-run 用 0 占位，真实行 ID 永远不等于 0，所以能正确判成冲突",
		},
		{
			name: "星瀚没有这个名字", row: "某个本地独有的部门", source: "", id: 1, want: false,
			why: "不构成冲突，本地独有是正常的",
		},
		{
			name: "空名称", row: "", source: "", id: 1, want: false,
			why: "没名字无法判重",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsOrgNameConflict(c.row, c.source, c.id, synced); got != c.want {
				t.Errorf("IsOrgNameConflict(%q, %q, %d) = %v，期望 %v（%s）",
					c.row, c.source, c.id, got, c.want, c.why)
			}
		})
	}
}

// dry-run 的占位 ID 必须不等于任何真实行 ID，否则冲突会被漏报。
func TestOrgPendingIDSentinel(t *testing.T) {
	// 本地自增 ID 从 1 起，所以 0 是安全的占位
	synced := map[string]map[int64]bool{"新部门": {0: true}}
	if !IsOrgNameConflict("新部门", "", 1, synced) {
		t.Error("手工行 id=1 与「将会新建」的同名节点应当判为冲突")
	}
	if IsOrgNameConflict("新部门", "", 0, synced) {
		t.Error("占位 ID 0 自身不该被判成冲突")
	}
}
