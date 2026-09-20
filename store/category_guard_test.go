package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// syncPathFiles 返回「同步链路」的白名单文件：只有这些文件参与
// 「外部数据 → 本地分类」的写入。
//
// ⚠️ 刻意**不包含** store/masterdata.go：那里的 `UPDATE asset_category`（L79 附近）
// 是手工主数据维护路径（管理员在分类页保存），合法存在，**不在本守卫范围内**。
// 本守卫只回答一个问题：「同步会不会 UPDATE 分类行？」——手工维护会不会，不是它的事。
//
// 也**不包含** store/seed.go：它只在 asset_category 为空时写种子（生产非空，永不执行），
// 且只 INSERT、不 UPDATE。
func syncPathFiles(t *testing.T) []string {
	t.Helper()
	files := []string{"sync.go", "orgsync.go"}       // 均在同包 store/ 下
	syncerGo, err := filepath.Glob("../syncer/*.go") // syncer/ 目录下所有 .go
	if err != nil {
		t.Fatalf("列举 syncer 目录失败：%v", err)
	}
	return append(files, syncerGo...)
}

// TestCategorySyncHasNoUpdatePath 钉死需求 A 的安全前提：
// **同步链路**对 asset_category 只有 INSERT，没有 UPDATE。
//
// 为什么必须钉死：repairable 是台账本地列，之所以敢加，唯一依据就是
// 「同步不会 UPDATE 分类行」（见 docs/增量架构-可维修标签与首页.md §1.1）。
// 一旦将来有人在同步路径里加了 `UPDATE asset_category SET ...`（比如为了同步类别名/编码），
// 本地维护的 repairable 会被静默抹掉，且不会让任何测试变红。
//
// 用扫源码而不是连库：连库只能验证「当前这条 INSERT 没 UPDATE」，
// 测不到「未来新增的 UPDATE 分支」；扫源码能覆盖到新增代码。
//
// 范围限定为同步白名单（syncPathFiles）——手工维护路径 masterdata.go 的
// UPDATE 是合法的，**不在本守卫范围内**，所以绝不能改成「全库 grep」。
func TestCategorySyncHasNoUpdatePath(t *testing.T) {
	for _, f := range syncPathFiles(t) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", f, err)
		}
		// 归一化：所有空白（含换行/缩进）压成单空格、转小写，
		// 这样 "UPDATE\n\tasset_category" 也能被 "update asset_category" 命中。
		norm := strings.ToLower(strings.Join(strings.Fields(string(b)), " "))
		if strings.Contains(norm, "update asset_category") {
			t.Fatalf("%s（同步链路）出现了对 asset_category 的 UPDATE——"+
				"repairable 本地列会被同步抹掉（见 docs/增量架构-可维修标签与首页.md §1.1）", f)
		}
	}
}

// TestRepairableNotInCardColumnLists 是 §1.1 的镜像守卫：
// repairable 属于 asset_category，绝不能混进 asset_card 的三个列清单
// （混进去说明有人把「分类可维修」误当成「卡片字段」，语义就错了）。
func TestRepairableNotInCardColumnLists(t *testing.T) {
	lists := map[string][]string{
		"kingdeeOwnedColumns": kingdeeOwnedColumns,
		"cardInitColumns":     cardInitColumns,
		"cardWriteColumns":    cardWriteColumns,
	}
	for name, cols := range lists {
		for _, c := range cols {
			if c == "repairable" {
				t.Fatalf("%s 不能包含 repairable（它是 asset_category 的列，不是 asset_card 的）", name)
			}
		}
	}
}
