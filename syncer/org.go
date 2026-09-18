package syncer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"asset-mgr/model"
	"asset-mgr/store"
)

// 组织主数据（公司 / 部门 / 员工）从星瀚同步。
//
// 与资产卡同步的三点不同：
//
//  1. **只做全量**。部门与人员接口的增量水位是请求体里的 createtime，
//     但星瀚侧的服务端过滤条件我们覆盖不了，增量拉回来的集合不可信；
//     90 个部门 + 136 个员工的体量也完全没有省流量的必要。
//
//  2. **有范围**。只同步「12」（电器板块）下面的 1201、1202 两个法人及其下级组织，
//     口径固定在 OrgScopeRoots。资产卡是"接口给什么就同步什么"，组织主数据是"只要这两个"。
//
//  3. **0 行必须中止**。见 orgplan.go 的 ErrOrgSourceEmpty。
//     资产卡返回 0 行最多是"没变化"，组织主数据返回 0 行若照常执行，
//     会把整张部门表/员工表判成"源里没有了"。
const (
	resourceOrg = "org"
	lockNameOrg = "asset_mgr_sync_org"
)

// orgSource 是写入 source / external_master_map 的来源标识，与资产卡保持一致。
const orgSource = sourceName

// OrgSyncResult 是一次组织同步的结果。
type OrgSyncResult struct {
	Run     *model.SyncRun
	Plan    *OrgPlan
	Created int // 新建的行数
	Updated int // 更新的行数
	// Conflicts 是本地手工数据与星瀚同名、但不是同一行的记录，需要人工决定去留。
	// 同步不按名称匹配，所以这些手工行不会被星瀚节点认领，会与同步行并存。
	Conflicts []store.OrgConflict
	// ConflictErr 是冲突检查本身的失败。单独暴露出来，避免"检查没跑成"
	// 被当成"没有冲突"——这两件事对使用者的含义完全相反。
	ConflictErr error
	DryRun      bool

	// resolved 记录「名称 → 本次同步落到的本地行 ID」，按 kind 分组。
	// 冲突检查用它排除"同名但本来就是同一行"的假冲突。
	//
	// dry-run 下「会新建」的条目登记为 orgPendingID（0），因为真实 ID 还不存在。
	// 不能用"不登记"来表示——那样会漏报真冲突：手工建的「信息数字化部」和
	// 星瀚要新建的 120112「信息数字化部」是两条不同的行，正是需要提醒人的情况。
	resolved map[string]map[string][]int64
}

// orgPendingID 是 dry-run 下「将会新建」的占位 ID。
// 本地自增 ID 从 1 起，所以 0 不会和任何真实行相等，能正确判为冲突。
const orgPendingID int64 = 0

// ResolvedNames 返回本次同步登记的名称数量（按 kind）。
// 只用于诊断：冲突检查依赖这张表，它是空的就什么都查不出来。
func (r *OrgSyncResult) ResolvedNames() map[string]int {
	out := make(map[string]int, len(r.resolved))
	for kind, byName := range r.resolved {
		out[kind] = len(byName)
	}
	return out
}

func (r *OrgSyncResult) noteResolved(kind, name string, id int64) {
	if r.resolved == nil {
		r.resolved = make(map[string]map[string][]int64)
	}
	if r.resolved[kind] == nil {
		r.resolved[kind] = make(map[string][]int64)
	}
	r.resolved[kind][name] = append(r.resolved[kind][name], id)
}

// PlanOrg 只读地拉取星瀚数据并算出落库计划：不写任何表、不落运行记录、不加锁。
//
// 给「清理主数据之前」这类场景用：删之前必须先确认源能读到东西，
// 否则一次接口故障就会把部门/员工删干净而重建不出来。
// BuildOrgPlan 里的五处 0 行护栏在这里同样生效，所以预检和真跑用的是同一套判据。
func (s *Service) PlanOrg(ctx context.Context) (*OrgSyncResult, error) {
	if s.client == nil {
		return nil, errors.New("kingdee client not configured")
	}
	depts, deptFilter, err := s.client.QueryDepartments(ctx)
	if err != nil {
		return nil, fmt.Errorf("拉取星瀚部门失败: %w", err)
	}
	people, peopleFilter, err := s.client.QueryPersonnel(ctx)
	if err != nil {
		return nil, fmt.Errorf("拉取星瀚人员失败: %w", err)
	}
	plan, err := BuildOrgPlan(depts, deptFilter, people, peopleFilter)
	if err != nil {
		return nil, err
	}
	// DryRun 固定为 true：这个入口本来就不写库，标成 false 会误导调用方。
	return &OrgSyncResult{Plan: plan, DryRun: true}, nil
}

// SyncOrg 拉取星瀚的部门与人员，按范围落库到 company / department / employee。
func (s *Service) SyncOrg(ctx context.Context, triggeredBy string) (*OrgSyncResult, error) {
	if s.client == nil {
		return nil, errors.New("kingdee client not configured")
	}

	// 进程内互斥 + 数据库 advisory lock（与资产卡同步共用同一把进程锁，避免并发写主数据）
	s.mu.Lock()
	defer s.mu.Unlock()

	conn, err := s.st.DB().Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("get db conn: %w", err)
	}
	defer conn.Close()

	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 10)", lockNameOrg).Scan(&locked); err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	if !locked {
		return nil, errors.New("无法获取组织同步锁，可能其他实例正在同步")
	}
	defer conn.ExecContext(ctx, "DO RELEASE_LOCK(?)", lockNameOrg)

	// 先把 running 记录落盘并提交，后面任何失败都能在同步列表里看到。
	// 不把这次提交省掉：拉两个接口要几十次请求，卡在网络上的时间远长于写一行记录，
	// 万一进程被杀，没有这条记录就完全看不出发生过什么。
	runID, err := s.createOrgRun(ctx, triggeredBy)
	if err != nil {
		return nil, err
	}

	fail := func(err error) (*OrgSyncResult, error) {
		s.finishOrgRun(ctx, runID, model.SyncStatusFailed, err.Error(), 0, 0)
		return nil, err
	}

	// ---- 1. 拉源数据 ----
	depts, deptFilter, err := s.client.QueryDepartments(ctx)
	if err != nil {
		return fail(fmt.Errorf("拉取星瀚部门失败: %w", err))
	}
	people, peopleFilter, err := s.client.QueryPersonnel(ctx)
	if err != nil {
		return fail(fmt.Errorf("拉取星瀚人员失败: %w", err))
	}

	// ---- 2. 算落库计划（纯函数，含 0 行护栏）----
	plan, err := BuildOrgPlan(depts, deptFilter, people, peopleFilter)
	if err != nil {
		return fail(err)
	}

	res := &OrgSyncResult{Plan: plan, DryRun: s.cfg.Sync.DryRun}

	tx, err := s.st.DB().BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()

	if s.cfg.Sync.DryRun {
		// dry-run 全程只读：只判断每一条是「会新建」还是「会更新」，不写任何行。
		if err := s.dryRunOrgPlan(tx, plan, res); err != nil {
			return fail(err)
		}
	} else {
		if err := s.applyOrgPlan(tx, plan, res); err != nil {
			return fail(err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fail(err)
	}

	// 同名冲突是读库的旁路检查，放在提交之后：它不影响同步结果，失败也不该让同步算失败。
	// 但**不能静默吞掉错误**——那会让"检查没跑成"看起来像"没有冲突"。
	if conflicts, err := s.findOrgConflicts(res); err != nil {
		res.ConflictErr = err
	} else {
		res.Conflicts = conflicts
	}

	total := len(plan.Companies) + len(plan.Departments) + len(plan.Employees)

	// 与资产卡同步一致：dry-run 也把「会新建 / 会更新」记进统计列，
	// 这样历史里能看到影响面的变化趋势。
	//
	// 但这会让「演练」和「真写」在运行记录里长得一样（130 这个数字看着像真建了 130 行），
	// 所以在摘要里明确标出来。摘要是运行记录表格里唯一能写自由文本的地方。
	summary := ""
	if res.DryRun {
		summary = "演练模式（dry_run）：未写库，新增/更新为预估数"
	}

	// 零变更也是成功。资产卡同步存在 partial（部分失败），组织同步是单事务、
	// 要么全成要么整体失败，没有中间态。
	s.finishOrgRunWithStats(ctx, runID, model.SyncStatusSuccess, summary, total, res.Created, res.Updated)

	// 回读刚写完的记录，而不是在这里拼一个近似的对象。
	// 自己拼会漏掉 started_at（零值时间序列化成 0001-01-01），
	// 也会让「接口返回的批次」和「运行记录里查到的批次」两处慢慢长歪。
	if run, err := s.st.GetSyncRun(runID); err == nil && run != nil {
		res.Run = run
	}
	return res, nil
}

// applyOrgPlan 按计划落库。
//
// 顺序有讲究：公司 → 部门（按层级从浅到深）→ 员工。
// 部门必须父先于子，parent_id 才能一遍串起来；
// 员工必须最后，因为它要引用部门 ID。
func (s *Service) applyOrgPlan(tx *sql.Tx, plan *OrgPlan, res *OrgSyncResult) error {
	// count 按「新建 / 更新」分别累加。口径必须与 dryRunOrgPlan 一致，
	// 否则演练说「会新建 130」、真跑报「新建 0」，同一个同步两个数。
	count := func(created bool) {
		if created {
			res.Created++
			return
		}
		res.Updated++
	}

	// ---- 公司 ----
	companyID := make(map[string]int64, len(plan.Companies))
	for _, c := range plan.Companies {
		id, created, err := store.ResolveOrgMasterTx(tx, orgSource, model.MasterKindCompany, c.Code, c.Name)
		if err != nil {
			return err
		}
		if err := store.UpdateOrgCompanyTx(tx, id, model.Company{
			Name: c.Name, Code: c.Code, Source: orgSource,
		}); err != nil {
			return err
		}
		companyID[c.Code] = id
		res.noteResolved(model.MasterKindCompany, c.Name, id)
		count(created)
	}

	// ---- 部门 ----
	deptID := make(map[string]int64, len(plan.Departments))
	for _, d := range plan.Departments {
		id, created, err := store.ResolveOrgMasterTx(tx, orgSource, model.MasterKindDepartment, d.Code, d.Name)
		if err != nil {
			return err
		}
		// 父节点可能是公司（1201/1202），那时不在部门表里，parent_id 落 0，
		// 归属靠 company_id 表达。这样前端"部门列表 + 所属公司"两列就够用，
		// 不需要把公司也塞进部门表造成两份真相。
		parentID := deptID[d.ParentCode]
		if err := store.UpdateOrgDepartmentTx(tx, id, model.Department{
			Name: d.Name, Code: d.Code, ParentID: parentID,
			CompanyID: companyID[d.CompanyCode],
			SortIndex: d.SortIndex, LongNumber: d.LongNumber,
			Level: d.Level, Enabled: d.Enabled, Source: orgSource,
		}); err != nil {
			return err
		}
		deptID[d.Code] = id
		res.noteResolved(model.MasterKindDepartment, d.Name, id)
		count(created)
	}

	// ---- 员工 ----
	for _, e := range plan.Employees {
		if strings.TrimSpace(e.EmpNo) == "" {
			// 工号是员工表的业务键，没有工号就没法建映射，跳过并计数
			continue
		}
		id, created, err := store.ResolveOrgMasterTx(tx, orgSource, model.MasterKindEmployee, e.EmpNo, e.Name)
		if err != nil {
			return err
		}
		if err := store.UpdateOrgEmployeeTx(tx, id, model.Employee{
			EmpNo: e.EmpNo, Name: e.Name,
			DeptID:    deptID[e.DeptCode],
			CompanyID: companyID[e.CompanyCode],
			Phone:     e.Phone, Active: e.Active, Source: orgSource,
		}); err != nil {
			return err
		}
		res.noteResolved(model.MasterKindEmployee, e.Name, id)
		count(created)
	}
	return nil
}

// dryRunOrgPlan 只读地统计「会新建」与「会更新」，不写任何行。
func (s *Service) dryRunOrgPlan(tx *sql.Tx, plan *OrgPlan, res *OrgSyncResult) error {
	count := func(kind, code, name string) error {
		id, err := store.LookupOrgMasterTx(tx, orgSource, kind, code)
		if err != nil {
			return err
		}
		if id == 0 {
			res.Created++
			// 会新建：登记占位 ID，让同名手工行能被正确报成冲突
			res.noteResolved(kind, name, orgPendingID)
			return nil
		}
		res.Updated++
		res.noteResolved(kind, name, id)
		return nil
	}
	for _, c := range plan.Companies {
		if err := count(model.MasterKindCompany, c.Code, c.Name); err != nil {
			return err
		}
	}
	for _, d := range plan.Departments {
		if err := count(model.MasterKindDepartment, d.Code, d.Name); err != nil {
			return err
		}
	}
	for _, e := range plan.Employees {
		if e.EmpNo == "" {
			continue
		}
		if err := count(model.MasterKindEmployee, e.EmpNo, e.Name); err != nil {
			return err
		}
	}
	return nil
}

// findOrgConflicts 找出本地手工行与星瀚行同名、但不是同一行的记录。
func (s *Service) findOrgConflicts(res *OrgSyncResult) ([]store.OrgConflict, error) {
	var out []store.OrgConflict
	for _, kind := range []string{
		model.MasterKindCompany, model.MasterKindDepartment, model.MasterKindEmployee,
	} {
		got, err := s.st.FindOrgNameConflicts(kind, res.resolved[kind])
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

// createOrgRun 先提交一条 running 记录，让失败也留痕。
func (s *Service) createOrgRun(ctx context.Context, triggeredBy string) (int64, error) {
	tx, err := s.st.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := s.st.CreateSyncRun(tx, orgSource, resourceOrg, model.SyncModeFull, triggeredBy, "")
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Service) finishOrgRun(ctx context.Context, runID int64, status, summary string, created, updated int) {
	s.finishOrgRunWithStats(ctx, runID, status, summary, created+updated, created, updated)
}

func (s *Service) finishOrgRunWithStats(ctx context.Context, runID int64, status, summary string, total, created, updated int) {
	tx, err := s.st.DB().BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	_ = store.UpdateSyncRunStats(tx, runID, model.SyncRun{
		TotalCount: total, CreatedCount: created, UpdatedCount: updated,
	})
	_ = store.FinishSyncRun(tx, runID, status, summary)
	_ = tx.Commit()
}
