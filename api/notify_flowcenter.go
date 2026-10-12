package api

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"asset-mgr/integration/yunzhijia"
	"asset-mgr/model"
)

// 本文件是「统一流程中心（cloudflow）」通道的编排层实现（A 方案）。
//
// 与 im-todo 通道（见 notify.go / notify_plan.go）并存，切换由**每条待办记录自己的 channel**
// 决定：**新建**待办看当前开关 s.flowCenterEnabled，**办结 / 删除**已有待办看 task.Channel。
// 原因见 docs 文档「必读」第 2 条：切换前已用 im-todo 发出的存量待办仍在用户手里，必须继续用
// im-todo 的 action 接口办结；新发的是流程中心待办，必须用 done 接口。任何「只看全局开关」的
// 写法都会让其中一半永远办不掉。

// 待办通道标识（repair_notify_task.channel）。取值即落库字符串，**不要**改字面量。
//
// 值定义在 model（store 落库 / 读回也要用同一个值），这里只做别名，避免两处字面量各自漂移。
const (
	notifyChannelIMTodo     = model.NotifyChannelIMTodo
	notifyChannelFlowCenter = model.NotifyChannelFlowCenter
)

// newTaskChannel 返回**新建**待办应走的通道：由当前开关 s.flowCenterEnabled 决定。
//
// 注意：**办结 / 删除**已有待办不看它，看那条记录自己的 task.Channel（见本文件顶部说明）。
// 这是整个迁移里最容易写错的一点：把「新建选通道」和「办结选通道」用同一个判断，
// 会让切换前用 im-todo 发出的存量待办在云之家永远转不成已办。
func (s *NotifyService) newTaskChannel() string {
	if s.flowCenterEnabled {
		return notifyChannelFlowCenter
	}
	return notifyChannelIMTodo
}

// shouldUseFlowCenter 判断「办结 / 删除」一条**已有**待办时该走哪个通道：看记录自己的 channel。
//
// 这是最容易写错、且写错后**静默失效**的一处（用户只看到「待办一直在那儿」）。故抽成纯函数
// 单独测（见 notify_flowcenter_dispatch_test.go）。
// nil（无记录）按 im-todo 处理——但调用方通常已先判 nil 并 return。
func shouldUseFlowCenter(task *model.NotifyTask) bool {
	return task != nil && task.Channel == notifyChannelFlowCenter
}

// flowBizIDOf 取一条 flowcenter 待办记录自己的 bizId；为空返回 ("", false)。
//
// 空 bizId **不代表**「没发成功」——bizId 是确定性算出来的，发送前就已落库（见 flowBizID）。
// 实际只可能来自：im-todo 通道的记录、或历史脏数据。防御仍有价值（防脏数据），
// 但调用方不应据此推断发送结果。
//
// 无论如何，**绝不能**拿空串去调 done/delete——那会发一个 `[""]` 的请求。
func flowBizIDOf(task *model.NotifyTask) (string, bool) {
	if task == nil || task.BizID == "" {
		return "", false
	}
	return task.BizID, true
}

// 流程中心固定参数（P0）。
const (
	// flowUrgency 紧急程度，取值 1–5；P0 统一 3（居中，不过度打扰）。
	flowUrgency = 3
	// flowMessageMode 任务消息模型：标题 + 消息都显示（标题优先）。
	flowMessageMode = "MODE_TITLE_MESSAGE"
	// 本集成**刻意不启用**节点超时（SendFlowTodoRequest.TTL 保持 nil → 请求体不带该字段）。
	// 设计原则：待办的生命周期由业务动作驱动，不由时间驱动——用户要求「待办任务需要
	// 处理人执行后消失」，超时属于「由时间驱动消失」，与该原则直接冲突，需重新拍板。
	//
	// ⚠️ 请勿「顺手」给 SendFlowTodoRequest 加 TTL/TTLType：那是产品行为的改变，不是重构。
)

// flowActivityNames 是 purpose（英文动作码）→ 流程中心节点中文名的映射表。
//
// purpose 既用作 activityId（英文，稳定），也用于拼 bizId（节点级唯一）。
// 中文名仅供流程中心「我的待办」列表展示。
var flowActivityNames = map[string]string{
	"intake":   "待受理",
	"dispatch": "待接单",
	"take":     "维修中",
	"confirm":  "待确认",
	"reject":   "已驳回",
	"return":   "已退回",
	"cancel":   "已取消",
	"done":     "已完工",
	"accept":   "已受理",
}

// flowActivityName 把 purpose 映射为节点中文名。
// 未命中回退 purpose 原文（不 panic、不返回空串）——未知动作也要能发出可读的节点名，
// 而不是让整条待办发不出去。
func flowActivityName(purpose string) string {
	if name, ok := flowActivityNames[purpose]; ok {
		return name
	}
	return purpose
}

// flowBizID 生成流程中心的 bizId：`{sourceId}-{empID}-{purpose}`，例如 `repair-1409-88-dispatch`。
//
// 为什么是「节点级」而非「单号级」：A 方案下同一维修工会先后拿到「待接单」与「维修中」两个待办，
// 二者必须能分别办结，故 bizId 必须含 purpose。长度约 45 字符，远小于列宽 128。
//
// 注意：bizId 是**确定性**的（发送前即可算出），故可在 Upsert 时直接落库，无需发送成功后再回写。
func flowBizID(sourceID string, empID int64, purpose string) string {
	return sourceID + "-" + strconv.FormatInt(empID, 10) + "-" + purpose
}

// pickFlowCreator 从候选里选流程中心的流程发起人（creator），返回取值与来源。
//
// 优先级：**报修人 openId → 配置的 sender_id**。
//
// 为什么优先报修人：creator 的语义是「流程发起人」。维修单是报修人发起的，
// 以报修人名义发起语义正确，报修人也能在云之家「我发起的」里看到自己的单。
//
// 为什么必须保留兜底：生产实测在职 131 人中只有 101 人有 openId（77.1%），
// 报修人可能恰好没绑定；而 creator 是**必填项**，空值会被云之家拒绝
// （2026-09-23 实测 `errorCode=1101030`「creator不能为空」）⇒ 没有兜底就等于整条待办发不出去。
//
// 抽成**纯函数**（不碰 store / 网络）以便在集成测试被安全阀 SKIP 的环境下仍能直接单测
// ——与 shouldSendInformationalNotice 同一理由：这条优先级必须有测试钉住。
func pickFlowCreator(reporterOpenID, senderID string) (creator, source string) {
	if reporterOpenID != "" {
		return reporterOpenID, "reporter"
	}
	if senderID != "" {
		return senderID, "sender_id"
	}
	return "", "none"
}

// resolveCreatorOpenID 解析流程中心的 creator：优先报修人，兜底 sender_id。
//
// 只被 sendFlowTodo 调用（im-todo 通道不需要 creator），所以这次 DB 查询
// **只在流程中心通道真的发待办时才会发生**。
func (s *NotifyService) resolveCreatorOpenID(reporterEmpID int64) string {
	var reporterOpenID string
	if reporterEmpID > 0 {
		openID, err := s.st.EmployeeOpenID(reporterEmpID)
		if err != nil {
			log.Printf("notify: 查询报修人 open_id 失败 emp=%d err=%v（将走 sender_id 兜底）", reporterEmpID, err)
		} else {
			reporterOpenID = openID
		}
	}

	creator, source := pickFlowCreator(reporterOpenID, s.senderID)
	switch source {
	case "sender_id":
		log.Printf("notify: 报修人 emp=%d 无云之家 openId，流程中心 creator 兜底为 sender_id", reporterEmpID)
	case "none":
		log.Printf("notify: 报修人 emp=%d 无云之家 openId 且 sender_id 未配置 —— "+
			"流程中心待办会因 creator 为空被拒绝（errorCode=1101030）", reporterEmpID)
	}
	return creator
}

// sendFlowTodo 通过流程中心发送一条待办（todo/send）。
//
// flowId 用 job.sourceID（= "repair-{id}"，单号级，一张维修单一个流程实例）；
// activityId 用 op.purpose（节点级）；bizId 用 flowBizID（节点级）。
func (s *NotifyService) sendFlowTodo(job *notifyJob, op notifyOp) (*yunzhijia.FlowResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyCallTimeout)
	defer cancel()

	// 流程中心会**自动**在跳转地址后追加 ?ticket=xxx（换完整权限会话），
	// 故这里用裸链接，绝不嵌 yzj_token（那只能换只读会话）。
	link := s.deepLinkFlowCenter(job.repairID)

	// creator（流程发起人）：优先**报修人**，报修人无 openId 时兜底到配置的 sender_id。
	// 见 resolveCreatorOpenID / pickFlowCreator。
	//
	// ⚠️ creator 是**必填项**：2026-09-23 实测空值被云之家直接拒绝
	//   {"errorCode":1101030,"error":"creator不能为空","success":false}
	// 即「照常发送」实际是必然失败（3 次退避重试后落库 state=failed）。
	// 注意 MissingFlowCenterConfig（config/config.go:410）**不检查该项**，启动防御拦不住，
	// 故此处再留一道显式日志：两个来源都为空时，日志里能一眼看出根因。
	creator := s.resolveCreatorOpenID(job.reporterEmpID)
	if creator == "" {
		log.Printf("notify: 流程中心待办 creator 为空（报修人 emp=%d 无 openId 且 sender_id 未配置），"+
			"云之家会拒绝该请求（errorCode=1101030）repair=%s", job.reporterEmpID, job.code)
	}
	req := &yunzhijia.SendFlowTodoRequest{
		WebLink:        link,
		MobileLink:     link,
		WebFlowLink:    link, // P0 不区分「待办跳转」与「流程监控跳转」，均填同一裸链接（待实测）
		MobileFlowLink: link,
		AppID:          s.todoAppID,
		Todo: []yunzhijia.SendFlowTodoItem{{
			Approver:       op.openID,
			BizID:          flowBizID(job.sourceID, op.empID, op.purpose),
			MessageMode:    flowMessageMode,
			MessageContent: op.content,
		}},
		TypeID:       s.flowCenterTypeID,
		Urgency:      flowUrgency,
		FlowID:       job.sourceID,
		Title:        op.title,
		SerialNo:     job.code,
		Creator:      creator,
		ActivityID:   op.purpose,
		ActivityName: flowActivityName(op.purpose),
	}
	res, err := s.yzj.SendFlowTodo(ctx, req)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return res, fmt.Errorf("flowcenter send todo rejected: errorCode=%d error=%s", res.ErrorCode, res.Error)
	}
	return res, nil
}

// doneFlowTodo 把一条流程中心待办标记为已办（todo/done）。
func (s *NotifyService) doneFlowTodo(bizID string) (*yunzhijia.FlowResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyCallTimeout)
	defer cancel()
	res, err := s.yzj.DoneFlowTodo(ctx, &yunzhijia.FlowBizRequest{
		AppID: s.todoAppID,
		BizID: []string{bizID},
	})
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return res, fmt.Errorf("flowcenter done todo rejected: errorCode=%d error=%s", res.ErrorCode, res.Error)
	}
	return res, nil
}

// deleteFlowTodo 删除单条流程中心待办（todo/delete，按 bizId）。
// 用于「同一节点重新激活」（先删后建）——删掉旧的已办待办，再用同一 bizId 重发。
func (s *NotifyService) deleteFlowTodo(bizID string) (*yunzhijia.FlowResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyCallTimeout)
	defer cancel()
	res, err := s.yzj.DeleteFlowTodo(ctx, &yunzhijia.FlowBizRequest{
		AppID: s.todoAppID,
		BizID: []string{bizID},
	})
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return res, fmt.Errorf("flowcenter delete todo rejected: errorCode=%d error=%s", res.ErrorCode, res.Error)
	}
	return res, nil
}

// deleteFlowInstance 删除整个流程实例（instance/delete）——连带删除该流程下所有待办与抄送。
// 撤单（整单终结）走它，而不是逐条 delete todo。
func (s *NotifyService) deleteFlowInstance(flowID string) (*yunzhijia.FlowResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyCallTimeout)
	defer cancel()
	res, err := s.yzj.DeleteFlowInstance(ctx, &yunzhijia.FlowInstanceRequest{
		AppID:  s.todoAppID,
		FlowID: []string{flowID},
	})
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return res, fmt.Errorf("flowcenter delete instance rejected: errorCode=%d error=%s", res.ErrorCode, res.Error)
	}
	return res, nil
}

// retrySendFlowTodo 带指数退避地重试 send（与 retryCreate 同策略）。
func (s *NotifyService) retrySendFlowTodo(job *notifyJob, op notifyOp) (*yunzhijia.FlowResult, error) {
	var res *yunzhijia.FlowResult
	var err error
	for i := 0; i < s.maxAttempts; i++ {
		res, err = s.sendFlowTodo(job, op)
		if err == nil {
			return res, nil
		}
		if i < s.maxAttempts-1 && s.backoff > 0 {
			time.Sleep(s.backoff << i)
		}
	}
	return res, err
}

// retryDoneFlowTodo 带指数退避地重试 done。
func (s *NotifyService) retryDoneFlowTodo(bizID string) (*yunzhijia.FlowResult, error) {
	var res *yunzhijia.FlowResult
	var err error
	for i := 0; i < s.maxAttempts; i++ {
		res, err = s.doneFlowTodo(bizID)
		if err == nil {
			return res, nil
		}
		if i < s.maxAttempts-1 && s.backoff > 0 {
			time.Sleep(s.backoff << i)
		}
	}
	return res, err
}

// retryDeleteFlowTodo 带指数退避地重试 todo/delete（按 bizId）。
func (s *NotifyService) retryDeleteFlowTodo(bizID string) (*yunzhijia.FlowResult, error) {
	var res *yunzhijia.FlowResult
	var err error
	for i := 0; i < s.maxAttempts; i++ {
		res, err = s.deleteFlowTodo(bizID)
		if err == nil {
			return res, nil
		}
		if i < s.maxAttempts-1 && s.backoff > 0 {
			time.Sleep(s.backoff << i)
		}
	}
	return res, err
}

// finishFlowInstance 标记整个流程实例「已完成」（instance/finish）——结案。
//
// 与 deleteFlowInstance 的区别：finish 是「正常走完」，delete 是「撤销 / 终结」。
// confirm（确认完工）是业务终态（结案），用 finish；撤单（cancel）才是 delete。
// 不调 finish 的话，各人待办虽被 done 办结，但**流程实例在云之家侧会永远停在「进行中」**，
// 发起人视图里挂着一张永不结束的流程——「处理人执行后消失」就只做到了一半。
//
// ✅ 已实测（2026-09-23，探针 cmd/zzflowprobe，见 docs §11）：
//   - finish **不校验未办结节点**：实例内仍有未办结节点时调用，仍返回 success:true 直接结案。
//     故「先逐个 deal 再 finish」不是硬约束，保持现状即可（更保守，无副作用）。
//   - 结案后该实例的待办从「我的待办」消失 ⇒ finish 是待办消失的可靠手段。
//   - 另注：待办列表按 flowId 聚合（一实例一条，标题为最近一次 send 的内容），
//     故验证流转是否正确要看**标题里的节点名**，不能数条数。
func (s *NotifyService) finishFlowInstance(flowID string) (*yunzhijia.FlowResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyCallTimeout)
	defer cancel()
	res, err := s.yzj.FinishFlowInstance(ctx, &yunzhijia.FlowInstanceRequest{
		AppID:  s.todoAppID,
		FlowID: []string{flowID},
	})
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return res, fmt.Errorf("flowcenter finish instance rejected: errorCode=%d error=%s", res.ErrorCode, res.Error)
	}
	return res, nil
}

// retryFinishFlowInstance 带指数退避地重试 instance/finish（结构照抄 retryDeleteFlowInstance）。
func (s *NotifyService) retryFinishFlowInstance(flowID string) (*yunzhijia.FlowResult, error) {
	var res *yunzhijia.FlowResult
	var err error
	for i := 0; i < s.maxAttempts; i++ {
		res, err = s.finishFlowInstance(flowID)
		if err == nil {
			return res, nil
		}
		if i < s.maxAttempts-1 && s.backoff > 0 {
			time.Sleep(s.backoff << i)
		}
	}
	return res, err
}

// retryDeleteFlowInstance 带指数退避地重试 instance/delete。
func (s *NotifyService) retryDeleteFlowInstance(flowID string) (*yunzhijia.FlowResult, error) {
	var res *yunzhijia.FlowResult
	var err error
	for i := 0; i < s.maxAttempts; i++ {
		res, err = s.deleteFlowInstance(flowID)
		if err == nil {
			return res, nil
		}
		if i < s.maxAttempts-1 && s.backoff > 0 {
			time.Sleep(s.backoff << i)
		}
	}
	return res, err
}

// shouldDeleteForReactivate 判断「重新通知同一人」前是否要先删掉旧待办。
//
// 两个通道的语义不同，绝不能共用同一个条件：
//   - im-todo：一人一单只有一条待办，dealt 的也要删掉再建（否则新待办建不出来）；
//   - flowcenter：**只删 active 的**（未办结的旧待办要清掉）；**dealt 的绝不删**——
//     那是已办结的节点，删掉就丢了流程留痕。
//
// ⚠️ 理由更正（2026-09-23 实测，探针 cmd/zzflowprobe）：本函数原先写的理由是
// 「不删 active 的会导致**同一人手上同时挂两条**」——**该理由已被实测推翻**：
// 「我的待办」按 `flowId` 聚合，一个实例只有一条卡片（后发的顶替先发的），
// 同一人根本不可能看到两条。
//
// 但**结论保持不变**（flowcenter 下只删 active、保留 dealt），理由改为：
//  1. 未办结的旧节点会让实例停在未完结状态，流程语义不干净；
//  2. 云之家其他视图/角标（首页待办数、已办列表）是否仍会显示该未办结节点 —— 尚未实测。
//
// 这是 A 方案「节点留痕」成立的关键：接单时先办结「待接单」再发「维修中」，
// 若此处把已办结（dealt）的「待接单」当 im-todo 那样删掉，流程记录里就少一个环节。
// nil（无记录）不删。
func (s *NotifyService) shouldDeleteForReactivate(task *model.NotifyTask) bool {
	if task == nil {
		return false
	}
	if shouldUseFlowCenter(task) {
		return task.State == model.NotifyStateActive
	}
	return task.State == model.NotifyStateActive || task.State == model.NotifyStateDealt
}

// hasFlowCenterTask 判断某维修单是否**确实存在**流程中心待办记录。
//
// 用于 confirm 结案的守卫：planFor 是纯函数、查不了库，故守卫必须在 deliver 阶段。
// 「待办全由 im-todo 发出、之后开关才打开」的单，其 flowId 在云之家并不存在；
// 若不加守卫，retryFinishFlowInstance 会带 3 次退避重试去打 3 次不存在的 flowId + 3 条错误日志。
//
// 直接用 s.st.DB() 查询（与 api/notify_plan.go 的 assetManagerEmpIDs 同一做法），不新增 store 方法。
func (s *NotifyService) hasFlowCenterTask(repairID int64) bool {
	var one int
	err := s.st.DB().QueryRow(
		`SELECT 1 FROM repair_notify_task WHERE repair_id = ? AND channel = ? LIMIT 1`,
		repairID, model.NotifyChannelFlowCenter).Scan(&one)
	return err == nil
}

// deleteForReactivate 在「重新通知同一个人」（先删后建）前删掉其**旧**待办。
//
// 按该记录**自己的 channel** 分派：若切换前用 im-todo 发出的旧待办被错误地用流程中心接口删，
// 会删不掉（静默失效，用户那条旧待办一直在）。故这里绝不能看全局开关。
// 是否需要删由调用方先经 shouldDeleteForReactivate 判定（flowcenter 下 dealt 的不删）。
func (s *NotifyService) deleteForReactivate(job *notifyJob, task *model.NotifyTask, op notifyOp) error {
	if shouldUseFlowCenter(task) {
		bizID, ok := flowBizIDOf(task)
		if !ok {
			return nil // bizId 为空（脏数据 / 非 flowcenter 记录），无从删起
		}
		_, err := s.retryDeleteFlowTodo(bizID)
		return err
	}
	_, err := s.retryAction(job, yunzhijia.TodoActionType{Delete: intPtr(1)}, []string{op.openID})
	return err
}
