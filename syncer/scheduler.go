package syncer

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"asset-mgr/model"
)

const (
	// startupDelay 启动后先等一会再补跑，避开启动阶段建表/迁移和外部探测的争抢。
	startupDelay = time.Minute

	// phaseTimeout 是单个阶段的超时（组织主数据、资产卡各一份）。
	// 分阶段给而不是给整条链一个总预算：组织同步卡住时不该把资产卡的额度吃掉，
	// 反过来也一样——两个阶段各自失败，才能各自看清是谁的问题。
	phaseTimeout = 30 * time.Minute

	// catchUpSkipWithin 是「马上就到下一个定时点」的阈值。
	// 23:59 启动时如果还去补跑今天这批，1 分钟后真正的定时批次又会跑一次，
	// 白跑一轮（组织同步要拉两个接口几十页）。这种情况直接等定时点。
	catchUpSkipWithin = 10 * time.Minute

	// TriggerScheduler 是写进 sync_run.triggered_by 的触发来源标识，
	// 也是「启动补跑」判断"今天的定时批次跑过没有"的依据。
	TriggerScheduler = "scheduler"
)

// Scheduler 每日在固定时刻跑一个批次：**先组织主数据，再资产卡**。
//
// 顺序不是风格问题。资产卡同步只会建「卡片引用到的」主数据，而且只写编码和名称，
// 不写组织关系。实测（空库，227 张卡）：
//
//	只跑资产卡          → 部门 29 个（0 个有归属公司 / 上级 / 组织路径）
//	                      员工 65 名（0 名挂到部门 / 公司）
//	先组织再跑资产卡    → 部门 88 个（88 个有归属公司、40 个有上级、88 个有路径）
//	                      员工 136 名（全部挂到部门与公司）
//
// 也就是说，顺序反了并不会产生重复行（组织同步按编码能把资产卡建的行接管过来，
// 下一轮就补齐了），但当天的台账里「使用部门」只有一个名字、没有归属公司，
// 按公司或部门筛选资产、划盘点范围都会漏掉——正是要避免的"资产信息不全"。
//
// 另有一条潜在风险支持同一个顺序：如果某张卡的使用部门**没有编码**，
// 资产卡同步会退到按名称兜底并建出一行无编码的部门，组织同步之后按编码匹配不上，
// 那才会真的变成两条。当前数据里没有这种卡，但兜底分支一直在。
//
// 不用 time.Ticker：Ticker 必须等满一个间隔才第一次触发，进程重启比间隔频繁就永远不跑；
// 这里改成「算准下一个定时点 → 等它」，并把下次运行时间暴露出去。
type Scheduler struct {
	svc    *Service
	hour   int
	minute int

	mu      sync.Mutex
	nextRun time.Time
	lastRun time.Time
	lastErr string
	running bool
}

func NewScheduler(svc *Service, hour, minute int) *Scheduler {
	return &Scheduler{svc: svc, hour: hour, minute: minute}
}

// SchedulerStatus 供 API 只读展示。
type SchedulerStatus struct {
	Enabled   bool       `json:"enabled"`
	DailyAt   string     `json:"daily_at"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	LastError string     `json:"last_error"`
	Running   bool       `json:"running"`
}

func (s *Scheduler) Status() SchedulerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := SchedulerStatus{
		Enabled:   true,
		DailyAt:   fmt.Sprintf("%02d:%02d", s.hour, s.minute),
		LastError: s.lastErr,
		Running:   s.running,
	}
	if !s.nextRun.IsZero() {
		t := s.nextRun
		st.NextRunAt = &t
	}
	if !s.lastRun.IsZero() {
		t := s.lastRun
		st.LastRunAt = &t
	}
	return st
}

// Run 调度循环，阻塞，调用方用 go 启动。ctx 取消后退出。
func (s *Scheduler) Run(ctx context.Context) {
	s.catchUpIfMissed(ctx)

	for {
		next := nextDaily(time.Now(), s.hour, s.minute)
		s.setNext(next)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.tick(ctx)
	}
}

// catchUpIfMissed 在启动时判断「最近一个定时点」这一批是否真的跑成功过，没跑成才补一次。
//
// 不无条件补跑：否则每次部署重启都会触发一轮组织 + 资产卡同步，
// 与「定时在零点」的预期不符，而且没人知道为什么会突然跑起来。
func (s *Scheduler) catchUpIfMissed(ctx context.Context) {
	now := time.Now()
	if time.Until(nextDaily(now, s.hour, s.minute)) <= catchUpSkipWithin {
		return
	}
	lastDue := lastScheduledBefore(now, s.hour, s.minute)

	// 判据是「**调度器**跑成功过一次资产卡」，两个限定词都有用：
	//
	//   - 资产卡而不是组织：资产卡是链上的最后一步，它成功就说明整批跑完了
	//     （组织失败时根本不会走到资产卡，见 runOnce）。
	//   - 调度器而不是任意来源：手工点「立即增量同步」只跑资产卡、不刷组织，
	//     不能拿它当"今天的定时批次已经跑过"，否则组织数据会整天不更新。
	//
	// 于是这四种情况都能正确落到该有的分支：零点批次成功 → 跳过；
	// 零点停机 → 补跑；零点卡在组织同步失败 → 补跑（当天还能救回来）；
	// 零点资产卡 partial → 补跑。
	ok, err := s.svc.HasSuccessfulRunSince(model.ResourceAssetCard, TriggerScheduler, lastDue)
	if err != nil {
		// 查不出来就不补跑。宁可漏一次，也不要在状态不明时擅自写主数据。
		log.Printf("启动补跑判断失败，本次不补跑: %v", err)
		return
	}
	if ok {
		return
	}

	log.Printf("启动补跑：%s 的定时批次未成功，%s 后立即执行一轮",
		lastDue.Format("2006-01-02 15:04"), startupDelay)
	timer := time.NewTimer(startupDelay)
	select {
	case <-ctx.Done():
		timer.Stop()
		return
	case <-timer.C:
	}
	s.tick(ctx)
}

func (s *Scheduler) setNext(t time.Time) {
	s.mu.Lock()
	s.nextRun = t
	s.mu.Unlock()
}

func (s *Scheduler) tick(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		// 上一轮还没跑完（比如接口变慢）：跳过这一轮，不要排队堆积
		s.mu.Unlock()
		log.Printf("定时同步跳过：上一轮仍在运行")
		return
	}
	s.running = true
	s.mu.Unlock()

	err := s.runOnce(ctx)

	s.mu.Lock()
	s.running = false
	s.lastRun = time.Now()
	if err != nil {
		s.lastErr = err.Error()
	} else {
		s.lastErr = ""
	}
	s.mu.Unlock()

	if err != nil {
		log.Printf("定时同步失败: %v", err)
		return
	}
	log.Printf("定时同步完成")
}

// runOnce 跑一个完整批次：组织主数据 → 资产卡。
//
// 组织同步失败时**不再继续跑资产卡**，这是有意的取舍：
// 批次的意义就是让资产卡在一个主数据完整的库上落地。组织同步没跑成还继续跑资产卡，
// 得到的就是"部门有名字没归属公司、员工没挂部门"的那份不完整数据，
// 而这正是把顺序定成先组织后资产卡要避免的。
//
// 代价是漏跑一天资产卡。这个代价可以接受，也是可恢复的：第二天照常，
// 或者人工在同步页点一次。反过来，如果组织同步的失败是
// "对方静默改了过滤条件"这类信号（组织同步的五处 0 行护栏就是为它准备的），
// 那更该让这批停下来让人看一眼，而不是带着可疑的主数据继续往前跑。
func (s *Scheduler) runOnce(ctx context.Context) error {
	log.Printf("定时同步开始：第一步，组织主数据")

	orgCtx, cancelOrg := context.WithTimeout(ctx, phaseTimeout)
	orgRes, err := s.svc.SyncOrg(orgCtx, TriggerScheduler)
	cancelOrg()
	if err != nil {
		return fmt.Errorf("组织主数据同步失败，本批已跳过资产卡同步（先组织后资产卡，避免资产引用到不完整的主数据）: %w", err)
	}
	log.Printf("组织主数据同步完成: 公司=%d 部门=%d 员工=%d（新建 %d / 更新 %d）",
		len(orgRes.Plan.Companies), len(orgRes.Plan.Departments), len(orgRes.Plan.Employees),
		orgRes.Created, orgRes.Updated)

	log.Printf("定时同步第二步：资产卡")
	cardCtx, cancelCard := context.WithTimeout(ctx, phaseTimeout)
	run, err := s.svc.Run(cardCtx, model.SyncModeIncremental, TriggerScheduler)
	cancelCard()
	if err != nil {
		return fmt.Errorf("资产卡同步失败: %w", err)
	}
	log.Printf("资产卡同步完成: id=%d status=%s %s", run.ID, run.Status, run.ErrorSummary)

	// partial 说明有卡片失败了。批次本身不算失败（下次会重试），但要让调度状态里看得见。
	if run.Status != model.SyncStatusSuccess {
		return fmt.Errorf("资产卡同步未全部成功: status=%s %s", run.Status, run.ErrorSummary)
	}
	return nil
}

// nextDaily 返回严格晚于 now 的、当天 HH:MM 的时刻；当天已过则取次日。
func nextDaily(now time.Time, hour, minute int) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// lastScheduledBefore 返回严格不晚于 now 的最近一个 HH:MM 时刻。
func lastScheduledBefore(now time.Time, hour, minute int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if t.After(now) {
		t = t.AddDate(0, 0, -1)
	}
	return t
}

// HasSuccessfulRunSince 是 store 查询的转发，放在这里是为了让调度器
// 只依赖 Service 这一个协作者（它需要的一切都已经在 Service 里了）。
func (s *Service) HasSuccessfulRunSince(resource, triggeredBy string, since time.Time) (bool, error) {
	return s.st.HasSuccessfulRunSince(resource, triggeredBy, since)
}
