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
	ScannedAt string `json:"scanned_at"`

	// Filter 是接口**回显的服务端生效过滤条件**（响应里的 data.filter）。
	//
	// 为什么要记它：2026-09-18 星瀚给 query-personnel 加了一条默认过滤
	// [(entryentity.orgstructure.number = '1202' OR ... '1201')]，
	// 请求体里传什么都不生效，行数从 5038 直接变 0。
	//
	// 当时只看到「行数 5038 → 0」，第一反应会是「对方把投影清了」或者「数据被删了」，
	// 而真相是配置里多了一条过滤。行数变化本身说不出原因，过滤条件才说得出来。
	// 同理，资产卡接口也在这天被加上了
	// [((org.number = '1202' OR org.number = '1201') AND billstatus = 'C')]，
	// 它恰好和业务上的可见范围一致，所以没被察觉 —— 这种「恰好没坏事」的变更最危险，
	// 因为它会让下一次变更失去参照。
	Filter string `json:"filter"`

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

// diffFilter 比对两次扫描里「服务端生效的过滤条件」。
//
// 这是本工具里唯一能看见「对方改了查询口径」的地方：过滤条件既不在请求体里
// （传什么都无效），也不在返回数据里（0 行时连字段都没有），只在这条回显上。
func diffFilter(old, cur string) (change string, important bool) {
	if old == cur {
		return "", false
	}
	switch {
	case old == "":
		return fmt.Sprintf("  + 过滤条件新增   %s\n      ← 接口开始带服务端过滤，行数可能骤降；"+
			"请求体里的 filter 无法覆盖，需星瀚侧调整", cur), true
	case cur == "":
		return fmt.Sprintf("  - 过滤条件移除   %s\n      ← 查询口径放宽，行数会变多，落库量需重新评估", old), true
	default:
		return fmt.Sprintf("  ! 过滤条件变更\n      上次 %s\n      本次 %s", old, cur), true
	}
}

// reportDiff 打印本次扫描与基线的差异，返回是否检测到「值得处理」的变化。
//
// 注意「值得处理」的口径：只有字段的增删、值域翻转、以及过滤条件变更算，
// 单纯的数量波动（新建了几张卡）不算 —— 否则每次扫描都在报警，很快就没人看了。
func reportDiff(old, cur *fingerprint, verbose bool) (important bool) {
	fmt.Printf("\n=== 与基线对比（基线 %s，%d 行 / %d 个编码）===\n",
		old.ScannedAt, old.Rows, old.UniqueCodes)

	// ---- 0 行必须单独走一条分支 ----
	//
	// 若照常 diff，会报出「48 个字段全部消失」——那是噪声，且方向完全错误：
	// 它暗示"对方删了投影列"，而真实原因通常是"接口没返回数据"（过滤条件被改、
	// 权限被收、单据被删）。把 0 行混进字段 diff 里，会把人送去查错的地方。
	if cur.Rows == 0 {
		fmt.Println("  ⚠ 本次返回 0 行 —— 字段清单无从采集，不能据此判断投影是否变化")
		if cur.Filter != "" {
			fmt.Printf("     服务端生效过滤: %s\n", cur.Filter)
		} else {
			fmt.Println("     响应里没有 filter 回显，无法判断是否被加了过滤条件")
		}
		fmt.Printf("     上次 %d 行 / %d 个编码；先查查询口径与授权，别翻字段清单\n",
			old.Rows, old.UniqueCodes)
		if change, imp := diffFilter(old.Filter, cur.Filter); change != "" {
			fmt.Println(change)
			important = imp
		}
		if important {
			fmt.Println("\n  ⚠ 查询口径变了。这不是投影问题，是过滤条件问题。")
		}
		return important
	}

	// ---- 过滤条件先报 ----
	// 放在字段 diff 之前：它是"为什么行数变了"的解释，应该先于现象出现。
	if change, imp := diffFilter(old.Filter, cur.Filter); change != "" {
		fmt.Println("过滤条件：")
		fmt.Println(change)
		important = imp
	}

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
		// 注意：这里不能直接 return false。过滤条件变了但字段没变时，
		// important 已经是 true，直接返回会把唯一的信号吞掉。
		if !important {
			fmt.Println("  无变化 —— 投影结构与上次一致")
			return false
		}
		return true
	}
	for i, b := range blocks {
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(b)
	}

	if isImportant(blocks) {
		important = true
	}
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
