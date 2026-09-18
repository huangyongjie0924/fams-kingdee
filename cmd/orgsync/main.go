package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sort"
	"time"

	"asset-mgr/config"
	"asset-mgr/integration/kingdee"
	"asset-mgr/store"
	"asset-mgr/syncer"
)

// 组织主数据（公司 / 部门 / 员工）从星瀚同步的入口。
//
// 默认 dry-run：只算不写。要真正落库必须显式 -dry-run=false。
// 这个默认值是有意的——组织主数据是台账里所有「使用部门」「使用人」的来源，
// 写错了会连带影响资产列表、权限范围和盘点，比同步几张卡严重得多。
func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件路径")
	dryRun := flag.Bool("dry-run", true, "只算不写库（默认开启）")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	cfg.Sync.DryRun = *dryRun

	st, err := store.New(cfg.DSN())
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer st.Close()

	client, err := kingdee.NewClient(cfg.KingdeeClientConfig())
	if err != nil {
		log.Fatalf("创建金蝶客户端失败: %v", err)
	}

	svc := syncer.NewService(st, client, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	fmt.Printf("同步范围（longnumber 前缀）:\n")
	for _, r := range syncer.OrgScopeRoots {
		fmt.Printf("  %s\n", r)
	}
	fmt.Println()

	trigger := "manual"
	if *dryRun {
		trigger = "dryrun"
	}
	log.Printf("拉取星瀚部门与人员（dry_run=%v）...", *dryRun)

	res, err := svc.SyncOrg(ctx, trigger)
	if err != nil {
		log.Fatalf("组织同步失败: %v", err)
	}

	p := res.Plan
	fmt.Println()
	fmt.Println("=========== 源数据 ===========")
	fmt.Printf("部门接口: %d 行（服务端过滤 %s），范围内 %d 个节点\n", p.DeptRows, p.DeptFilter, p.DeptInScope)
	fmt.Printf("人员接口: %d 行（服务端过滤 %s），范围内 %d 人\n", p.PeopleRows, p.PeopleFilter, p.PeopleInScope)

	fmt.Println()
	fmt.Println("=========== 落库计划 ===========")
	fmt.Printf("公司 %d 家 / 部门 %d 个 / 员工 %d 人\n", len(p.Companies), len(p.Departments), len(p.Employees))

	fmt.Println()
	fmt.Println("--- 公司 ---")
	for _, c := range p.Companies {
		fmt.Printf("  %-8s %-32s 简称=%-10s L%d\n", c.Code, c.Name, c.ShortName, c.Level)
	}

	fmt.Println()
	fmt.Println("--- 部门（前 12 个）---")
	for i, d := range p.Departments {
		if i >= 12 {
			fmt.Printf("  ... 其余 %d 个\n", len(p.Departments)-12)
			break
		}
		fmt.Printf("  %-12s %-24s 归属=%s L%d 父=%-10s %s\n",
			d.Code, d.Name, d.CompanyCode, d.Level, orDash(d.ParentCode), d.LongNumber)
	}

	// 部门按归属公司分布
	byComp := map[string]int{}
	for _, d := range p.Departments {
		byComp[d.CompanyCode]++
	}
	fmt.Printf("  按归属公司: ")
	keys := make([]string, 0, len(byComp))
	for k := range byComp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s=%d  ", k, byComp[k])
	}
	fmt.Println()

	fmt.Println()
	fmt.Println("--- 员工（前 12 个）---")
	for i, e := range p.Employees {
		if i >= 12 {
			fmt.Printf("  ... 其余 %d 人\n", len(p.Employees)-12)
			break
		}
		fmt.Printf("  %-12s %-10s 主部门=%-12s 归属=%-5s 在职=%-5v 候选=%v\n",
			e.EmpNo, e.Name, orDash(e.DeptCode), orDash(e.CompanyCode), e.Active, e.DeptCodes)
	}

	// 一人多部门的分布：这是台账 employee.dept_id 单值化必须交代清楚的
	multi := 0
	for _, e := range p.Employees {
		if len(e.DeptCodes) > 1 {
			multi++
		}
	}
	fmt.Printf("  其中挂多个部门的 %d 人（主部门取 1201 优先，全部候选见上面「候选」列）\n", multi)

	fmt.Println()
	fmt.Println("=========== 落库结果 ===========")
	if res.DryRun {
		fmt.Printf("dry-run：会新建 %d 行 / 会更新 %d 行（未写库）\n", res.Created, res.Updated)
	} else {
		fmt.Printf("已写入：新建 %d 行 / 更新 %d 行\n", res.Created, res.Updated)
	}

	fmt.Printf("登记的名称数（冲突检查用）: %v\n", res.ResolvedNames())
	if res.ConflictErr != nil {
		fmt.Printf("⚠ 同名冲突检查未跑成: %v\n", res.ConflictErr)
	} else if len(res.Conflicts) > 0 {
		fmt.Println()
		fmt.Printf("⚠ 同名冲突 %d 处 —— 本地手工数据与星瀚同名但不是同一行，同步不按名称匹配所以并存，需人工决定去留：\n", len(res.Conflicts))
		for _, c := range res.Conflicts {
			fmt.Printf("  [%s] %s：手工行 id=%v ↔ 同步行 id=%v\n", c.Kind, c.Name, c.ManualIDs, c.SyncedIDs)
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
