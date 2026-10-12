package syncer

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"asset-mgr/integration/yunzhijia"
	"asset-mgr/model"
	"asset-mgr/store"
)

// 云之家通讯录增量同步（openId 回填路径 B）。
//
// 这是「未登录过 FAMS 的人也能收到待办」的唯一批量路径：SSO 回填（路径 A）
// 只覆盖自己登录过的人，而维修工 / 报修人是**任意员工**，不可能都先登录一遍。
// 方案见 docs/云之家待办通知集成方案.md §6.5 / §6.6。
//
// 与星瀚两阶段的关系：**两套独立外部源**，本同步的失败 / panic 绝不能中断
// 「组织主数据 → 资产卡」的批次（§6.5）。因此它在调度器里作为**独立一步**运行，
// 且不复用 runOnce「组织失败就跳过资产卡」的取舍。
const (
	// yzjSource 是通讯录同步写入 sync_run / sync_state 的 source。
	// 与星瀚的 "kingdee" 分开，两者的游标与失败记录互不影响。
	yzjSource = "yunzhijia"

	// yzjPageSize 是单页拉取条数。getallpersons 每次限制 1000 条以内
	// （integration/yunzhijia/person.go 亦会把 count 夹到 1000）。
	yzjPageSize = 1000

	// yzjMaxPages 是单轮同步的最大分页数（防跑飞护栏）。
	//
	// getallpersons 是**增量事件接口**：若因参数问题服务端一直返回满页，
	// 没有上限就是一个死循环。100 页 = 最多 10 万人，远超本企业体量（在职 130 人）。
	yzjMaxPages = 100

	// yzjStageOpenID 是 sync_run_error.stage 的取值（单人 openId 回填失败）。
	yzjStageOpenID = "openid"

	// yzjLockName 是通讯录同步的数据库 advisory lock 名。
	// 防止多实例、以及「定时批次」与「手动 yzjsync」并发写同一游标。
	yzjLockName = "asset_mgr_sync_yzj_org"
)

// YzjOrgResult 是一次云之家通讯录增量同步的结果。
//
// Updated 是「成功回填 openId 的人数」——它是判断「能不能打开 todo_enabled」的
// 唯一依据，所以单独成列，不与其它计数混在一起。
type YzjOrgResult struct {
	RunID   int64  // sync_run.id
	Pages   int    // 实际拉取页数
	Fetched int    // 拉取到的人员条数（含增量接口的重复项）
	Updated int    // 成功回填 openId 的人数
	Skipped int    // 缺工号 / 缺 openId 而跳过的人数
	// NotInSystem 是「云之家有、本系统无此工号」而跳过的人数。
	//
	// 这是**正常情况**、不是失败：云之家通讯录约 4465 人（整个工作圈），
	// 而本系统 employee 表只有百余人，绝大多数通讯录人员与 FAMS 无关。
	// 单独计列，避免把正常情况淹没在 Failed 里——否则退出码恒为 1、
	// 每次跑批写数千条 sync_run_error，真正的失败信号会被噪声盖住。
	NotInSystem int
	// Failed 只统计**真异常**：工号在本系统重复（拒填）与 SQL 出错。
	Failed int
	Cursor string // 本次的游标（最后一页返回的 data.time；未推进时保持旧值）
	Status string // sync_run.status
}

// yzjOrgOptions 让 source / resource / 分页参数可注入。
//
// source / resource 可注入是**硬要求**：sync_state 的唯一键是 (source, resource)，
// 若测试沿用生产的 ("yunzhijia", "yzj_org") 跑，就会覆盖生产游标。
// 测试传探针名（如 "yunzhijia-test"），绝不触碰生产行。
//
// pageSize / maxPages 可注入是为了让测试用小值快速跑完（默认见常量）。
type yzjOrgOptions struct {
	source      string
	resource    string
	triggeredBy string
	pageSize    int
	maxPages    int
}

// SyncYunzhijiaOrg 执行一次云之家通讯录增量同步（openId 回填路径 B）。
//
// 复用 sync_run / sync_state / sync_run_error，但**与星瀚同步链路完全独立**：
// source='yunzhijia'，失败不得中断「组织主数据 → 资产卡」的批次（§6.5）。
//
// 三种情况直接跳过（返回 nil, nil，**不算失败**）：
//  1. directory_sync_enabled=false —— 默认关闭是正常状态，不打错误日志；
//  2. yunzhijia.eid 为空 —— §12-Q5，eid 目前确实为空，不能把定时任务打成失败；
//  3. 云之家客户端不可用 —— 未注入且无凭证可构造。
//
// 内部委托给 syncYunzhijiaOrg，后者把 source/resource 参数化，便于测试隔离。
func (s *Service) SyncYunzhijiaOrg(ctx context.Context, triggeredBy string) (*YzjOrgResult, error) {
	return s.syncYunzhijiaOrg(ctx, yzjOrgOptions{
		source:      yzjSource,
		resource:    model.ResourceYzjOrg,
		triggeredBy: triggeredBy,
	})
}

// syncYunzhijiaOrg 是通讯录增量同步的主体。
//
// 语义严格按 §6.5：
//   - 读游标 → 建 running 运行记录 → 分页拉取 → 逐人回填 → 成功/部分成功才推进游标；
//   - begin 从 0 递增，返回条数不足 count 表示分页结束；
//   - 同一 openId 可能多次出现，**以「后出现的覆盖已有记录」**——
//     SetEmployeeOpenIDByEmpNo 本身即覆盖写，按返回顺序依次调用就天然满足，
//     因此**不做去重**（去重会把它变成"只写第一次"，与 K9 相反）。
func (s *Service) syncYunzhijiaOrg(ctx context.Context, opt yzjOrgOptions) (*YzjOrgResult, error) {
	// —— 门禁 1：开关（默认关闭是正常状态，不打错误日志）——
	if s.cfg == nil || !s.cfg.Yunzhijia.DirectorySyncEnabled {
		return nil, nil
	}

	// —— 门禁 2：eid（说明性日志，**不是 error**）——
	// eid 目前确实为空（§12-Q5），把它当失败会让每日同步任务长期显示红色，
	// 反而掩盖真正的问题。
	eid := strings.TrimSpace(s.cfg.Yunzhijia.EID)
	if eid == "" {
		log.Printf("通讯录同步跳过：未配置 yunzhijia.eid（团队id），无法拉取通讯录")
		return nil, nil
	}

	// —— 门禁 3：客户端 ——
	client := s.resolveYunzhijiaClient()
	if client == nil {
		log.Printf("通讯录同步跳过：云之家客户端不可用（未注入且缺少 app_id/secret）")
		return nil, nil
	}

	// 进程内互斥：与星瀚同步共用同一把进程锁，避免并发写主数据 / 游标。
	s.mu.Lock()
	defer s.mu.Unlock()

	// 数据库 advisory lock：防止多实例、以及定时批次与手动 yzjsync 并发。
	conn, err := s.st.DB().Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("get db conn: %w", err)
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 10)", yzjLockName).Scan(&locked); err != nil {
		return nil, fmt.Errorf("acquire yzj lock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("无法获取通讯录同步锁，可能已有实例正在同步")
	}
	defer conn.ExecContext(ctx, "DO RELEASE_LOCK(?)", yzjLockName)

	pageSize := opt.pageSize
	if pageSize <= 0 || pageSize > yzjPageSize {
		pageSize = yzjPageSize
	}
	maxPages := opt.maxPages
	if maxPages <= 0 {
		maxPages = yzjMaxPages
	}

	// 读游标；为空时交给 FetchAllPersons 回退首次起始时间（2008-08-02 01:40:38）。
	state, err := s.st.GetSyncState(opt.source, opt.resource)
	if err != nil {
		return nil, err
	}
	cursor := ""
	if state != nil {
		cursor = state.CursorValue
	}

	// 先落一条 running 记录并提交，让失败也留痕（与 SyncOrg 一致）。
	runID, err := s.createYzjRun(ctx, opt.source, opt.resource, cursor, opt.triggeredBy)
	if err != nil {
		return nil, err
	}

	res := &YzjOrgResult{RunID: runID, Cursor: cursor}
	lastTime := ""

	var fetchErr error
	begin := 0
	for page := 0; page < maxPages; page++ {
		pg, err := client.FetchAllPersons(ctx, eid, cursor, begin, pageSize)
		if err != nil {
			// 拉取失败：记下原因，跳出主循环，统一在下面判定状态与是否推进游标。
			fetchErr = err
			break
		}
		res.Pages++
		if pg.Time != "" {
			lastTime = pg.Time
		}

		// 护栏 ①：空页立即结束（增量接口无数据时的正常返回）。
		if len(pg.Persons) == 0 {
			break
		}

		for _, p := range pg.Persons {
			res.Fetched++
			if strings.TrimSpace(p.JobNo) == "" || strings.TrimSpace(p.OpenID) == "" {
				// 缺工号无从匹配，缺 openId 无处可填，都只能跳过。
				res.Skipped++
				continue
			}
			match, err := s.st.SetEmployeeOpenIDByEmpNo(p.JobNo, p.OpenID)
			if err != nil {
				// SQL 出错 = 真异常，必须可见。
				res.Failed++
				s.addYzjError(ctx, runID, p.JobNo, "回填 openId 出错: "+err.Error())
				continue
			}
			switch match {
			case store.EmpNoNotFound:
				// 本系统无此工号：**正常情况**（云之家通讯录远大于本系统花名册）。
				// 不记 Failed、**不写 sync_run_error**——否则每次跑批写数千条垃圾、
				// 退出码恒为 1，真正的失败会被噪声淹没。
				res.NotInSystem++
			case store.EmpNoAmbiguous:
				// 工号在本系统重复：真异常，必须可见。拒填（宁可漏填也不填错人）。
				res.Failed++
				s.addYzjError(ctx, runID, p.JobNo, "工号在本系统重复，openId 未回填（宁可漏填也不填错人）")
			case store.EmpNoUnique:
				res.Updated++
			}
		}

		// 分页结束判据：返回条数不足 count。
		if len(pg.Persons) < pageSize {
			break
		}
		begin += len(pg.Persons)
	}

	status := model.SyncStatusSuccess
	if res.Failed > 0 {
		status = model.SyncStatusPartial
	}
	if fetchErr != nil {
		// 拉取失败：整轮作废。
		status = model.SyncStatusFailed
	}
	res.Status = status

	summary := ""
	if fetchErr != nil {
		summary = "拉取失败: " + fetchErr.Error()
	}
	s.finishYzjRun(ctx, runID, status, summary, res)

	// 成功 / 部分成功才推进游标（与 syncer.go 的资产卡同步一致）。
	//
	// 失败（拉取错误）**不推进**：最后一页返回的 time 才是"已完整消费到的时刻"，
	// 中途失败时 lastTime 只到某个中间页，推进它会跳过后续页里的变更。下次从旧游标
	// 重跑即可续上（回填是覆盖写，幂等）。
	if status != model.SyncStatusFailed && lastTime != "" {
		if err := s.updateYzjState(ctx, opt.source, opt.resource, lastTime, runID); err != nil {
			return res, fmt.Errorf("推进通讯录游标失败: %w", err)
		}
		res.Cursor = lastTime
	}
	if fetchErr != nil {
		return res, fmt.Errorf("通讯录同步拉取失败: %w", fetchErr)
	}
	return res, nil
}

// resolveYunzhijiaClient 返回通讯录同步用的云之家客户端。
//
// 优先用注入的（测试 / 显式接线）；否则在凭证齐备时按配置构造一个。
//
// 为什么保留兜底构造：main.go 负责创建客户端并交给 api.Server / 通知服务，
// 但（在本次改动范围内）未接线到 syncer.Service。兜底构造让通讯录同步**无需改动
// main.go 即可工作**；一旦 main.go 显式注入，注入的客户端优先（见 SetYunzhijiaClient）。
func (s *Service) resolveYunzhijiaClient() *yunzhijia.Client {
	if s.yzjClient != nil {
		return s.yzjClient
	}
	if s.cfg == nil {
		return nil
	}
	yz := s.cfg.Yunzhijia
	if yz.AppID == "" || yz.Secret == "" {
		return nil
	}
	// 构造条件与 main.go 一致（app_id/secret 齐备）。客户端自带 token 缓存，
	// 即便每轮新建也只是多取一次 token，代价可忽略。
	return yunzhijia.NewClient(yz.BaseURL, yz.AppID, yz.Secret, 10*time.Second)
}

// createYzjRun 先提交一条 running 记录，让失败也留痕。
func (s *Service) createYzjRun(ctx context.Context, source, resource, cursor, triggeredBy string) (int64, error) {
	tx, err := s.st.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := s.st.CreateSyncRun(tx, source, resource, model.SyncModeIncremental, triggeredBy, cursor)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// finishYzjRun 写入统计并结束运行记录。extra 非空时拼在摘要前面（用于拉取失败原因）。
func (s *Service) finishYzjRun(ctx context.Context, runID int64, status, extra string, res *YzjOrgResult) {
	tx, err := s.st.DB().BeginTx(ctx, nil)
	if err != nil {
		log.Printf("通讯录同步：写运行记录失败: %v", err)
		return
	}
	defer tx.Rollback()
	_ = store.UpdateSyncRunStats(tx, runID, model.SyncRun{
		TotalCount:   res.Fetched,
		UpdatedCount: res.Updated,
		SkippedCount: res.Skipped,
		FailedCount:  res.Failed,
	})
	summary := fmt.Sprintf("pages=%d fetched=%d updated=%d skipped=%d not_in_system=%d failed=%d",
		res.Pages, res.Fetched, res.Updated, res.Skipped, res.NotInSystem, res.Failed)
	if extra != "" {
		summary = extra + " | " + summary
	}
	_ = store.FinishSyncRun(tx, runID, status, summary)
	_ = tx.Commit()
}

// addYzjError 记录单人回填失败（external_id=jobNo，stage='openid'）。
// 写失败只记日志，不影响主流程——它本身是"可见性"的补充，不该反过来中断同步。
func (s *Service) addYzjError(ctx context.Context, runID int64, jobNo, msg string) {
	tx, err := s.st.DB().BeginTx(ctx, nil)
	if err != nil {
		log.Printf("通讯录同步：写错误记录失败: %v", err)
		return
	}
	defer tx.Rollback()
	_ = store.AddSyncRunError(tx, runID, model.SyncRunError{
		ExternalID:   jobNo,
		Stage:        yzjStageOpenID,
		ErrorMessage: truncate(msg, 500),
		Retryable:    false,
	})
	_ = tx.Commit()
}

// updateYzjState 推进游标（成功 / 部分成功才调用）。
func (s *Service) updateYzjState(ctx context.Context, source, resource, cursor string, runID int64) error {
	tx, err := s.st.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := store.UpdateSyncState(tx, source, resource, cursor, runID); err != nil {
		return err
	}
	return tx.Commit()
}
