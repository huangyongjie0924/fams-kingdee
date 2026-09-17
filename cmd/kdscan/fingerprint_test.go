package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本轮的现场：2026-09-17 19:18 扫到的 finentry 只有 2 个键，且全库恒为 0；
// 20:07 再扫，同样的 2 个键还在，但多出 37 个 originalfincard_* 且带着真值。
// 当时没有任何比对手段，只能靠人记住「上次是什么样」——结果就是拿旧快照下了错结论。
// 这个用例把那次变更钉住：这种改动必须被报出来。
func TestDiffFieldsCatchesSilentProjectionChange(t *testing.T) {
	old := map[string]fieldFP{
		// 旧投影：两个键都在，但从来没有过非零值
		"fin_originalval": {Kind: "scalar", Present: 227, NonZero: 0},
		"fin_networth":    {Kind: "scalar", Present: 227, NonZero: 0},
	}
	cur := map[string]fieldFP{
		"fin_originalval":                {Kind: "scalar", Present: 227, NonZero: 0},
		"fin_networth":                   {Kind: "scalar", Present: 227, NonZero: 0},
		"originalfincard_originalval":    {Kind: "scalar", Present: 200, NonZero: 200},
		"originalfincard_accumdepre":     {Kind: "scalar", Present: 200, NonZero: 200},
		"originalfincard_finaccountdate": {Kind: "scalar", Present: 200, NonZero: 200},
	}

	changes := diffFields(old, cur, false)
	joined := joinLines(changes)

	if !strings.Contains(joined, "originalfincard_originalval") {
		t.Fatalf("新增的 originalfincard_originalval 没被报出来:\n%s", joined)
	}
	if !strings.Contains(joined, "新增*有值") {
		t.Errorf("新增且有值的字段应标记为「新增*有值」（区别于新增但恒 0）:\n%s", joined)
	}
	// 两个恒 0 的旧键这次也没变，不该出现在报告里
	if strings.Contains(joined, "fin_networth") {
		t.Errorf("fin_networth 两次都是全零，不该报变化:\n%s", joined)
	}
	if !isImportant(changes) {
		t.Error("静默新增投影列是必须人工确认的变化，isImportant 应为 true")
	}
}

// 「键在」不等于「有数据」——本轮误判的根源。
// 一个键从恒 0 变成有值，正是"可以接入了"的信号，必须单独分级报出来。
func TestDiffFieldsReportsValueFlip(t *testing.T) {
	t.Run("开始有值", func(t *testing.T) {
		old := map[string]fieldFP{"f": {Kind: "scalar", Present: 200, NonZero: 0}}
		cur := map[string]fieldFP{"f": {Kind: "scalar", Present: 200, NonZero: 200}}

		changes := diffFields(old, cur, false)
		joined := joinLines(changes)
		if !strings.Contains(joined, "开始有值") {
			t.Fatalf("恒 0 → 有值 应报「开始有值」:\n%s", joined)
		}
		if !isImportant(changes) {
			t.Error("值域翻转必须人工确认")
		}
	})

	t.Run("变全零", func(t *testing.T) {
		old := map[string]fieldFP{"f": {Kind: "scalar", Present: 200, NonZero: 200}}
		cur := map[string]fieldFP{"f": {Kind: "scalar", Present: 200, NonZero: 0}}

		changes := diffFields(old, cur, false)
		joined := joinLines(changes)
		if !strings.Contains(joined, "变全零") {
			t.Fatalf("有值 → 全零 应报「变全零」:\n%s", joined)
		}
		if !isImportant(changes) {
			t.Error("数据被撤必须人工确认")
		}
	})
}

// 投影列被删是最危险的一种：代码不报错，只是从此读到零值。
func TestDiffFieldsReportsRemovedField(t *testing.T) {
	old := map[string]fieldFP{"gone": {Kind: "scalar", Present: 200, NonZero: 200}}
	cur := map[string]fieldFP{}

	changes := diffFields(old, cur, false)
	if !strings.Contains(joinLines(changes), "✗ 消失") {
		t.Fatalf("消失的字段必须报出来:\n%s", joinLines(changes))
	}
	if !isImportant(changes) {
		t.Error("字段消失必须人工确认")
	}
}

// 业务数据每天在变（新建卡、金额变动），单纯计数波动不该报警——
// 否则每次扫描满屏 ⚠，告警就废了。
func TestDiffFieldsIgnoresCountDriftUnlessVerbose(t *testing.T) {
	old := map[string]fieldFP{"f": {Kind: "scalar", Present: 227, NonZero: 200}}
	cur := map[string]fieldFP{"f": {Kind: "scalar", Present: 228, NonZero: 201}}

	if changes := diffFields(old, cur, false); len(changes) != 0 {
		t.Fatalf("非 verbose 下不该报计数波动，实际:\n%s", joinLines(changes))
	}
	if isImportant(diffFields(old, cur, false)) {
		t.Error("计数波动不该触发重要告警")
	}

	verboseChanges := diffFields(old, cur, true)
	if len(verboseChanges) == 0 {
		t.Fatal("verbose 下应报出计数波动")
	}
	if !strings.Contains(joinLines(verboseChanges), "计数变化") {
		t.Errorf("计数波动应标记为「计数变化」:\n%s", joinLines(verboseChanges))
	}
	// 关键：即使 verbose 打出来了，也不能算重要变化
	if isImportant(verboseChanges) {
		t.Error("计数波动即使 verbose 显示，也不该算重要变化")
	}
}

// 没有任何变化时不该输出任何行，避免"看起来像有变化"。
func TestDiffFieldsNoChange(t *testing.T) {
	f := map[string]fieldFP{
		"a": {Kind: "scalar", Present: 227, NonZero: 200},
		"b": {Kind: "array", Present: 227, NonZero: 200},
	}
	if changes := diffFields(f, f, true); len(changes) != 0 {
		t.Fatalf("相同指纹不该报变化，实际:\n%s", joinLines(changes))
	}
}

// 指纹要能落盘再读回来且完全等价——否则下次扫描的比对基准就是错的。
func TestFingerprintRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fp.json")

	want := &fingerprint{
		ScannedAt:   "2026-09-17 23:32:00",
		Rows:        227,
		UniqueCodes: 200,
		Fields: map[string]fieldFP{
			"number": {Kind: "scalar", Present: 227, NonZero: 227},
		},
		Nested: map[string]map[string]fieldFP{
			"finentry": {
				"originalfincard_originalval": {Kind: "scalar", Present: 200, NonZero: 200},
				"fin_originalval":             {Kind: "scalar", Present: 200, NonZero: 0},
			},
		},
	}
	if err := saveFingerprint(path, want); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	got, err := loadFingerprint(path)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if got.Rows != want.Rows || got.UniqueCodes != want.UniqueCodes || got.ScannedAt != want.ScannedAt {
		t.Errorf("标量字段不一致: got %+v / want %+v", got, want)
	}
	if len(got.Fields) != 1 || got.Fields["number"].NonZero != 227 {
		t.Errorf("顶层字段没读回: %+v", got.Fields)
	}
	sub := got.Nested["finentry"]
	if len(sub) != 2 {
		t.Fatalf("嵌套子字段没读回: %+v", sub)
	}
	// 恒 0 的键必须原样保留 —— 它正是用来判断"键在但没数据"的依据
	if sub["fin_originalval"].Present != 200 || sub["fin_originalval"].NonZero != 0 {
		t.Errorf("恒 0 子字段的 present/nonzero 被读错了: %+v", sub["fin_originalval"])
	}
}

// 基线文件存在但内容坏了时，必须报错而不是当成"首次运行"——
// 否则会静默覆盖掉一份本来有价值的快照。
func TestLoadFingerprintRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFingerprint(path); err == nil {
		t.Fatal("坏文件应当返回错误")
	}
	// 文件不存在则应是 IsNotExist，调用方据此走"首次建基线"分支
	if _, err := loadFingerprint(filepath.Join(dir, "nope.json")); !os.IsNotExist(err) {
		t.Fatalf("不存在的文件应返回 IsNotExist，实际: %v", err)
	}
}
