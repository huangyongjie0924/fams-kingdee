// kdscan 全量扫描星瀚接口的返回字段，用于诊断「字段取不到值 / 对方改了投影 / 对方改了查询口径」。
//
// 为什么把它留成常驻命令：星瀚侧会改接口，而且是**静默改**——
//   - 2026-09-17 当天 19:18 扫到的 finentry 只有 2 个键（且全库恒为 0），20:07 再扫变成 39 个键、
//     真实取值挂在 originalfincard_* 前缀上。
//   - 2026-09-18 人员接口被加了一条服务端过滤条件，请求体怎么传都覆盖不了，
//     行数从 5038 直接变 0。
//
// 所以「字段取不到值」的第一反应应该是重扫，而不是翻旧结论。有这条命令，重扫就是一行：
//
//	go run ./cmd/kdscan -config config.yaml                    # 资产卡
//	go run ./cmd/kdscan -config config.yaml -target personnel  # 人员
//	go run ./cmd/kdscan -config config.yaml -target dept       # 部门（行政组织）
//	go run ./cmd/kdscan -config config.yaml -target all        # 三个都扫
//
// 为什么不用 struct 解：Go 的 encoding/json 会**静默丢弃**结构体没有声明的键。
// 用 struct 去问「接口返回了哪些字段」，答案永远是「我声明过的那些」——自证循环。
// 所以这里一律解成 map[string]any，并保留 json.Number 以看清原始数值字面量。
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"asset-mgr/config"
)

func post(url string, body any, headers map[string]string) ([]byte, error) {
	payload, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func decode(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // 保住原始数值字面量，float64 会把 0.0000000000 变成 0
	return dec.Decode(dst)
}

// isEmpty 判定 null / 空串 / 空数组 / 空对象
func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// isZero 在 isEmpty 之上再判定数值/布尔的零
func isZero(v any) bool {
	if isEmpty(v) {
		return true
	}
	switch strings.TrimSpace(fmt.Sprintf("%v", v)) {
	case "0", "0.0", "false", "0.000000", "0.0000000000":
		return true
	}
	return false
}

// source 是一个可扫描的星瀚接口。
type source struct {
	name     string         // asset | personnel，用于命令行与文件命名
	label    string         // 中文名，用于打印
	path     string         // 接口路径
	dataBody map[string]any // 请求体里的 data 部分
	baseline string         // 指纹基线文件
}

// resolveSources 把 -target 展开成要扫的接口清单。
func resolveSources(cfg *config.Config, target, dir, override string) ([]source, error) {
	asset := source{
		name:  "asset",
		label: "资产卡",
		path:  cfg.Kingdee.QueryPath,
		// 资产卡接口要求 data 里必须有 assetname / billno（接口会校验非空，实测不参与过滤）。
		// filter 传空：真正的过滤条件在星瀚侧的接口配置里，这里传什么都覆盖不了，
		// 唯一能知道它是什么的办法是读响应里的 filter 回显。
		dataBody: map[string]any{"assetname": "1", "billno": "1", "filter": ""},
		baseline: joinDir(dir, "kingdee-fields-baseline.json"),
	}
	personnel := source{
		name:  "personnel",
		label: "人员",
		path:  cfg.Kingdee.PersonnelQueryPath,
		// 人员接口只要求 data 存在（缺了报「请求参数没有 data 数据」）。
		// 里面的键（filter / name / number / enable ...）实测全部不生效，
		// 过滤条件同样在星瀚侧的接口配置里。
		// 官方文档的请求体参数只有 createtime / pageSize / pageNo 三个，
		// createtime 是增量同步的时间水位，留空即不按时间过滤。
		dataBody: map[string]any{},
		baseline: joinDir(dir, "kingdee-personnel-baseline.json"),
	}
	department := source{
		name:  "dept",
		label: "部门（行政组织）",
		path:  cfg.Kingdee.DepartmentQueryPath,
		// 部门接口同样只要求 data 存在，但 pageSize 是**必填**
		// （缺了报「页大小pageSize不能为空」）。分页参数由 scanSource 统一带上。
		dataBody: map[string]any{},
		baseline: joinDir(dir, "kingdee-dept-baseline.json"),
	}

	switch target {
	case "asset":
		if override != "" {
			asset.baseline = override
		}
		return []source{asset}, nil
	case "personnel":
		if override != "" {
			personnel.baseline = override
		}
		return []source{personnel}, nil
	case "dept":
		if override != "" {
			department.baseline = override
		}
		return []source{department}, nil
	case "all":
		if override != "" {
			return nil, fmt.Errorf("-baseline 只在单目标扫描时有意义，-target=all 请改用 -baseline-dir")
		}
		return []source{asset, personnel, department}, nil
	default:
		return nil, fmt.Errorf("未知 -target=%q，可选 asset | personnel | dept | all", target)
	}
}

// joinDir 用正斜杠拼路径：Windows 下 filepath.Join 会产出反斜杠，
// 打印出来和文档/README 里写的对不上，容易让人以为扫的是另一个文件。
func joinDir(dir, name string) string {
	if dir == "" {
		return name
	}
	return strings.TrimSuffix(dir, "/") + "/" + name
}

// scanSource 扫描一个接口，打印结构明细并与基线比对，返回是否检测到值得处理的变化。
func scanSource(cfg *config.Config, src source, token, rawPrefix, code string, pageSize int,
	verbose, updateBaseline bool) (important bool, err error) {

	fmt.Printf("\n\n########## %s（%s）##########\n", src.label, src.path)

	// ---- 1) 分页拉全量 ----
	var rows []map[string]any
	filterEcho := ""
	var totalCount int64
	for page := 1; ; page++ {
		raw, err := post(cfg.Kingdee.BaseURL+src.path, map[string]any{
			"data":     src.dataBody,
			"pageNo":   page,
			"pageSize": pageSize,
		}, map[string]string{
			"accessToken":     token,
			"Idempotency-Key": fmt.Sprintf("%d", time.Now().UnixNano()),
		})
		if err != nil {
			return false, fmt.Errorf("query request: %w", err)
		}
		if rawPrefix != "" {
			name := fmt.Sprintf("%s.%s.page%d", rawPrefix, src.name, page)
			// 必须检查错误：这里原先写的是 `_ = os.WriteFile(...)`，
			// 于是传了 Git Bash 风格的 /tmp/xxx 时（Go 是 Windows 程序，
			// 会解析成 D:\tmp\xxx）落盘失败也一声不吭，人还以为拿到了原始报文。
			// 静默失败比报错危险得多——尤其这个工具的用途就是"别信记忆，去看原始数据"。
			if err := os.WriteFile(name, raw, 0o644); err != nil {
				fmt.Printf("原始响应落盘失败 %s: %v\n", name, err)
			} else {
				fmt.Println("原始响应已写入", name, len(raw), "字节")
			}
		}

		var top struct {
			Data struct {
				Filter     string           `json:"filter"`
				Rows       []map[string]any `json:"rows"`
				TotalCount int64            `json:"totalCount"`
				LastPage   bool             `json:"lastPage"`
			} `json:"data"`
			ErrorCode string `json:"errorCode"`
			Message   string `json:"message"`
		}
		if err := decode(raw, &top); err != nil {
			return false, fmt.Errorf("decode query: %w", err)
		}
		if top.ErrorCode != "" && top.ErrorCode != "0" {
			return false, fmt.Errorf("接口报错: %s %s", top.ErrorCode, top.Message)
		}
		// 过滤条件每页都一样，取第一页的即可
		if page == 1 {
			filterEcho = top.Data.Filter
			totalCount = top.Data.TotalCount
		}
		fmt.Printf("page %d: %d 行 / totalCount=%d / lastPage=%v\n",
			page, len(top.Data.Rows), top.Data.TotalCount, top.Data.LastPage)
		rows = append(rows, top.Data.Rows...)
		if top.Data.LastPage || len(top.Data.Rows) == 0 {
			break
		}
		if page > 1000 {
			return false, fmt.Errorf("分页超过安全上限，可能陷入死循环")
		}
	}
	fmt.Printf("扫描时间 %s，合计 %d 行（totalCount=%d）\n",
		time.Now().Format("2006-01-02 15:04:05"), len(rows), totalCount)
	if filterEcho != "" {
		fmt.Printf("服务端生效过滤条件（响应回显）: %s\n", filterEcho)
	}

	// 本次扫描的指纹：只记「字段在不在、有没有非零值」+「服务端过滤条件」，供下次扫描比对。
	// 为什么要这个：星瀚改投影/改口径都是静默的，而人的记忆不可靠——
	// 2026-09-17 就是拿 19:18 的快照去判断 20:07 的接口，白白绕了一圈。
	fp := &fingerprint{
		ScannedAt: time.Now().Format("2006-01-02 15:04:05"),
		Filter:    filterEcho,
		Rows:      len(rows),
		Fields:    map[string]fieldFP{},
		Nested:    map[string]map[string]fieldFP{},
	}

	// 0 行时下面所有字段统计都是空壳，打印出来只会让人误以为"投影被清空了"。
	// 直接跳过，把结论交给第 8 步的比对。
	if len(rows) == 0 {
		fmt.Println("\n⚠ 接口返回 0 行，字段清单无从采集。")
		if filterEcho != "" {
			fmt.Println("  最可能的原因就是上面那条服务端过滤条件——请求体里的参数覆盖不了它。")
		}
		fmt.Println("  请让星瀚侧确认过滤条件，或为本应用开通 bos_user/query 权限。")
	} else {
		reportStructure(rows, fp, code)
	}

	// ---- 8) 与上次扫描对比 ----
	//
	// 为什么这一步比上面所有步骤都重要：上面打印的是「此刻长什么样」，
	// 而排查「字段取不到值」真正需要的是「跟上一次比，什么变了」。
	// 星瀚改投影不通知，唯一能抓住它的手段就是留下快照、下次比对。
	if src.baseline == "" {
		return false, nil
	}
	old, err := loadFingerprint(src.baseline)
	switch {
	case err == nil:
		important = reportDiff(old, fp, verbose)
		if updateBaseline {
			if err := saveFingerprint(src.baseline, fp); err != nil {
				fmt.Printf("\n基线写入失败: %v\n", err)
			} else {
				fmt.Printf("\n基线已更新: %s\n", src.baseline)
			}
		} else {
			fmt.Printf("\n（基线未改动；确认变化无误后用 -update 更新）\n")
		}
	case os.IsNotExist(err):
		if err := saveFingerprint(src.baseline, fp); err != nil {
			fmt.Printf("\n基线写入失败: %v\n", err)
		} else {
			fmt.Printf("\n首次扫描，已建立基线 %s（顶层字段 %d 个 / 嵌套结构 %d 个）\n",
				src.baseline, len(fp.Fields), len(fp.Nested))
		}
	default:
		// 基线存在但读不动（被改坏了 / 不是 JSON）——不要静默当成"首次运行"，
		// 否则会覆盖掉一份本来有价值的快照。
		fmt.Printf("\n基线 %s 读取失败: %v\n  用 -update 重新生成，或 -baseline-dir=\"\" 跳过对比\n",
			src.baseline, err)
	}
	return important, nil
}

// reportStructure 打印字段清单、嵌套明细、重复业务键、数值字面量，并填充指纹。
func reportStructure(rows []map[string]any, fp *fingerprint, code string) {
	type stat struct {
		kind     string
		kindSet  bool // 类型是否已由「第一个非 null 值」确定
		present  int
		nonEmpty int
		nonZero  int
		distinct map[string]bool
		sample   string
	}
	stats := map[string]*stat{}
	var order []string
	keyCountDist := map[int]int{}

	for _, r := range rows {
		keyCountDist[len(r)]++
		for k, v := range r {
			s, ok := stats[k]
			if !ok {
				s = &stat{kind: "scalar", distinct: map[string]bool{}}
				stats[k] = s
				order = append(order, k)
			}
			// 类型必须由「第一个非 null 值」决定，不能只看第一行。
			// 反例就在眼前：人员接口第一行（administrator）的 entryentity 是 null，
			// 若按第一行判定，entryentity 会被记成 scalar，
			// 于是 nestedKeys 里没有它，整个嵌套结构（27 个字段里唯一的嵌套）被静默漏掉。
			if !s.kindSet && v != nil {
				switch v.(type) {
				case []any:
					s.kind = "array"
				case map[string]any:
					s.kind = "object"
				default:
					s.kind = "scalar"
				}
				s.kindSet = true
			}
			s.present++
			if !isEmpty(v) {
				s.nonEmpty++
				if s.sample == "" {
					b, _ := json.Marshal(v)
					s.sample = string(b)
					if len(s.sample) > 40 {
						s.sample = s.sample[:40] + "…"
					}
				}
			}
			if !isZero(v) {
				s.nonZero++
			}
			s.distinct[fmt.Sprintf("%v", v)] = true
		}
	}

	fmt.Printf("\n=== 字段数分布（每行几个字段:行数）===\n")
	kc := make([]int, 0, len(keyCountDist))
	for k := range keyCountDist {
		kc = append(kc, k)
	}
	sort.Ints(kc)
	for _, k := range kc {
		fmt.Printf("  %d 个字段: %d 行\n", k, keyCountDist[k])
	}

	sort.Strings(order)
	fmt.Printf("\n=== 顶层字段（共 %d 个）===\n", len(stats))
	fmt.Printf("%-30s %-7s %6s %6s %6s %7s  %s\n", "字段名", "类型", "出现", "非空", "非零", "取值数", "样例")
	for _, k := range order {
		s := stats[k]
		fp.Fields[k] = fieldFP{Kind: s.kind, Present: s.present, NonZero: s.nonZero}
		fmt.Printf("%-30s %-7s %6d %6d %6d %7d  %s\n",
			k, s.kind, s.present, s.nonEmpty, s.nonZero, len(s.distinct), s.sample)
	}

	// ---- 嵌套结构展开 ----
	fmt.Println("\n=== 嵌套结构明细 ===")
	var nestedKeys []string
	for _, k := range order {
		if stats[k].kind == "array" || stats[k].kind == "object" {
			nestedKeys = append(nestedKeys, k)
		}
	}
	if len(nestedKeys) == 0 {
		fmt.Println("  （没有任何嵌套结构）")
	}
	for _, nk := range nestedKeys {
		type sub struct {
			kind             string
			kindSet          bool
			present, nonZero int
			sample           string
			distinct         map[string]bool
		}
		subs := map[string]*sub{}
		var subOrder []string
		rowsWithArray, rowsWithNull, rowsWithEmpty := 0, 0, 0
		maxLen := 0

		for _, r := range rows {
			v, ok := r[nk]
			if !ok || v == nil {
				rowsWithNull++
				continue
			}
			// 数组和对象都当作"若干条明细"处理：嵌套字段既有 array 也有 object，
			// 只认 array 会让 object 型嵌套在统计里凭空消失（既不计数也不展开）。
			var elems []any
			switch t := v.(type) {
			case []any:
				elems = t
			case map[string]any:
				elems = []any{t}
			default:
				continue
			}
			if len(elems) == 0 {
				rowsWithEmpty++
				continue
			}
			rowsWithArray++
			if len(elems) > maxLen {
				maxLen = len(elems)
			}
			for _, e := range elems {
				em, _ := e.(map[string]any)
				for k, val := range em {
					s, ok := subs[k]
					if !ok {
						s = &sub{kind: "scalar", distinct: map[string]bool{}}
						subs[k] = s
						subOrder = append(subOrder, k)
					}
					// 同顶层：类型由第一个非 null 值决定，否则明细行里先出现 null
					// 会把一个子表字段错记成 scalar。
					if !s.kindSet && val != nil {
						switch val.(type) {
						case []any:
							s.kind = "array"
						case map[string]any:
							s.kind = "object"
						default:
							s.kind = "scalar"
						}
						s.kindSet = true
					}
					s.present++
					if !isZero(val) {
						s.nonZero++
					}
					s.distinct[fmt.Sprintf("%v", val)] = true
					if s.sample == "" && val != nil {
						b, _ := json.Marshal(val)
						s.sample = string(b)
					}
				}
			}
		}

		fmt.Printf("\n-- %s --\n", nk)
		fmt.Printf("  有值 %d 行 / 空数组 %d 行 / null 或缺失 %d 行 / 单行最多 %d 条\n",
			rowsWithArray, rowsWithEmpty, rowsWithNull, maxLen)
		fmt.Printf("  %-36s %6s %6s %7s  %s\n", "子字段", "出现", "非零", "取值数", "样例")
		sort.Strings(subOrder)
		subFP := map[string]fieldFP{}
		for _, k := range subOrder {
			s := subs[k]
			subFP[k] = fieldFP{Kind: s.kind, Present: s.present, NonZero: s.nonZero}
			fmt.Printf("  %-36s %6d %6d %7d  %s\n", k, s.present, s.nonZero, len(s.distinct), s.sample)
		}
		fp.Nested[nk] = subFP
	}

	// ---- 业务键重复分析 ----
	const bizKey = "number"
	counter := map[string]int{}
	ids := map[string][]string{}
	for _, r := range rows {
		k := fmt.Sprintf("%v", r[bizKey])
		counter[k]++
		ids[k] = append(ids[k], fmt.Sprintf("%v", r["id"]))
	}
	var dup []string
	for k, v := range counter {
		if v > 1 {
			dup = append(dup, k)
		}
	}
	sort.Strings(dup)
	fmt.Printf("\n=== 业务键 %s 重复分析 ===\n", bizKey)
	fmt.Printf("  总行数 %d / 去重后 %d / 重复键 %d 个\n", len(rows), len(counter), len(dup))
	for _, k := range dup {
		fmt.Printf("    %s × %d  ids=%v\n", k, counter[k], ids[k])
	}
	fp.UniqueCodes = len(counter)

	// ---- 数值字段的原始字面量种类 ----
	fmt.Println("\n=== 数值字段的原始字面量种类（看精度）===")
	numRe := map[string]map[string]bool{}
	for _, r := range rows {
		for k, v := range r {
			if n, ok := v.(json.Number); ok {
				if numRe[k] == nil {
					numRe[k] = map[string]bool{}
				}
				numRe[k][n.String()] = true
			}
		}
	}
	nk := make([]string, 0, len(numRe))
	for k := range numRe {
		nk = append(nk, k)
	}
	sort.Strings(nk)
	for _, k := range nk {
		vals := make([]string, 0, len(numRe[k]))
		for v := range numRe[k] {
			vals = append(vals, v)
		}
		sort.Strings(vals)
		if len(vals) > 6 {
			fmt.Printf("  %-32s %3d 种，前 6: %v …\n", k, len(vals), vals[:6])
		} else {
			fmt.Printf("  %-32s %3d 种: %v\n", k, len(vals), vals)
		}
	}

	// ---- 目标行原文 ----
	fmt.Printf("\n=== 目标行 %s 的原始数据 ===\n", code)
	found := false
	for i, r := range rows {
		if fmt.Sprintf("%v", r[bizKey]) != code {
			continue
		}
		found = true
		b, _ := json.Marshal(r)
		fmt.Printf("  [第 %d 行] id=%v bizstatus=%v billstatus=%v\n    %s\n",
			i+1, r["id"], r["bizstatus"], r["billstatus"], string(b))
	}
	if !found {
		fmt.Println("  （未找到）")
	}
}

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件")
	target := flag.String("target", "asset", "扫描目标：asset | personnel | dept | all")
	rawPrefix := flag.String("raw", "", "原始响应落盘前缀（每页一个文件），留空则不落盘")
	code := flag.String("code", "12020302000003", "重点展开的业务编码（资产编码 / 人员工号 / 组织编码）")
	baselineDir := flag.String("baseline-dir", "docs", "指纹基线目录；文件不存在时自动创建")
	baselineOverride := flag.String("baseline", "", "显式指定基线文件（仅单目标扫描时有效）")
	updateBaseline := flag.Bool("update", false, "用本次扫描覆盖基线（默认只对比不写）")
	verbose := flag.Bool("v", false, "连仅有计数变化的字段一起打印")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Println("load config:", err)
		os.Exit(1)
	}
	srcs, err := resolveSources(cfg, *target, *baselineDir, *baselineOverride)
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}

	// ---- token（两个接口共用）----
	raw, err := post(cfg.Kingdee.BaseURL+"/oauth2/getToken", map[string]any{
		"client_id":     cfg.Kingdee.ClientID,
		"client_secret": cfg.Kingdee.ClientSecret,
		"username":      cfg.Kingdee.Username,
		"accountId":     cfg.Kingdee.AccountID,
		"nonce":         fmt.Sprintf("%d", time.Now().UnixNano()),
		"timestamp":     time.Now().Format("2006-01-02 15:04:05"),
		"language":      "zh_CN",
	}, nil)
	if err != nil {
		fmt.Println("token request:", err)
		os.Exit(1)
	}
	var tokResp struct {
		Data struct {
			// 注意是 access_token（下划线）：token 接口和其他接口的命名风格不一致，
			// 按 accessToken 取会拿到空串，然后所有后续请求都变成未授权。
			AccessToken string `json:"access_token"`
		} `json:"data"`
		ErrorCode string `json:"errorCode"`
		Message   string `json:"message"`
	}
	if err := decode(raw, &tokResp); err != nil || tokResp.Data.AccessToken == "" {
		fmt.Printf("token 失败: %s %s\n", tokResp.ErrorCode, tokResp.Message)
		os.Exit(1)
	}
	fmt.Println("token OK")

	pageSize := cfg.Kingdee.PageSize
	if pageSize <= 0 {
		pageSize = 200
	}
	anyImportant := false
	for _, src := range srcs {
		imp, err := scanSource(cfg, src, tokResp.Data.AccessToken, *rawPrefix, *code, pageSize,
			*verbose, *updateBaseline)
		if err != nil {
			fmt.Printf("\n%s 扫描失败: %v\n", src.label, err)
			os.Exit(3)
		}
		anyImportant = anyImportant || imp
	}

	// 退出码带上结论，定时任务/CI 里能直接靠退出码报警，不用去 grep 输出文本：
	//
	//	0  扫描成功，无重要变化
	//	1  扫描成功，但检测到重要变化（投影增删 / 值域翻转 / 过滤条件变更）→ 需人工确认
	//	2  参数错误
	//	3  扫描失败（网络、鉴权、接口报错）
	//
	// 1 和 3 分开是有意的：1 是"接口变了，人来看看"，3 是"这次没扫成，结果不可信"。
	// 混在一个码里，运维会分不清该看报告还是该查网络。
	if anyImportant {
		os.Exit(1)
	}
}
