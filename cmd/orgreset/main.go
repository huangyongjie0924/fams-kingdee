// orgreset 重建组织主数据：清空「公司 / 部门 / 员工」及其外部映射，
// 再从星瀚全量重建，最后重挂资产卡上的引用。
//
// ⚠ 为什么必须连 external_master_map 一起清
//
// 映射表存的是「星瀚编码 → 本地行 ID」。只删三张表、不清映射的话，
// 组织同步查映射会拿到**已经被删掉的 ID**，把它当成"已存在的行"去做 UPDATE——
// UPDATE 影响 0 行、也不报错，那一行就再也不会被创建出来。
//
// 在库副本上实测（删表但不清理映射）：
//
//	公司 0/2、部门 59/88、员工 71/136
//	202 张卡的使用部门、202 张卡的使用人、204 张卡的权属公司全部断链
//	59 个部门的 company_id 全部指向不存在的公司
//
// 这不是"可能出问题"，是必然。所以清理映射是这一步的一部分，不是可选项。
//
// 顺序也是固定的：组织主数据 → 资产卡。资产卡同步会把卡片上的
// use_dept_id / user_emp_id / owner_company_id 按编码重新解析并写回，
// 这是重建后引用能接上的唯一途径（组织同步本身不碰 asset_card）。
//
// 默认 dry-run，只打印将要做什么。真跑需要 -dry-run=false 并且
// -confirm 与配置里的库名完全一致——防止在错误的库上执行。
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"asset-mgr/config"
	"asset-mgr/integration/kingdee"
	"asset-mgr/model"
	"asset-mgr/store"
	"asset-mgr/syncer"
)

// purgeTables 是要清空的主数据表，顺序即删除顺序（无外键约束，顺序只为可读）。
var purgeTables = []string{"department", "employee", "company"}

// mapKinds 是 external_master_map 里与组织主数据相关的 kind。
// 不碰 area / category / vendor 等其它 kind：那些不是组织主数据，
// 清掉只会让下一次资产卡同步多做一遍无谓的解析。
var mapKinds = []string{model.MasterKindCompany, model.MasterKindDepartment, model.MasterKindEmployee}

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件路径")
	dryRun := flag.Bool("dry-run", true, "只打印将要做什么，不写库（默认开启）")
	confirm := flag.String("confirm", "", "真跑时必须填写配置里的库名，用于防止误连库")
	backupDir := flag.String("backup-dir", "", "备份目录，留空则用当前目录下的 orgreset-backup")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	// 库名比对是最后一道闸：这个工具会删掉全部部门与员工，
	// 连错库的代价比多打一个参数大得多。
	if !*dryRun {
		if err := checkConfirm(*confirm, cfg.MySQL.Database); err != nil {
			log.Fatalf("%v", err)
		}
	}

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	fmt.Printf("目标库: %s@%s:%d/%s\n", cfg.MySQL.User, cfg.MySQL.Host, cfg.MySQL.Port, cfg.MySQL.Database)
	fmt.Printf("模式: %s\n\n", modeLabel(*dryRun))

	// ---- 第 0 步：预检。删之前必须先确认源能读到东西 ----
	// 否则一次接口故障就会把主数据删干净而重建不出来。
	if err := precheckSource(ctx, svc); err != nil {
		log.Fatalf("预检失败，未做任何改动: %v", err)
	}

	// ---- 第 1 步：备份 ----
	// 备份写在清理之前，且必须成功；写不成就不要往下走。
	//
	// 先抓一份「账号 → 工号」的对照，再写进备份文件：
	// 员工行会被删掉重建、ID 全变，sys_user.employee_id 因此断链。
	// emp_no 是唯一的业务键，按它重绑是确定性的，不需要猜。
	// 对照也落盘一份，万一进程在中途被杀，还能照着手工恢复。
	bindings, err := sysUserBindings(st.DB())
	if err != nil {
		log.Fatalf("读取账号绑定失败: %v", err)
	}
	backupPath, err := writeBackup(st.DB(), *backupDir, bindings)
	if err != nil {
		log.Fatalf("备份失败，未做任何改动: %v", err)
	}
	if *dryRun {
		// dry-run 既然承诺「不写库」，就不该留下备份文件——反复 dry-run
		// 会堆一堆 100KB 的垃圾，还容易让人误以为"已经备份过了"。
		//
		// 但备份能不能写成功必须在这里验证过：真跑时它失败就等于中止，
		// 而"会不会失败"正是 dry-run 该回答的问题。所以试写 → 量大小 → 删掉。
		fi, statErr := os.Stat(backupPath)
		if rmErr := os.Remove(backupPath); rmErr != nil {
			log.Fatalf("备份试写后清理失败: %v", rmErr)
		}
		if statErr != nil {
			log.Fatalf("备份试写校验失败: %v", statErr)
		}
		fmt.Printf("\n[1/5] 备份试写成功（%d 字节，含 %d 条账号绑定对照），dry-run 已删除\n",
			fi.Size(), len(bindings))
	} else {
		fmt.Printf("\n[1/5] 备份已写入 %s（含 %d 条账号绑定对照）\n", backupPath, len(bindings))
	}

	// ---- 第 2 步：清理主数据与映射 ----
	before, err := countOrgRows(st.DB())
	if err != nil {
		log.Fatalf("统计清理前数量失败: %v", err)
	}
	fmt.Printf("[2/5] 清理前: 公司 %d / 部门 %d / 员工 %d / 相关映射 %d\n",
		before.Company, before.Department, before.Employee, before.MapRows)

	if *dryRun {
		fmt.Println("      dry-run：跳过清理与写入。去掉 -dry-run=false 才会真跑。")
		fmt.Println("\n[3/5] 组织同步    （dry-run 跳过）")
		fmt.Println("[4/5] 资产卡同步  （dry-run 跳过）")
		if len(bindings) > 0 {
			fmt.Printf("      账号重绑    （dry-run 跳过，真跑时会按工号重绑 %d 个账号）\n", len(bindings))
		}
		// 体检在 dry-run 下也跑：它只读，而且"重建前长什么样"正是做决定要看的东西。
		// 光看"会删 20 个部门"没有体感，看到"202 张可见卡里 2 张引用会断"才有。
		fmt.Println("\n[5/5] 体检（当前状态，只读）")
		if cur, err := inspect(st.DB()); err != nil {
			fmt.Printf("      体检失败: %v\n", err)
		} else {
			cur.print()
		}
		return
	}

	purged, err := purgeOrgMasters(st.DB())
	if err != nil {
		log.Fatalf("清理失败: %v", err)
	}
	fmt.Printf("      已删除: 公司 %d / 部门 %d / 员工 %d / 映射 %d\n",
		purged.Company, purged.Department, purged.Employee, purged.MapRows)

	// ---- 第 3 步：组织主数据全量重建 ----
	fmt.Println("\n[3/5] 组织同步（公司 / 部门 / 员工）...")
	orgRes, err := svc.SyncOrg(ctx, "orgreset")
	if err != nil {
		log.Fatalf("组织同步失败，库已清空，请用 %s 恢复: %v", backupPath, err)
	}
	fmt.Printf("      新建 %d / 更新 %d（公司 %d / 部门 %d / 员工 %d）\n",
		orgRes.Created, orgRes.Updated,
		len(orgRes.Plan.Companies), len(orgRes.Plan.Departments), len(orgRes.Plan.Employees))

	// ---- 第 4 步：资产卡全量同步，重挂引用 ----
	// 这一步是引用能接上的唯一途径：组织同步只写主数据表，不碰 asset_card。
	//
	// 必须是全量：增量按修改时间取，没被改动过的卡片会被跳过，
	// 它们身上断掉的引用就永远接不上。
	fmt.Println("\n[4/5] 资产卡同步（重挂 use_dept_id / user_emp_id / owner_company_id）...")
	run, err := svc.Run(ctx, model.SyncModeFull, "orgreset")
	if err != nil {
		log.Fatalf("资产卡同步失败，主数据已重建但卡片引用未接上，请重跑本命令的第 4 步（勿再清理）: %v", err)
	}
	fmt.Printf("      资产卡 id=%d status=%s total=%d updated=%d skipped=%d failed=%d\n",
		run.ID, run.Status, run.TotalCount, run.UpdatedCount, run.SkippedCount, run.FailedCount)

	// ---- 第 4b 步：把账号按工号重绑回新的员工行 ----
	// 不做这一步的话，所有绑了员工的账号都会指向不存在的 ID——
	// 这是本工具自己造成的回归，不该留给人工收尾。
	rebound, unmatched, err := rebindSysUsers(st.DB(), bindings)
	if err != nil {
		log.Fatalf("账号重绑失败，请按 %s 里的 UPDATE 语句手工恢复: %v", backupPath, err)
	}
	fmt.Printf("      账号重绑: 成功 %d / 对不上 %d（按工号匹配）\n", rebound, unmatched)
	if unmatched > 0 {
		for _, b := range bindings {
			if b.EmpNo == "" {
				continue
			}
			if ok, err := employeeExistsByNo(st.DB(), b.EmpNo); err == nil && !ok {
				fmt.Printf("        - 账号 %s 原绑工号 %s，星瀚范围内没有这个工号，需人工指定\n", b.Username, b.EmpNo)
			}
		}
	}

	// ---- 第 5 步：体检 ----
	fmt.Println("\n[5/5] 体检")
	report, err := inspect(st.DB())
	if err != nil {
		log.Fatalf("体检失败: %v", err)
	}
	report.print()
}

// checkConfirm 校验 -confirm 与目标库名一致。抽出来是为了能单测：
// 这道闸一旦失效，工具就会在没人确认的情况下删库。
func checkConfirm(confirm, database string) error {
	if strings.TrimSpace(confirm) == "" {
		return fmt.Errorf("真跑需要显式确认：加 -confirm=%s（或先去掉 -dry-run=false 看一遍计划）", database)
	}
	if strings.TrimSpace(confirm) != database {
		return fmt.Errorf("确认值 %q 与配置里的库名 %q 不一致，拒绝执行", confirm, database)
	}
	return nil
}

func modeLabel(dryRun bool) string {
	if dryRun {
		return "dry-run（只打印，不写库）"
	}
	return "真跑（会删除并重建）"
}

// precheckSource 只读地拉一次星瀚，确认范围内的节点与人都不为 0。
// 五处 0 行护栏在 SyncOrg 内部，这里再挡一道是为了"删之前"就拦住。
func precheckSource(ctx context.Context, svc *syncer.Service) error {
	res, err := svc.PlanOrg(ctx)
	if err != nil {
		return fmt.Errorf("拉取星瀚组织数据失败: %w", err)
	}
	p := res.Plan
	fmt.Printf("预检: 部门接口 %d 行（范围内 %d 节点）/ 人员接口 %d 行（范围内 %d 人）\n",
		p.DeptRows, p.DeptInScope, p.PeopleRows, p.PeopleInScope)
	if p.DeptInScope == 0 || p.PeopleInScope == 0 {
		return fmt.Errorf("范围内部门 %d 个、人员 %d 人，源数据不完整，拒绝清理",
			p.DeptInScope, p.PeopleInScope)
	}
	return nil
}

type orgCounts struct {
	Company, Department, Employee, MapRows int
}

func countOrgRows(db *sql.DB) (*orgCounts, error) {
	c := &orgCounts{}
	pairs := []struct {
		dst   *int
		query string
		args  []any
	}{
		{&c.Company, "SELECT COUNT(*) FROM company", nil},
		{&c.Department, "SELECT COUNT(*) FROM department", nil},
		{&c.Employee, "SELECT COUNT(*) FROM employee", nil},
		{&c.MapRows, "SELECT COUNT(*) FROM external_master_map WHERE kind IN (?,?,?)",
			[]any{mapKinds[0], mapKinds[1], mapKinds[2]}},
	}
	for _, p := range pairs {
		if err := db.QueryRow(p.query, p.args...).Scan(p.dst); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// purgeOrgMasters 在一个事务里清映射 + 清三张表，返回删除行数。
//
// 映射必须在表之前删：反过来的话，中途失败会留下"表已空、映射还在"的
// 最坏状态——正是这个工具要避免的那个坑。
func purgeOrgMasters(db *sql.DB) (*orgCounts, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	out := &orgCounts{}

	res, err := tx.Exec(`DELETE FROM external_master_map WHERE kind IN (?,?,?)`,
		mapKinds[0], mapKinds[1], mapKinds[2])
	if err != nil {
		return nil, fmt.Errorf("清 external_master_map: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil {
		out.MapRows = int(n)
	}

	for _, t := range purgeTables {
		res, err := tx.Exec("DELETE FROM " + t)
		if err != nil {
			return nil, fmt.Errorf("清 %s: %w", t, err)
		}
		n, _ := res.RowsAffected()
		switch t {
		case "company":
			out.Company = int(n)
		case "department":
			out.Department = int(n)
		case "employee":
			out.Employee = int(n)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// sysUserBinding 是「账号 → 原绑工号」的一条对照。
type sysUserBinding struct {
	UserID   int64
	Username string
	EmpNo    string
}

// sysUserBindings 抓取所有已绑定员工的账号及其工号。
// 必须在清理之前调用：清理之后 employee 行就换了 ID，再也查不出原来的对应关系。
func sysUserBindings(db *sql.DB) ([]sysUserBinding, error) {
	rows, err := db.Query(`SELECT u.id, u.username, e.emp_no
		FROM sys_user u JOIN employee e ON e.id = u.employee_id
		WHERE u.employee_id > 0 ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sysUserBinding
	for rows.Next() {
		var b sysUserBinding
		if err := rows.Scan(&b.UserID, &b.Username, &b.EmpNo); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// rebindSysUsers 按工号把账号重新绑到重建后的员工行。
//
// 按 emp_no 匹配而不是按姓名：姓名会重名（星瀚里一个部门名能挂 78 个节点，
// 人名同理），工号在人员接口里是唯一键。匹配不上就保持原样并报出来，
// 不猜——把 A 的账号绑到 B 头上比断链严重得多。
func rebindSysUsers(db *sql.DB, bindings []sysUserBinding) (rebound, unmatched int, err error) {
	for _, b := range bindings {
		res, err := db.Exec(`UPDATE sys_user SET employee_id =
			(SELECT id FROM employee WHERE emp_no = ? LIMIT 1)
			WHERE id = ? AND EXISTS (SELECT 1 FROM employee WHERE emp_no = ?)`,
			b.EmpNo, b.UserID, b.EmpNo)
		if err != nil {
			return rebound, unmatched, fmt.Errorf("重绑账号 %s（工号 %s）: %w", b.Username, b.EmpNo, err)
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			rebound++
		} else {
			unmatched++
		}
	}
	return rebound, unmatched, nil
}

func employeeExistsByNo(db *sql.DB, empNo string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM employee WHERE emp_no = ?`, empNo).Scan(&n)
	return n > 0, err
}

// writeBackup 把将被清理的 4 张表导出成可回灌的 INSERT 语句。
// 只导出这 4 张：它们才是本工具会动的东西，全库导出会把备份文件撑大且拖慢。
//
// bindings 会额外写成 UPDATE 语句附在末尾：账号重绑失败时靠它手工恢复。
func writeBackup(db *sql.DB, dir string, bindings []sysUserBinding) (string, error) {
	if dir == "" {
		dir = "orgreset-backup"
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("org-masters-%s.sql", time.Now().Format("20060102-150405")))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	tables := append([]string{"external_master_map"}, purgeTables...)
	for _, t := range tables {
		if err := dumpTable(db, f, t); err != nil {
			return "", fmt.Errorf("导出 %s: %w", t, err)
		}
	}

	if len(bindings) > 0 {
		if _, err := fmt.Fprint(f, "\n-- sys_user 重绑映射（账号 → 工号）。重建后按工号重新指向新的 employee.id\n"); err != nil {
			return "", err
		}
		for _, b := range bindings {
			if _, err := fmt.Fprintf(f, "-- %s -> %s\nUPDATE sys_user SET employee_id = (SELECT id FROM employee WHERE emp_no = %s LIMIT 1) WHERE id = %d;\n",
				b.Username, b.EmpNo, quote(b.EmpNo), b.UserID); err != nil {
				return "", err
			}
		}
	}
	return path, nil
}

// dumpTable 生成 INSERT 语句。表都很小（百行量级），逐行写即可。
func dumpTable(db *sql.DB, f *os.File, table string) error {
	cols, err := tableColumns(db, table)
	if err != nil {
		return err
	}
	rows, err := db.Query(fmt.Sprintf("SELECT %s FROM %s", strings.Join(cols, ","), table))
	if err != nil {
		return err
	}
	defer rows.Close()

	if _, err := fmt.Fprintf(f, "\n-- %s\n", table); err != nil {
		return err
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		lit := make([]string, len(vals))
		for i, v := range vals {
			lit[i] = sqlLiteral(v)
		}
		if _, err := fmt.Fprintf(f, "INSERT INTO %s (%s) VALUES (%s);\n",
			table, strings.Join(cols, ","), strings.Join(lit, ",")); err != nil {
			return err
		}
	}
	return rows.Err()
}

func tableColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`SELECT column_name FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("表 %s 不存在", table)
	}
	return cols, rows.Err()
}

func sqlLiteral(v any) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return quote(string(t))
	case string:
		return quote(t)
	case time.Time:
		return quote(t.Format("2006-01-02 15:04:05"))
	default:
		return fmt.Sprintf("%v", t)
	}
}

func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

// refReport 是重建后的引用完整性体检。
type refReport struct {
	Company, Department, Employee, MapRows int
	DeptWithCompany, DeptWithParent        int
	ParentOrphan, CompanyOrphan            int
	Cards                                  int
	CardDeptBroken, CardEmpBroken          int
	CardCompanyBroken                      int
	UserEmpBroken                          int
	SoftDeletedCards                       int
	SoftDeletedWithStaleRefs               int
	BrokenCards                            []string
}

// inspect 统计重建后的引用完整性。
//
// ⚠ 卡片一律只统计 deleted_at IS NULL 的：软删除的卡在界面上根本不显示，
// 把它们算进"断链"会报出一批用户看不到、也不需要处理的卡，
// 让人以为影响面比实际大。软删除卡单独报一行，只作信息。
func inspect(db *sql.DB) (*refReport, error) {
	r := &refReport{}
	scalar := []struct {
		dst   *int
		query string
	}{
		{&r.Company, "SELECT COUNT(*) FROM company"},
		{&r.Department, "SELECT COUNT(*) FROM department"},
		{&r.Employee, "SELECT COUNT(*) FROM employee"},
		{&r.MapRows, "SELECT COUNT(*) FROM external_master_map WHERE kind IN (?,?,?)"},
		{&r.DeptWithCompany, "SELECT COUNT(*) FROM department WHERE company_id > 0"},
		{&r.DeptWithParent, "SELECT COUNT(*) FROM department WHERE parent_id > 0"},
		{&r.ParentOrphan, `SELECT COUNT(*) FROM department d WHERE d.parent_id > 0
			AND NOT EXISTS (SELECT 1 FROM department p WHERE p.id = d.parent_id)`},
		{&r.CompanyOrphan, `SELECT COUNT(*) FROM department d WHERE d.company_id > 0
			AND NOT EXISTS (SELECT 1 FROM company c WHERE c.id = d.company_id)`},
		{&r.Cards, "SELECT COUNT(*) FROM asset_card WHERE deleted_at IS NULL"},
		{&r.CardDeptBroken, `SELECT COUNT(*) FROM asset_card c WHERE c.deleted_at IS NULL
			AND c.use_dept_id > 0 AND NOT EXISTS (SELECT 1 FROM department d WHERE d.id = c.use_dept_id)`},
		{&r.CardEmpBroken, `SELECT COUNT(*) FROM asset_card c WHERE c.deleted_at IS NULL
			AND c.user_emp_id > 0 AND NOT EXISTS (SELECT 1 FROM employee e WHERE e.id = c.user_emp_id)`},
		{&r.CardCompanyBroken, `SELECT COUNT(*) FROM asset_card c WHERE c.deleted_at IS NULL
			AND c.owner_company_id > 0 AND NOT EXISTS (SELECT 1 FROM company m WHERE m.id = c.owner_company_id)`},
		{&r.UserEmpBroken, `SELECT COUNT(*) FROM sys_user u WHERE u.employee_id > 0
			AND NOT EXISTS (SELECT 1 FROM employee e WHERE e.id = u.employee_id)`},
		{&r.SoftDeletedCards, "SELECT COUNT(*) FROM asset_card WHERE deleted_at IS NOT NULL"},
		{&r.SoftDeletedWithStaleRefs, `SELECT COUNT(*) FROM asset_card c WHERE c.deleted_at IS NOT NULL
			AND ((c.use_dept_id > 0 AND NOT EXISTS (SELECT 1 FROM department d WHERE d.id = c.use_dept_id))
			  OR (c.user_emp_id > 0 AND NOT EXISTS (SELECT 1 FROM employee e WHERE e.id = c.user_emp_id))
			  OR (c.owner_company_id > 0 AND NOT EXISTS (SELECT 1 FROM company m WHERE m.id = c.owner_company_id)))`},
	}
	for _, s := range scalar {
		var err error
		if strings.Contains(s.query, "kind IN") {
			err = db.QueryRow(s.query, mapKinds[0], mapKinds[1], mapKinds[2]).Scan(s.dst)
		} else {
			err = db.QueryRow(s.query).Scan(s.dst)
		}
		if err != nil {
			return nil, err
		}
	}

	rows, err := db.Query(`SELECT c.asset_code, c.name,
		(CASE WHEN c.use_dept_id > 0 AND NOT EXISTS (SELECT 1 FROM department d WHERE d.id = c.use_dept_id) THEN '部门' ELSE '' END),
		(CASE WHEN c.user_emp_id > 0 AND NOT EXISTS (SELECT 1 FROM employee e WHERE e.id = c.user_emp_id) THEN '使用人' ELSE '' END),
		(CASE WHEN c.owner_company_id > 0 AND NOT EXISTS (SELECT 1 FROM company m WHERE m.id = c.owner_company_id) THEN '权属公司' ELSE '' END)
		FROM asset_card c
		WHERE c.deleted_at IS NULL
		  AND ((c.use_dept_id > 0 AND NOT EXISTS (SELECT 1 FROM department d WHERE d.id = c.use_dept_id))
			OR (c.user_emp_id > 0 AND NOT EXISTS (SELECT 1 FROM employee e WHERE e.id = c.user_emp_id))
			OR (c.owner_company_id > 0 AND NOT EXISTS (SELECT 1 FROM company m WHERE m.id = c.owner_company_id)))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code, name, d, e, m string
		if err := rows.Scan(&code, &name, &d, &e, &m); err != nil {
			return nil, err
		}
		parts := []string{}
		for _, p := range []string{d, e, m} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		r.BrokenCards = append(r.BrokenCards, fmt.Sprintf("%s（%s）缺 %s", code, name, strings.Join(parts, "/")))
	}
	return r, rows.Err()
}

func (r *refReport) print() {
	fmt.Printf("      主数据: 公司 %d / 部门 %d（有归属公司 %d、有上级 %d）/ 员工 %d\n",
		r.Company, r.Department, r.DeptWithCompany, r.DeptWithParent, r.Employee)
	fmt.Printf("      孤儿: 父节点 %d / 归属公司 %d\n", r.ParentOrphan, r.CompanyOrphan)
	fmt.Printf("      资产卡 %d 张（不含软删除）: 使用部门断链 %d / 使用人断链 %d / 权属公司断链 %d\n",
		r.Cards, r.CardDeptBroken, r.CardEmpBroken, r.CardCompanyBroken)
	fmt.Printf("      账号绑定员工断链 %d\n", r.UserEmpBroken)
	if r.SoftDeletedCards > 0 {
		fmt.Printf("      （另有软删除卡 %d 张，其中 %d 张带失效引用；它们在界面上不显示，不计入上面）\n",
			r.SoftDeletedCards, r.SoftDeletedWithStaleRefs)
	}

	if len(r.BrokenCards) == 0 {
		fmt.Println("      ✓ 没有断链的卡片")
		return
	}
	fmt.Printf("\n      以下 %d 张卡的引用接不上（星瀚里没有对应的部门/人员/公司，或本地行不在同步范围）：\n", len(r.BrokenCards))
	for _, b := range r.BrokenCards {
		fmt.Printf("        - %s\n", b)
	}
	fmt.Println("      这些需要人工指定归属，工具不会替你猜。")
}
