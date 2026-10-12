package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"asset-mgr/config"
	"asset-mgr/integration/yunzhijia"
	"asset-mgr/model"
	"asset-mgr/store"
)

// 通知队列的固定容量与 worker 数（§7.2）：**禁止**每次跃迁裸开 goroutine
// （外部慢 / 高并发时 goroutine 无界增长），改用固定 worker pool + 有界 buffered channel。
const (
	notifyQueueSize   = 256
	notifyWorkers     = 4
	notifyMaxAttempts = 3
	notifyBackoffBase = 500 * time.Millisecond
	notifyErrMaxLen   = 500 // repair_notify_task.last_error 列宽
	notifyCallTimeout = 15 * time.Second
)

// NotifyService 是维修待办通知的编排主体：把维修单状态跃迁转成云之家待办调用，
// 用有界队列 + worker pool 异步投递，失败落 repair_notify_task 并退避重试。
//
// 设计要点（见 docs/云之家待办通知集成方案.md §7）：
//   - 投递在业务事务**之外**（挂载点已保证在 CreateRepair / Transition 提交之后）；
//   - 有界队列满 / 服务关闭时**不阻塞调用方**，任务落 failed；
//   - 幂等以 repair_notify_task 为唯一真相，**绝不**依赖云之家 create 报错去重（§4.3）；
//   - 所有日志走 yunzhijia.MaskOpenID 脱敏，绝不打印 token / secret。
//
// 零值不可用，必须经 NewNotifyService 构造。方法对 nil 接收者安全（挂载点可无条件调用）。
type NotifyService struct {
	st  *store.Store
	yzj *yunzhijia.Client

	enabled       bool
	todoAppID     string
	headImg       string
	todoType      int
	tagID         string
	senderID      string
	portalBaseURL string

	// —— 统一流程中心（cloudflow）——
	//
	// flowCenterEnabled 决定**新建**待办走哪个通道：true → 流程中心，false → im-todo。
	// ⚠️ **办结 / 删除**已有待办**不看它**，看那条记录自己的 task.Channel——否则切换前用
	// im-todo 发出的存量待办会永远办不掉（文档「必读」第 2 条，见 notify_flowcenter.go 顶部）。
	flowCenterEnabled  bool
	flowCenterTypeID   string
	flowCenterTypeName string
	flowCenterAppName  string

	// deeplinkSecret / deeplinkTTL 用于给待办 URL 嵌第 2 层免登 token（§5.4）。
	// 这里存的是**独立密钥**（yunzhijia.login_token_secret），不是 auth.jwt_secret；
	// 为空即表示第 2 层未启用，此时 deepLink 静默降级为裸链接。
	deeplinkSecret string
	deeplinkTTL    time.Duration

	// informational 是「信息性告知」开关（§3.2 的 #2/#5/#7 共三处），对应配置项
	// yunzhijia.informational_enabled（默认 false）。
	// 打开后这三处走「新建**已读**待办」（create 后紧跟 action{read:1}，见 notify_plan.go 的
	// ensureReadOp / informationalNotice）——语义是「推，但不打扰」，而不是让一条未读待办挂在
	// 收件人的待办列表里（产品定义 Q2 的推荐①）。
	informational bool

	maxAttempts int
	backoff     time.Duration

	queue    chan *notifyJob
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	// inflight 是「已入队、尚未处理完」的任务数，供 WaitIdle / 优雅关闭判断是否排空。
	inflight int64
}

// notifyJob 是一次跃迁解析出的投递单元：一个维修单 + 一组已解析好收件人与文案的操作。
type notifyJob struct {
	repairID int64
	code     string
	sourceID string
	// reporterEmpID：报修人 empID。流程中心的 creator（流程发起人）取它的 openId
	// ——「谁报修就以谁名义发起」，解析与兜底见 resolveCreatorOpenID。
	//
	// 为什么放在 job 上、而不是在 planFor 里顺手解析成 openId：
	// creator 只有流程中心通道用得到，放在 job 上可以让 im-todo 通道**完全不碰**这次查询。
	reporterEmpID int64
	ops           []notifyOp
}

// NewNotifyService 创建通知服务并启动 worker pool。
//
// 即使未启用（todo_enabled=false，或云之家客户端未就绪 / 必填项缺失），也返回一个
// **可安全调用**的服务（enqueue 全部 no-op）。启用条件：显式开关 + 客户端就绪 + 必填项齐备。
func NewNotifyService(st *store.Store, cfg *config.Config, yzj *yunzhijia.Client) *NotifyService {
	s := &NotifyService{
		st:            st,
		yzj:           yzj,
		todoAppID:     cfg.Yunzhijia.TodoAppID,
		headImg:       cfg.Yunzhijia.HeadImg,
		todoType:      cfg.Yunzhijia.TodoType,
		tagID:         cfg.Yunzhijia.TagID,
		senderID:      cfg.Yunzhijia.SenderID,
		portalBaseURL: strings.TrimSuffix(cfg.Yunzhijia.PortalBaseURL, "/"),

		flowCenterEnabled:  cfg.Yunzhijia.FlowCenterEnabled,
		flowCenterTypeID:   cfg.Yunzhijia.FlowCenterTypeID,
		flowCenterTypeName: cfg.Yunzhijia.FlowCenterTypeName,
		flowCenterAppName:  cfg.Yunzhijia.FlowCenterAppName,

		deeplinkSecret: cfg.Yunzhijia.LoginTokenSecret,
		deeplinkTTL:    cfg.Yunzhijia.DeeplinkTokenTTL,
		informational:  cfg.Yunzhijia.InformationalEnabled,
		maxAttempts:    notifyMaxAttempts,
		backoff:        notifyBackoffBase,
		queue:          make(chan *notifyJob, notifyQueueSize),
		stopCh:         make(chan struct{}),
	}
	// 启用条件：任一通道显式开启 + 云之家客户端就绪。配置校验已在 config.Load 里做过一次
	//（缺必填项会记日志并关闭对应通道），这里是运行期兜底。
	s.enabled = (cfg.Yunzhijia.TodoEnabled || cfg.Yunzhijia.FlowCenterEnabled) && yzj != nil

	// 兜底 1：portal_base_url 是两个通道**共用**的（拼待办深链），为空则谁都发不出去。
	if s.enabled && s.portalBaseURL == "" {
		log.Printf("notify: portal_base_url 为空（拼待办深链必填），已关闭待办通知")
		s.enabled = false
	}
	// 兜底 2：im-todo 专属必填项（head_img 是 generatetodo 必填）——**仅** im-todo 通道启用时校验，
	// 否则「只开流程中心」会被 im-todo 的字段缺失误杀。
	if s.enabled && cfg.Yunzhijia.TodoEnabled && (s.todoAppID == "" || s.headImg == "") {
		log.Printf("notify: im-todo 待办配置不完整（app_id / head_img 有空值），已关闭 im-todo 通道")
		s.enabled = false
	}
	// 兜底 3：流程中心启用但 type_id 为空 → 关闭流程中心（**回退 im-todo**），与配置层启动防御同一思路。
	if s.enabled && s.flowCenterEnabled && s.flowCenterTypeID == "" {
		log.Printf("notify: 流程中心已启用但 flowcenter_type_id 为空，已关闭流程中心（新待办回退 im-todo）")
		s.flowCenterEnabled = false
	}
	// 兜底 4：关掉流程中心后若 im-todo 也没启用 → 无通道可用，整体关闭。
	if s.enabled && !cfg.Yunzhijia.TodoEnabled && !s.flowCenterEnabled {
		log.Printf("notify: 无可用待办通道（im-todo 与流程中心均未就绪），已关闭待办通知")
		s.enabled = false
	}
	// 流程中心走 team 级 accessToken，**必须带 eid**。main.go 未调用 SetEID，这里就地补上
	// （幂等、配置期一次），否则流程中心请求会在运行期以「eid 未配置」静默失败。
	if s.enabled && s.flowCenterEnabled && yzj != nil && cfg.Yunzhijia.EID != "" {
		yzj.SetEID(cfg.Yunzhijia.EID)
	}
	if s.enabled {
		// 启动时把 openId 覆盖率打出来（§3.1 / §6.4）。
		//
		// 若覆盖率为 0，待办会「上线了但一条都发不出去」——运行期只表现为一条条
		// 「skip emp=N 无云之家 openId」的日志，极易被误判成「配置没生效」，
		// 而真实原因是「还没有任何人绑定 openId」。这是最难定位的一类失败，
		// 所以在启动这一刻就用一条明确的告警把它拦住。
		if st != nil {
			withOpenID, total, err := st.CountEmployeeOpenIDCoverage()
			switch {
			case err != nil:
				// 查不到覆盖率不能影响启动——它只是诊断信息。
				log.Printf("notify: 查询 openId 覆盖率失败（不影响启动）: %v", err)
			case total == 0:
				log.Printf("notify: 待办通知已启用，但在册员工数为 0，无法判断 openId 覆盖率")
			case withOpenID == 0:
				log.Printf("配置警告：待办通知已启用，但 %d 名在册员工中**没有一人**绑定云之家 openId，"+
					"所有待办都会被跳过——表现为「功能已开启但收不到任何通知」。"+
					"请先让员工免登一次（路径 A，自动回填）或运行 cmd/yzjsync 批量回填（路径 B），"+
					"确认覆盖率大于 0 后再启用 todo_enabled。", total)
			default:
				log.Printf("notify: openId 覆盖 %d/%d 名在册员工", withOpenID, total)
			}
		}
		for i := 0; i < notifyWorkers; i++ {
			s.wg.Add(1)
			go s.worker()
		}
		log.Printf("notify: 待办通知已启用（workers=%d queue=%d 新待办通道=%s）",
			notifyWorkers, notifyQueueSize, s.newTaskChannel())
	} else {
		log.Printf("notify: 待办通知未启用（todo_enabled=%v flowcenter_enabled=%v client=%v）",
			cfg.Yunzhijia.TodoEnabled, cfg.Yunzhijia.FlowCenterEnabled, yzj != nil)
	}
	return s
}

// OnRepairCreated 是挂载点一：新建报修成功后调用（空串 → pending，不经过 Transition）。
// 创建没有「跃迁前状态」，故 prevStatus 传空串（submit 分支不使用它）。
func (s *NotifyService) OnRepairCreated(o *model.RepairOrder) {
	if s == nil || !s.enabled || o == nil {
		return
	}
	s.enqueue(o, model.RepairActionSubmit, "", "")
}

// OnRepairTransition 是挂载点二：状态跃迁成功后调用（Transition 已提交、writeJSON 之前）。
//
// prevStatus 是**跃迁前**状态（调用点传 `o.Status`，非返回值 updated.Status）：
// 仅 cancel 分支用它区分「报修人撤单（pending→cancelled，行 10）」与
// 「管理员取消（accepted/dispatched→cancelled，行 11）」，两者 action 同为 cancel。
func (s *NotifyService) OnRepairTransition(o *model.RepairOrder, action, remark, prevStatus string) {
	if s == nil || !s.enabled || o == nil {
		return
	}
	s.enqueue(o, action, remark, prevStatus)
}

// OnRepairDeleted 是挂载点三：管理员**物理删除**维修单之后调用，清理云之家侧残留待办。
//
// 与「撤单」共用同一套整单清理（deliverDeleteAll：删流程实例 + 删该 sourceId 下所有 im-todo），
// 因为语义相同 —— 单据已终结，不该再有任何待办挂在别人的待办列表里。
//
// 与撤单的唯一区别：**不产生任何新建待办**。撤单会给报修人补一条「已被管理员取消」
// （planFor 的 cancel 分支），而单据已物理删除，再推待办只会把用户送到一个 404 的深链上。
//
// ⚠️ 调用时机：必须在 store.DeleteRepairOrders **提交之后**（与另两个挂载点一致）。
// 此时 repair_notify_task 行已随单据一起删除，deliverDeleteAll 里「逐人置 deleted」退化为
// no-op；但删流程实例 / 删 im-todo 都是按 sourceId 调的，不依赖本地行，故清理依然生效。
func (s *NotifyService) OnRepairDeleted(o *model.RepairOrder) {
	if s == nil || !s.enabled || o == nil {
		return
	}
	s.enqueueOps(o, "delete", []notifyOp{s.deleteAllOp(o)})
}

// enqueue 解析触发矩阵并投递到有界队列。
func (s *NotifyService) enqueue(o *model.RepairOrder, action, remark, prevStatus string) {
	s.enqueueOps(o, action, s.planFor(o, action, remark, prevStatus))
}

// enqueueOps 把已解析好的操作清单投递到有界队列。队列满 / 服务关闭时落 failed，**绝不阻塞调用方**。
// label 只用于日志（action 名或 "delete"），不影响投递语义。
func (s *NotifyService) enqueueOps(o *model.RepairOrder, label string, ops []notifyOp) {
	if len(ops) == 0 {
		return
	}
	job := &notifyJob{
		repairID:      o.ID,
		code:          o.Code,
		sourceID:      notifySourceID(o.ID),
		reporterEmpID: o.ReporterEmpID,
		ops:           ops,
	}
	atomic.AddInt64(&s.inflight, 1)
	// 先判停止：服务关闭后不再接收新任务（§7.4）。用独立 select 保证「已停止」优先于入队，
	// 避免 select 在「队列可写」与「已停止」同时就绪时随机选中前者、把任务遗留在队列里。
	select {
	case <-s.stopCh:
		s.markFailed(job, "通知服务正在关闭")
		atomic.AddInt64(&s.inflight, -1)
		return
	default:
	}
	select {
	case s.queue <- job:
		// 入队成功，由 worker 处理并在 process 里递减 inflight。
	default:
		// 队列满：丢弃并记日志 + 落 failed，绝不阻塞业务请求（§7.2）。
		log.Printf("notify: 队列已满，任务转为失败落库 repair=%s action=%s", o.Code, label)
		s.markFailed(job, "通知队列已满")
		atomic.AddInt64(&s.inflight, -1)
	}
}

// worker 消费队列；收到停止信号后把队列里剩余任务处理完再退出。
func (s *NotifyService) worker() {
	defer s.wg.Done()
	for {
		select {
		case job := <-s.queue:
			s.process(job)
		case <-s.stopCh:
			for {
				select {
				case job := <-s.queue:
					s.process(job)
				default:
					return
				}
			}
		}
	}
}

// process 逐条投递一个任务的所有操作。
func (s *NotifyService) process(job *notifyJob) {
	defer atomic.AddInt64(&s.inflight, -1)
	for _, op := range job.ops {
		s.deliver(job, op)
	}
}

func (s *NotifyService) deliver(job *notifyJob, op notifyOp) {
	switch op.kind {
	case opEnsure:
		s.deliverEnsure(job, op)
	case opDeal:
		s.deliverDeal(job, op)
	case opRead:
		s.deliverRead(job, op)
	case opDeleteAll:
		s.deliverDeleteAll(job, op)
	case opFinishFlow:
		s.deliverFinishFlow(job, op)
	}
}

// deliverEnsure 确保收件人有一条「未办」待办（§4.3 状态机）：
// 无记录 → create；已有 active/dealt → 先 delete（该人）再 create（重新激活）。
//
// **新建**待办走哪个通道由 s.newTaskChannel()（当前开关）决定；而「先删旧待办」必须按
// **旧记录自己的 task.Channel** 分派（见 deleteForReactivate），否则存量 im-todo 待办删不掉。
func (s *NotifyService) deliverEnsure(job *notifyJob, op notifyOp) {
	if op.openID == "" {
		return // 已在 planFor 记日志（无 openId 跳过该人）
	}
	task, err := s.st.GetNotifyTask(job.repairID, op.empID, job.sourceID)
	if err != nil {
		log.Printf("notify: op=create repair=%s 查询任务失败 err=%v", job.code, err)
		return
	}
	prev := model.NotifyStatePending
	if task != nil {
		prev = task.State
	}
	channel := s.newTaskChannel()
	// 重新通知同一个人前，是否先删旧待办：由 shouldDeleteForReactivate 按旧记录自己的 channel
	// 判定（flowcenter 下 dealt 的节点不删——那是已办结的节点，要留痕，见该函数注释）。
	if s.shouldDeleteForReactivate(task) {
		if err := s.deleteForReactivate(job, task, op); err != nil {
			log.Printf("notify: op=create repair=%s 先删旧待办失败（继续尝试 create）openId=%s err=%v",
				job.code, yunzhijia.MaskOpenID(op.openID), err)
		}
	}
	// 已知限制（P0 接受）：若前序 done 失败，该记录 state=failed，下次 ensure 因 state 不满足
	// shouldDeleteForReactivate（flowcenter 要求 active，im-todo 要求 active/dealt）而不走删除
	// 分支，直接 upsert 覆盖 biz_id —— 旧节点的 bizId 就此丢失。属「接口失败」异常路径，
	// 有界重试 3 次 + 失败有日志，P0 接受，待有真实数据后评估。
	// 落库 pending（唯一键 upsert，不重置已推进的状态）；同时记录本条的通道与（流程中心的）bizId。
	nt := &model.NotifyTask{
		RepairID: job.repairID, EmpID: op.empID, OpenID: op.openID,
		SourceID: job.sourceID, Purpose: op.purpose, State: model.NotifyStatePending,
		Channel: channel,
	}
	if channel == notifyChannelFlowCenter {
		nt.BizID = flowBizID(job.sourceID, op.empID, op.purpose)
	}
	if err := s.st.UpsertNotifyTask(nt); err != nil {
		log.Printf("notify: op=create repair=%s 落库失败 err=%v", job.code, err)
	}

	var errCode int
	if channel == notifyChannelFlowCenter {
		res, err := s.retrySendFlowTodo(job, op)
		if err != nil {
			s.recordFailure(job, op, err)
			return
		}
		errCode = res.ErrorCode
	} else {
		res, err := s.retryCreate(job, op)
		if err != nil {
			s.recordFailure(job, op, err)
			return
		}
		errCode = res.ErrorCode
	}
	s.markState(job, op, prev, model.NotifyStateActive)
	log.Printf("notify: op=create channel=%s repair=%s openId=%s result=ok errorCode=%d",
		channel, job.code, yunzhijia.MaskOpenID(op.openID), errCode)

	// withRead：信息性告知（产品定义 Q2）——待办已建好，立刻置已读避免打扰。
	// 置已读失败不回滚待办（它已存在，只是显示为未读），记日志即可。
	if op.withRead {
		if channel == notifyChannelFlowCenter {
			// 理论不可达（planFor 的 informationalNotice 已跳过 flowcenter）；防御性记日志。
			log.Printf("notify: op=create repair=%s withRead 但 channel=flowcenter（无置已读能力），跳过", job.code)
		} else if _, err := s.retryAction(job, yunzhijia.TodoActionType{Read: intPtr(1)}, []string{op.openID}); err != nil {
			log.Printf("notify: op=create repair=%s 置已读失败（待办已创建，仅显示为未读）openId=%s err=%v",
				job.code, yunzhijia.MaskOpenID(op.openID), err)
		}
	}
}

// deliverDeal 把收件人当前待办流转为已办。
//
// 幂等守卫（这是 accept/dispatch/take/finish/confirm 多处可能对同一人重复 deal 的前提）：
// 无记录 / 已删除 / **已办** 一律跳过——对已办的人再发一次 deal 是多余的云之家调用，
// 且状态机不允许「已办 → 已办」之外的语义。
func (s *NotifyService) deliverDeal(job *notifyJob, op notifyOp) {
	if op.openID == "" {
		return
	}
	task, err := s.st.GetNotifyTask(job.repairID, op.empID, job.sourceID)
	if err != nil {
		log.Printf("notify: op=deal repair=%s 查询任务失败 err=%v", job.code, err)
		return
	}
	if task == nil || task.State == model.NotifyStateDeleted || task.State == model.NotifyStateDealt {
		return
	}
	// 按**该记录自己的 channel** 分派（灰度切换地基）：存量 im-todo 待办用 action，流程中心用 done。
	if shouldUseFlowCenter(task) {
		bizID, ok := flowBizIDOf(task)
		if !ok {
			log.Printf("notify: op=deal repair=%s channel=flowcenter 但 bizId 为空（未成功发送？），跳过", job.code)
			return
		}
		res, err := s.retryDoneFlowTodo(bizID)
		if err != nil {
			s.recordFailure(job, op, err)
			return
		}
		s.markState(job, op, task.State, model.NotifyStateDealt)
		log.Printf("notify: op=deal channel=flowcenter repair=%s bizId=%s result=ok errorCode=%d",
			job.code, bizID, res.ErrorCode)
		return
	}
	at := yunzhijia.TodoActionType{Deal: intPtr(1)}
	if op.withRead {
		at.Read = intPtr(1)
	}
	res, err := s.retryAction(job, at, []string{op.openID})
	if err != nil {
		s.recordFailure(job, op, err)
		return
	}
	s.markState(job, op, task.State, model.NotifyStateDealt)
	log.Printf("notify: op=deal channel=im-todo repair=%s openId=%s result=ok errorCode=%d",
		job.code, yunzhijia.MaskOpenID(op.openID), res.ErrorCode)
}

// deliverRead 只把收件人待办置「已读」（actiontype{read:1}），**本地状态保持 active**。
//
// 「已读」不是本地状态机的节点，只是云之家侧的一个标记：提醒已被看到，但待办仍未办完
// （如维修工接单后仍在维修）。因此这里不调 markState，避免把 active 误推进成 dealt。
func (s *NotifyService) deliverRead(job *notifyJob, op notifyOp) {
	if op.openID == "" {
		return
	}
	task, err := s.st.GetNotifyTask(job.repairID, op.empID, job.sourceID)
	if err != nil {
		log.Printf("notify: op=read repair=%s 查询任务失败 err=%v", job.code, err)
		return
	}
	if task == nil || task.State == model.NotifyStateDeleted {
		return
	}
	// 按该记录自己的 channel 分派：流程中心没有「只置已读」，回滚后残留的 flowcenter 待办
	// 用 done 办结（只办结、不发新节点——开关关闭时 planFor 不会生成新节点 op）。
	if shouldUseFlowCenter(task) {
		bizID, ok := flowBizIDOf(task)
		if !ok {
			log.Printf("notify: op=read repair=%s channel=flowcenter 但 bizId 为空（未成功发送？），跳过", job.code)
			return
		}
		res, err := s.retryDoneFlowTodo(bizID)
		if err != nil {
			s.recordFailure(job, op, err)
			return
		}
		log.Printf("notify: op=read channel=flowcenter repair=%s bizId=%s result=ok errorCode=%d",
			job.code, bizID, res.ErrorCode)
		return
	}
	res, err := s.retryAction(job, yunzhijia.TodoActionType{Read: intPtr(1)}, []string{op.openID})
	if err != nil {
		s.recordFailure(job, op, err)
		return
	}
	log.Printf("notify: op=read channel=im-todo repair=%s openId=%s result=ok errorCode=%d",
		job.code, yunzhijia.MaskOpenID(op.openID), res.ErrorCode)
}

// markState 推进一条任务的状态。两个细节：
//  1. 若当前状态已等于目标，直接跳过——MySQL 的 RowsAffected 对「值未变」的 UPDATE 返回 0，
//     而 store.UpdateNotifyTaskState 把 0 行视为 ErrNoRows，那属良性而非错误；
//  2. 并发场景（同一单据的多个跃迁几乎同时投递，如快速「接单 → 报完工」）下，
//     另一个 worker 可能已把状态推进到目标值，此时 UPDATE 同样影响 0 行 —— 一并按良性处理
//     （最终状态一致，无需报警）。其余错误才记日志。
func (s *NotifyService) markState(job *notifyJob, op notifyOp, prev, target string) {
	if prev == target {
		return
	}
	if err := s.st.UpdateNotifyTaskState(job.repairID, op.empID, job.sourceID, target, ""); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		log.Printf("notify: op=%s repair=%s 更新状态失败 err=%v", op.kind, job.code, err)
	}
}

// deliverDeleteAll 撤单（整单终结）：两种通道**都要清**——灰度期间存量 im-todo 与新 flowcenter
// 待办可能混在同一张单上（§3.2 #9）。
//   - flowcenter：删整个流程实例（instance/delete，连带删该流程下所有待办与抄送）；
//   - im-todo：删该 sourceId 下所有人（不填 openids）。
//
// 二者各自失败只记日志、互不阻断（删不掉的那条留给重试器，本地状态不动）。
func (s *NotifyService) deliverDeleteAll(job *notifyJob, op notifyOp) {
	// flowcenter 侧：仅当流程中心在用时才调（从未启用则无实例，调了只会刷失败日志）。
	if s.flowCenterEnabled {
		if res, err := s.retryDeleteFlowInstance(job.sourceID); err != nil {
			log.Printf("notify: op=delete_all channel=flowcenter repair=%s result=fail err=%v", job.code, err)
		} else {
			log.Printf("notify: op=delete_all channel=flowcenter repair=%s result=ok errorCode=%d", job.code, res.ErrorCode)
		}
	}
	res, err := s.retryAction(job, yunzhijia.TodoActionType{Delete: intPtr(1)}, nil)
	if err != nil {
		// 删除失败：本地状态不动（保留 active/dealt，便于排查与将来重试）。
		log.Printf("notify: op=delete_all channel=im-todo repair=%s result=fail err=%v", job.code, err)
		return
	}
	for _, emp := range op.markDeletedEmpIDs {
		task, gerr := s.st.GetNotifyTask(job.repairID, emp, job.sourceID)
		if gerr != nil || task == nil {
			continue
		}
		_ = s.st.UpdateNotifyTaskState(job.repairID, emp, job.sourceID, model.NotifyStateDeleted, "")
	}
	log.Printf("notify: op=delete_all channel=im-todo repair=%s result=ok errorCode=%d", job.code, res.ErrorCode)
}

// deliverFinishFlow 把整张维修单的**流程实例**标记为「已完成」（结案，instance/finish）。
//
// 只处理流程中心通道：im-todo 没有流程实例概念（planFor 也只在 s.flowCenterEnabled 时才产生此 op）。
// 失败只记日志、**不影响**前面已办结的待办——结案失败不该回滚已经办结的动作，重试器会兜底。
// ⚠️ 待实测：instance/finish 与 todo/done 的先后关系、以及 finish 是否强制关闭未办结节点。
func (s *NotifyService) deliverFinishFlow(job *notifyJob, op notifyOp) {
	// 守卫：仅当该单确实存在 flowcenter 记录时才调 instance/finish。
	// 「待办全由 im-todo 发出、之后开关才打开」的单，其 flowId 在云之家并不存在——
	// 不加守卫会对不存在的 flowId 反复重试（3 次退避）并刷错误日志。
	if !s.hasFlowCenterTask(job.repairID) {
		log.Printf("notify: op=finish_flow repair=%s 跳过结案：该单无流程中心记录（待办可能全由 im-todo 发出）", job.code)
		return
	}
	res, err := s.retryFinishFlowInstance(job.sourceID)
	if err != nil {
		log.Printf("notify: op=finish_flow repair=%s result=fail err=%v", job.code, err)
		return
	}
	log.Printf("notify: op=finish_flow repair=%s result=ok errorCode=%d", job.code, res.ErrorCode)
}

// recordFailure 记录一次投递失败：attempts+1、state=failed、last_error（截断到列宽）。
func (s *NotifyService) recordFailure(job *notifyJob, op notifyOp, err error) {
	msg := truncateErr(err.Error())
	if op.empID > 0 {
		if ierr := s.st.IncrNotifyTaskAttempts(job.repairID, op.empID, job.sourceID, msg); ierr != nil {
			// 记录不存在（如首次即失败且未落库）时先补一行，避免 attempts 丢失。
			_ = s.st.UpsertNotifyTask(&model.NotifyTask{
				RepairID: job.repairID, EmpID: op.empID, OpenID: op.openID,
				SourceID: job.sourceID, Purpose: op.purpose, State: model.NotifyStatePending,
			})
			_ = s.st.IncrNotifyTaskAttempts(job.repairID, op.empID, job.sourceID, msg)
		}
		_ = s.st.UpdateNotifyTaskState(job.repairID, op.empID, job.sourceID, model.NotifyStateFailed, msg)
	}
	log.Printf("notify: op=%s repair=%s openId=%s result=fail err=%s",
		op.kind, job.code, yunzhijia.MaskOpenID(op.openID), msg)
}

// markFailed 把整批操作落库为 failed（队列满 / 服务关闭时用，不阻塞调用方）。
func (s *NotifyService) markFailed(job *notifyJob, reason string) {
	for _, op := range job.ops {
		if op.empID <= 0 {
			continue
		}
		_ = s.st.UpsertNotifyTask(&model.NotifyTask{
			RepairID: job.repairID, EmpID: op.empID, OpenID: op.openID,
			SourceID: job.sourceID, Purpose: op.purpose, State: model.NotifyStatePending,
		})
		_ = s.st.IncrNotifyTaskAttempts(job.repairID, op.empID, job.sourceID, reason)
		_ = s.st.UpdateNotifyTaskState(job.repairID, op.empID, job.sourceID, model.NotifyStateFailed, reason)
	}
}

// createTodo 调云之家 generatetodo（sync 由客户端强制 true）。成功返回结果，失败返回 error。
func (s *NotifyService) createTodo(job *notifyJob, op notifyOp) (*yunzhijia.TodoResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyCallTimeout)
	defer cancel()

	tt := s.todoType
	req := &yunzhijia.TodoRequest{
		AppID:     s.todoAppID,
		SourceID:  job.sourceID,
		URL:       s.deepLinkIMTodo(job.repairID, op.empID),
		Content:   op.content,
		Title:     op.title,
		ItemTitle: op.itemTitle,
		HeadImg:   s.headImg,
		SenderID:  s.senderID,
		TodoType:  &tt, // 显式传 0（通知中心），确定性优于依赖服务端默认
		OpenIDs:   []string{op.openID},
	}
	if s.tagID != "" {
		req.TagID = s.tagID
	}
	res, err := s.yzj.CreateTodo(ctx, req)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return res, fmt.Errorf("create todo rejected: errorCode=%d error=%s", res.ErrorCode, res.Error)
	}
	return res, nil
}

// actionTodo 调云之家 action（流转 / 删除）。流转状态经嵌套 actiontype 传递（K1，客户端已封装）。
func (s *NotifyService) actionTodo(job *notifyJob, at yunzhijia.TodoActionType, openIDs []string) (*yunzhijia.TodoResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyCallTimeout)
	defer cancel()

	req := &yunzhijia.TodoActionRequest{
		SourceType:   s.todoAppID,
		SourceItemID: job.sourceID,
		ActionType:   at,
		OpenIDs:      openIDs,
	}
	res, err := s.yzj.ActionTodo(ctx, req)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return res, fmt.Errorf("action todo rejected: errorCode=%d error=%s", res.ErrorCode, res.Error)
	}
	return res, nil
}

// retryCreate 带指数退避地重试 create。
func (s *NotifyService) retryCreate(job *notifyJob, op notifyOp) (*yunzhijia.TodoResult, error) {
	var res *yunzhijia.TodoResult
	var err error
	for i := 0; i < s.maxAttempts; i++ {
		res, err = s.createTodo(job, op)
		if err == nil {
			return res, nil
		}
		if i < s.maxAttempts-1 && s.backoff > 0 {
			time.Sleep(s.backoff << i)
		}
	}
	return res, err
}

// retryAction 带指数退避地重试 action。
func (s *NotifyService) retryAction(job *notifyJob, at yunzhijia.TodoActionType, openIDs []string) (*yunzhijia.TodoResult, error) {
	var res *yunzhijia.TodoResult
	var err error
	for i := 0; i < s.maxAttempts; i++ {
		res, err = s.actionTodo(job, at, openIDs)
		if err == nil {
			return res, nil
		}
		if i < s.maxAttempts-1 && s.backoff > 0 {
			time.Sleep(s.backoff << i)
		}
	}
	return res, err
}

// deepLinkIMTodo 拼 **im-todo** 待办跳转 URL（§5.1 + §5.4）。前端是 hash 路由
// （createWebHashHistory），深链必须是 `{portal_base_url}/#/repairs/{id}`，不能写成
// `/repairs/{id}` 路径形式。
//
// 在裸链接基础上追加第 2 层免登凭据，形态为：
//
//	{portal_base_url}/#/repairs/{id}?yzj_token={token}
//
// 三个都必须对、且都容易写错的点：
//
//  1. **token 放在 hash 之内**（`#/repairs/100?yzj_token=...`），不是 `#` 之前。
//     片段（fragment）不会被浏览器发进 HTTP 请求行，因此凭据不进服务端访问日志、
//     也不进 Referer —— 这正是 §5.6「凭据早于外部脚本清除」成立的前提。
//     前端 readParam() 两处都读（window.location.search 与 hash query），放 hash 内是
//     更安全的那一种，故选它。
//
//  2. **绑定收件人**：token 的 eid 必须是这张待办的**收件人** empID，不能是别人——
//     否则 A 收到的待办被转发给 B，B 也能免登进来看这张单。
//
//  3. **签发失败必须静默降级为裸链接**，绝不能因为第 2 层不可用就让待办发不出去：
//     未配置 login_token_secret（第 2 层按设计静默禁用）是最常见的情况，不记日志；
//     其它失败（empID/repairID 非法）才是真问题，记一条日志以便定位。
//     降级后第 0 层（JS 桥 ticket）与第 3 层（登录页回跳）仍然可用。
func (s *NotifyService) deepLinkIMTodo(repairID, empID int64) string {
	base := s.portalBaseURL + "/#/repairs/" + strconv.FormatInt(repairID, 10)
	tok, err := mintDeeplinkToken(s.deeplinkSecret, s.deeplinkTTL, empID, repairID)
	if err != nil {
		if !errors.Is(err, errDeeplinkDisabled) {
			log.Printf("签发深链免登 token 失败，待办已降级为不带 token 的链接: repairID=%d empID=%d err=%v",
				repairID, empID, err)
		}
		return base
	}
	return base + "?yzj_token=" + url.QueryEscape(tok)
}

// deepLinkFlowCenter 拼 **流程中心**待办跳转 URL：**裸链接**，不嵌 yzj_token。
//
// 为什么不嵌 yzj_token：流程中心在用户点击跳转时会**自动追加** `?ticket=xxx`，用 ticket 能换到
// **完整权限会话**；而 yzj_token 走 deeplink-login 只能换到 readonly:true 的**只读会话**——
// 维修工在只读会话下根本没法接单 / 报完工，这正是要换掉 im-todo 深链的原因。
//
// 形态仍是 hash 路由 `{portal_base_url}/#/repairs/{id}`（片段内），不是 `/repairs/{id}` 路径形式。
// flowcenter 的 WebLink / MobileLink / WebFlowLink / MobileFlowLink 都填它（P0 不区分跳转目标）。
func (s *NotifyService) deepLinkFlowCenter(repairID int64) string {
	return s.portalBaseURL + "/#/repairs/" + strconv.FormatInt(repairID, 10)
}

// WaitIdle 等待队列排空（所有已入队任务处理完毕）；返回是否在超时前排空。供测试与优雅关闭使用。
func (s *NotifyService) WaitIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if atomic.LoadInt64(&s.inflight) == 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return atomic.LoadInt64(&s.inflight) == 0
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Stop 优雅关闭（§7.4）：停止接收新任务 → 等待在途投递完成（带超时上限）。
// 超时未完成的任务已在 repair_notify_task 里留痕（pending/failed），不会静默丢失。
func (s *NotifyService) Stop(timeout time.Duration) {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		log.Printf("notify: 通知服务已停止（在途任务已处理完）")
	case <-time.After(timeout):
		log.Printf("notify: 通知服务停止超时（%s），放弃剩余在途任务（已落 repair_notify_task）", timeout)
	}
}

// intPtr 取 int 的地址（构造 TodoActionType 的 *int 字段用）。
func intPtr(v int) *int { return &v }

// truncateErr 把错误信息截断到 last_error 列宽（按字符数）。
func truncateErr(s string) string {
	r := []rune(s)
	if len(r) <= notifyErrMaxLen {
		return s
	}
	return string(r[:notifyErrMaxLen])
}
