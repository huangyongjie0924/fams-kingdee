package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// fieldFP 是单个字段的指纹。
//
// 关键设计：NonZero 必须记，不能只记字段名。
// 本轮最贵的一次误判就发生在这里——2026-09-17 19:18 扫到 finentry 里
// **确实有** originalval / networth 两个键，我据此下了「接口不提供金额」的结论；
// 20:07 再扫，同样的两个键还在，但旁边多出 39 个 originalfincard_* 的键，
// 真正的取值全挂在那边。
//
// 两个键"在"，和两个键"有数据"，是两回事。只记字段名的指纹看不出这个区别。
// 记下 NonZero，下次扫描才能直接报出「这个字段开始有值了」。
type fieldFP struct {
	Kind    string `json:"kind"`
	Present int    `json:"present"`
	NonZero int    `json:"nonzero"`
}

// fingerprint 是一次扫描的字段快照。
//
// 只记「字段在不在、有没有非零值」，**不记具体值**：
// 具体值随业务数据天天变，写进指纹只会把 diff 淹没在噪声里。
type fingerprint struct {
	ScannedAt   string                        `json:"scanned_at"`
	Rows        int                           `json:"rows"`
	UniqueCodes int                           `json:"unique_codes"`
	Fields      map[string]fieldFP            `json:"fields"`
	Nested      map[string]map[string]fieldFP `json:"nested"`
}

func loadFingerprint(path string) (*fingerprint, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f fingerprint
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func saveFingerprint(path string, f *fingerprint) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// hasValue 表示这个字段在本次扫描里真的取到过非零值。
func (f fieldFP) hasValue() bool { return f.NonZero > 0 }

// diffFields 比对一组字段，返回给人看的变化行。
//
// 分级（按"要不要立刻处理"排）：
//
//	消失     对方把投影列删了 —— 代码会静默读到零值，最危险
//	开始有值 字段一直恒 0，现在有数据了 —— 正是本轮的情形，通常意味着可以接入了
//	变全零   原本有值，现在全归零 —— 要么被撤了，要么对方改了列名
//	新增     多出来的键 —— 可能正是你要找的字段
//	计数变化 仅数量波动，通常只是业务数据增减，正常
func diffFields(old, cur map[string]fieldFP, verbose bool) (changes []string) {
	seen := map[string]bool{}
	var names []string
	for k := range old {
		seen[k] = true
		names = append(names, k)
	}
	for k := range cur {
		if !seen[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)

	for _, k := range names {
		o, inOld := old[k]
		c, inCur := cur[k]
		switch {
		case inOld && !inCur:
			changes = append(changes, fmt.Sprintf("  ✗ 消失     %-38s 上次 %s 出现 %d 次 / 非零 %d",
				k, o.Kind, o.Present, o.NonZero))
		case !inOld && inCur:
			tag := "新增"
			if c.hasValue() {
				tag = "新增*有值"
			}
			changes = append(changes, fmt.Sprintf("  + %-8s %-38s 本次 %s 出现 %d 次 / 非零 %d",
				tag, k, c.Kind, c.Present, c.NonZero))
		case o.hasValue() != c.hasValue():
			if c.hasValue() {
				changes = append(changes, fmt.Sprintf("  ! 开始有值 %-38s 非零 %d → %d   ← 可以接入了",
					k, o.NonZero, c.NonZero))
			} else {
				changes = append(changes, fmt.Sprintf("  ! 变全零   %-38s 非零 %d → %d   ← 数据被撤或列名改了",
					k, o.NonZero, c.NonZero))
			}
		default:
			if verbose && (o.Present != c.Present || o.NonZero != c.NonZero) {
				changes = append(changes, fmt.Sprintf("  · 计数变化 %-38s 出现 %d→%d / 非零 %d→%d",
					k, o.Present, c.Present, o.NonZero, c.NonZero))
			}
		}
	}
	return changes
}

// reportDiff 打印本次扫描与基线的差异，返回是否检测到「值得处理」的变化。
//
// 注意「值得处理」的口径：只有字段的增删和值域翻转算，
// 单纯的数量波动（新建了几张卡）不算 —— 否则每次扫描都在报警，很快就没人看了。
func reportDiff(old, cur *fingerprint, verbose bool) (important bool) {
	fmt.Printf("\n=== 与基线对比（基线 %s，%d 行 / %d 个编码）===\n",
		old.ScannedAt, old.Rows, old.UniqueCodes)

	if old.Rows != cur.Rows || old.UniqueCodes != cur.UniqueCodes {
		fmt.Printf("  总量: %d 行 → %d 行，%d 个编码 → %d 个编码\n",
			old.Rows, cur.Rows, old.UniqueCodes, cur.UniqueCodes)
	}

	var blocks []string
	if c := diffFields(old.Fields, cur.Fields, verbose); len(c) > 0 {
		blocks = append(blocks, "顶层字段：\n"+joinLines(c))
	}

	nestedNames := map[string]bool{}
	for k := range old.Nested {
		nestedNames[k] = true
	}
	for k := range cur.Nested {
		nestedNames[k] = true
	}
	var nn []string
	for k := range nestedNames {
		nn = append(nn, k)
	}
	sort.Strings(nn)
	for _, nk := range nn {
		o, inOld := old.Nested[nk]
		c, inCur := cur.Nested[nk]
		if !inOld {
			blocks = append(blocks, fmt.Sprintf("嵌套 %s：上次不存在，本次新增 %d 个子字段", nk, len(c)))
			continue
		}
		if !inCur {
			blocks = append(blocks, fmt.Sprintf("嵌套 %s：本次不存在了（上次 %d 个子字段）", nk, len(o)))
			continue
		}
		if ch := diffFields(o, c, verbose); len(ch) > 0 {
			blocks = append(blocks, fmt.Sprintf("嵌套 %s 的子字段：\n%s", nk, joinLines(ch)))
		}
	}

	if len(blocks) == 0 {
		fmt.Println("  无变化 —— 投影结构与上次一致")
		return false
	}
	for i, b := range blocks {
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(b)
	}

	important = isImportant(blocks)
	if important {
		fmt.Println("\n  ⚠ 检测到投影或值域变化。字段取不到值时，先看这里，别翻旧结论。")
	}
	return important
}

// isImportant 判断变化里有没有「必须人工确认」的项。
//
// 口径：字段增删 + 值域翻转算，单纯的数量波动不算。
// 为什么把计数波动排除掉：本库天天有卡在增减，如果 227 行变 228 行也报警，
// 每次扫描都是满屏 ⚠，很快就没人看了 —— 告警一旦开始狼来了，就等于没有。
func isImportant(blocks []string) bool {
	for _, b := range blocks {
		if strings.Contains(b, "✗ 消失") || strings.Contains(b, "! 开始有值") ||
			strings.Contains(b, "! 变全零") || strings.Contains(b, "新增*有值") {
			return true
		}
	}
	return false
}

func joinLines(ss []string) string {
	return strings.Join(ss, "\n")
}
