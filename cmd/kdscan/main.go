// kdscan 全量扫描星瀚资产卡接口的返回字段，用于诊断「字段取不到值 / 对方改了投影」。
//
// 为什么把它留成常驻命令：星瀚侧会改接口的投影列，而且是**静默改**——
// 2026-09-17 当天 19:18 扫到的 finentry 只有 2 个键（且全库恒为 0），20:07 再扫变成 39 个键、
// 真实取值挂在 originalfincard_* 前缀上。所以「字段取不到值」的第一反应应该是重扫，
// 而不是翻旧结论。有这条命令，重扫就是一行：
//
//	go run ./cmd/kdscan -config config.yaml
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

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件")
	rawPrefix := flag.String("raw", "", "原始响应落盘前缀（每页一个文件），留空则不落盘")
	target := flag.String("code", "12020302000003", "重点展开的资产编码")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Println("load config:", err)
		os.Exit(1)
	}
	base := cfg.Kingdee.BaseURL

	// ---- 1) token ----
	raw, err := post(base+"/oauth2/getToken", map[string]any{
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

	// ---- 2) 分页拉全量 ----
	var rows []map[string]any
	for page := 1; ; page++ {
		raw, err = post(base+cfg.Kingdee.QueryPath, map[string]any{
			"data":     map[string]any{"assetname": "1", "billno": "1", "filter": ""},
			"pageNo":   page,
			"pageSize": 200,
		}, map[string]string{
			"accessToken":     tokResp.Data.AccessToken,
			"Idempotency-Key": fmt.Sprintf("%d", time.Now().UnixNano()),
		})
		if err != nil {
			fmt.Println("query request:", err)
			os.Exit(1)
		}
		if *rawPrefix != "" {
			name := fmt.Sprintf("%s.page%d", *rawPrefix, page)
			_ = os.WriteFile(name, raw, 0o644)
			fmt.Println("原始响应已写入", name, len(raw), "字节")
		}

		var top struct {
			Data struct {
				Rows       []map[string]any `json:"rows"`
				TotalCount int64            `json:"totalCount"`
				LastPage   bool             `json:"lastPage"`
			} `json:"data"`
			ErrorCode string `json:"errorCode"`
			Message   string `json:"message"`
		}
		if err := decode(raw, &top); err != nil {
			fmt.Println("decode query:", err)
			os.Exit(1)
		}
		if top.ErrorCode != "" && top.ErrorCode != "0" {
			fmt.Printf("接口报错: %s %s\n", top.ErrorCode, top.Message)
			os.Exit(1)
		}
		fmt.Printf("page %d: %d 行 / totalCount=%d / lastPage=%v\n",
			page, len(top.Data.Rows), top.Data.TotalCount, top.Data.LastPage)
		rows = append(rows, top.Data.Rows...)
		if top.Data.LastPage || len(top.Data.Rows) == 0 {
			break
		}
	}
	fmt.Printf("扫描时间 %s，合计 %d 行\n", time.Now().Format("2006-01-02 15:04:05"), len(rows))

	// ---- 3) 顶层字段清单 ----
	type stat struct {
		kind     string
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
				kind := "scalar"
				switch v.(type) {
				case []any:
					kind = "array"
				case map[string]any:
					kind = "object"
				}
				s = &stat{kind: kind, distinct: map[string]bool{}}
				stats[k] = s
				order = append(order, k)
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
		fmt.Printf("%-30s %-7s %6d %6d %6d %7d  %s\n",
			k, s.kind, s.present, s.nonEmpty, s.nonZero, len(s.distinct), s.sample)
	}

	// ---- 4) 嵌套结构展开 ----
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
			arr, isArr := v.([]any)
			if !isArr {
				continue
			}
			if len(arr) == 0 {
				rowsWithEmpty++
				continue
			}
			rowsWithArray++
			if len(arr) > maxLen {
				maxLen = len(arr)
			}
			for _, e := range arr {
				em, _ := e.(map[string]any)
				for k, val := range em {
					s, ok := subs[k]
					if !ok {
						s = &sub{distinct: map[string]bool{}}
						subs[k] = s
						subOrder = append(subOrder, k)
					}
					s.present++
					if !isZero(val) {
						s.nonZero++
					}
					s.distinct[fmt.Sprintf("%v", val)] = true
					if s.sample == "" {
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
		for _, k := range subOrder {
			s := subs[k]
			fmt.Printf("  %-36s %6d %6d %7d  %s\n", k, s.present, s.nonZero, len(s.distinct), s.sample)
		}
	}

	// ---- 5) 业务键重复分析 ----
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

	// ---- 6) 数值字段的原始字面量种类 ----
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

	// ---- 7) 目标卡原文 ----
	fmt.Printf("\n=== 目标卡 %s 的原始行 ===\n", *target)
	found := false
	for i, r := range rows {
		if fmt.Sprintf("%v", r[bizKey]) != *target {
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
