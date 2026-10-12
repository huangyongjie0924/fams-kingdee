package api

import (
	"log"
	"strconv"

	"asset-mgr/model"
)

// 本文件实现触发矩阵（docs/云之家待办通知集成方案.md §3.2，共 10 行，逐行对应）。
//
// 收件人 openId 由 employee.open_id 解析；**查不到 openId 则跳过该人并记日志，不报错**。
// 全部使用 todoType=0（通知中心，应用已被勾选为「通知类」），不出现 todoType=1。
//
// 矩阵行 → 本文件实现：
//	#1  submit   受理人 create                                  → case submit
//	#2  accept   **默认不产生通知**：受理人保持未办；报修人「已受理」属 P1 进展告知，
//	             与 #5/#7 一样挂 informational 开关，P0 默认关闭          → case accept
//	#3  reject   受理人 deal{read,deal} + 报修人 create              → case reject
//	#4  dispatch 受理人 deal{read,deal} + 维护人员 create（VendorID>0 仅跳过维护人员） → case dispatch
//	#5  take     im-todo：维护人员 read{read:1}（本地保持 active）；流程中心（A 方案）：
//	             办结「待接单」节点 + 新发「维修中」节点（+报修人信息性，开关控制） → case take
//	#6  finish   维护人员 deal{read,deal} + 报修人 create               → case finish
//	#7  confirm  报修人 + 维护人员 + 全体受理人 deal（结案）（+维护人员信息性，开关控制） → case confirm
//	#8  return   报修人 deal{deal} + 维护人员重新激活（先删后建）           → case return
//	#9  cancel   删除该 sourceId 下所有人（不填 openids）；跃迁前状态≠pending
//	             （管理员取消，产品定义行 11）再给报修人 create「已被管理员取消」 → case cancel
//	#10 approve/scrap：P1，P0 不产生这两个跃迁，故不实现。
//
// 实现行数：9 行（#1–#9）；#4 的外部供应商分支为「仅跳过维护人员通知」；#10 属 P1。
//
// ⚠️ 关于 #2 / #4 / #5 / #7：产品定义（docs/维修待办通知-产品定义.md 行 2/3/5/7 与事件 13/14/15）
// 与技术方案 §3.2 在这几行上不一致，**以产品定义为准**（§3.2 该版有误，且 #2/#5 的错误会造成
// 「受理后到派工之间」「接单后到报完工之间」无人持有活跃待办的静默漏洞）。修正理由见各 case 注释。
//
// 关于「信息性告知」（#2/#5/#7 三处给收件人的进展告知）：其开关已从编译期常量改为**配置项**
// yunzhijia.informational_enabled（见 config/config.go 的 InformationalEnabled 字段注释，
// 以及本文件末尾的 ensureReadOp / informationalNotice）。

// notifyKind 是待办操作类型（对应 §4.3 状态机的三种动作）。
type notifyKind string

const (
	// opEnsure：确保该人有一条「未办」待办。无记录 → create；已有 active/dealt → 先删后建。
	opEnsure notifyKind = "create"
	// opDeal：把该人当前待办流转为已办（{read:1,deal:1} 或 {deal:1}）。
	opDeal notifyKind = "deal"
	// opRead：只把该人待办置「已读」（云之家侧标记），**本地状态保持 active 不变**——
	// 「已读」不是本地状态机的节点，只表示提醒已被看到、待办仍未办完（如维护人员接单后仍在维修）。
	opRead notifyKind = "read"
	// opDeleteAll：删除该 sourceId 下所有人（撤单，**不填 openids**）。
	opDeleteAll notifyKind = "delete_all"
	// opFinishFlow：把**整个流程实例**标记为「已完成」（结案，instance/finish）。
	// 整单级、不带 empID（与 opDeleteAll 同构）。仅流程中心通道产生——
	// im-todo 没有「流程实例」概念（见 notify_plan.go confirm 分支）。
	opFinishFlow notifyKind = "finish_flow"
)

// notifyOp 是一条待办操作：目标收件人 + 已拼好的文案 + 操作类型。
type notifyOp struct {
	kind      notifyKind
	empID     int64  // 目标员工（opDeleteAll 时为 0）
	openID    string // 已解析的云之家 openId（空 → worker 跳过并记日志）
	purpose   string // intake/dispatch/confirm/return…（写进 repair_notify_task.purpose）
	title     string
	content   string
	itemTitle string
	// withRead：是否要在本条操作里「置已读」。
	//   - opDeal：是否同时置已读（true → {read:1,deal:1}；false → {deal:1}）；
	//   - opEnsure：信息性告知（产品定义 Q2）——新建成功后**立刻** action{read:1} 置已读，
	//     避免「推了但打扰用户」（见 deliverEnsure 与 ensureReadOp）。
	withRead bool
	// markDeletedEmpIDs：opDeleteAll 时，本地需要置 deleted 的收件人（删整批后逐人落库）。
	markDeletedEmpIDs []int64
}

// notifySourceID 是云之家待办的唯一键来源（§4.2）：稳定、可读、可反查回 repair_order.id。
func notifySourceID(repairID int64) string {
	return "repair-" + strconv.FormatInt(repairID, 10)
}

// planFor 按 §3.2 触发矩阵解析出待办操作清单（收件人 + 文案 + 操作）。
// 在**业务事务之外**调用（挂载点已保证在 CreateRepair/Transition 提交之后）。
//
// prevStatus 是跃迁前状态（见 OnRepairTransition 注释），仅 cancel 分支使用。
func (s *NotifyService) planFor(o *model.RepairOrder, action, remark, prevStatus string) []notifyOp {
	if o == nil {
		return nil
	}
	var ops []notifyOp

	switch action {
	case model.RepairActionSubmit:
		// #1 提交报修 → 全体资产管理员 create。
		title, content := intakeCopy(o)
		for _, emp := range s.assetManagerEmpIDs() {
			ops = append(ops, s.ensureOp(o, emp, "intake", title, content))
		}

	case model.RepairActionAccept:
		// #2 受理：**默认不产生任何通知**（产品定义事件 13 / 行 2）。
		//
		// 受理人待办保持「未办」——他受理后的下一步是**派工**，任务未结束，绝不能标已办；
		// 待办由 dispatch 分支关闭（产品定义行 3 / AC-2）。这保证「受理后到派工之间」始终有人
		// 持有活跃待办，否则受理人忘了派工，这张单会静默烂在 accepted 里（漏洞 1）。
		// 报修人「报修已受理」是 P1 进展告知（产品定义行 2），与 #5/#7 一样挂 informational 开关。
		if s.informational {
			ops = append(ops, s.informationalNotice(o, o.ReporterEmpID, "accept", acceptedCopy)...)
		}

	case model.RepairActionReject:
		// #3 驳回 → 受理人待办流转 {read:1,deal:1}；报修人 create「已驳回」。
		for _, emp := range s.assetManagerEmpIDs() {
			ops = append(ops, s.dealOp(o, emp, "intake", true))
		}
		title, content := rejectedCopy(o, remark)
		ops = append(ops, s.ensureOp(o, o.ReporterEmpID, "reject", title, content))

	case model.RepairActionDispatch:
		// #4 派工 → 先关闭受理人待办，再通知维护人员（产品定义行 3 / AC-2：派工成功 → 管理员那条转已办）。
		// 外部维保供应商（VendorID>0）无 openId，仅跳过**维护人员**通知；受理人那条同样要关。
		for _, emp := range s.assetManagerEmpIDs() {
			ops = append(ops, s.dealOp(o, emp, "intake", true))
		}
		if o.VendorID > 0 {
			log.Printf("notify: skip repair=%s dispatch 外部供应商 vendor=%d 无 openId", o.Code, o.VendorID)
			break
		}
		title, content := dispatchedCopy(o)
		ops = append(ops, s.ensureOp(o, o.AssigneeEmpID, "dispatch", title, content))

	case model.RepairActionTake:
		// #5 接单。
		//
		// im-todo（开关关）：维护人员待办**只置已读**（{read:1}），本地状态保持 active（产品定义事件 14 / 行 5）。
		//   标已办等于「接单即完结」——维护人员在维修期间会没有任何待办，忘了报完工就无人被提醒（漏洞 2）。
		//   真正的关闭点是 finish（事件 15：报完工才标已办）。
		//
		// 流程中心（A 方案，开关开）：流程中心**没有「只置已读」**这个动作，若接单即 done 会复现漏洞 2
		//   （维修期间无待办）。故用「关旧节点 + 开新节点」承接：先办结维护人员的「待接单」节点，
		//   再给同一人发「维修中」节点——后者才是他在维修期间的活跃待办（见 flowActivityNames）。
		if s.flowCenterEnabled {
			ops = append(ops, s.dealOp(o, o.AssigneeEmpID, "dispatch", true))
			title, content := takeAckCopy(o)
			ops = append(ops, s.ensureOp(o, o.AssigneeEmpID, "take", title, content))
		} else {
			ops = append(ops, s.readOp(o, o.AssigneeEmpID, "dispatch"))
		}
		if s.informational {
			ops = append(ops, s.informationalNotice(o, o.ReporterEmpID, "take", repairingCopy)...)
		}

	case model.RepairActionFinish:
		// #6 报完工 → 维护人员待办流转 {read:1,deal:1}；报修人 create「待确认」。
		ops = append(ops, s.dealOp(o, o.AssigneeEmpID, "dispatch", true))
		title, content := confirmingCopy(o)
		ops = append(ops, s.ensureOp(o, o.ReporterEmpID, "confirm", title, content))

	case model.RepairActionConfirm:
		// #7 确认完工 → **结案**：报修人 + 被指派维护人员 + 全体受理人三方全部标已办
		// （产品定义行 7 / AC-5：不再有待办挂在任何人的待办列表里）。
		// 已 dealt 的人由 deliverDeal 的幂等守卫退化为 no-op，故这里直白写即可。
		ops = append(ops, s.dealOp(o, o.ReporterEmpID, "confirm", true))
		ops = append(ops, s.dealOp(o, o.AssigneeEmpID, "dispatch", true))
		for _, emp := range s.assetManagerEmpIDs() {
			ops = append(ops, s.dealOp(o, emp, "intake", true))
		}
		// informational 控制的是「新建一条信息性（已读）待办」，与「把旧的标已办」是两件事，故并存。
		if s.informational {
			ops = append(ops, s.informationalNotice(o, o.AssigneeEmpID, "done", doneCopy)...)
		}
		// 流程结案（仅流程中心）：**必须排在所有人 dealOp 之后**（放在本 case 末尾）——
		// 先把各人待办办结，再标记流程完成；反过来的话，若 instance/finish 会让云之家强制
		// 关闭未办结的节点，可能干扰 deal 的幂等判定（deliverDeal 会因待办已消失而查不到记录）。
		//
		// 判断用当前开关 s.flowCenterEnabled：这是「新建」语义（流程中心在用时才有实例可结），
		// 不是「办结已有待办」语义——后者才看记录自己的 channel。
		if s.flowCenterEnabled {
			ops = append(ops, s.finishFlowOp(o))
		}

	case model.RepairActionReturn:
		// #8 退回 → 报修人待办流转 {deal:1}；维护人员待办重新激活（先删后建）。
		ops = append(ops, s.dealOp(o, o.ReporterEmpID, "confirm", false))
		if s.flowCenterEnabled {
			// 流程中心下，维护人员手上那条是「维修中」节点（take 建的）。
			// 退回只可能发生在 confirming（见 store/repair.go 的跃迁表），而 finish 已办结过它，
			// 故正常路径下这条 deal 是 no-op（deliverDeal 对 dealt 短路）。
			// 保留它是**安全网**：若 finish 那次 done 失败（state=failed），它会把旧节点补办结，
			// 避免旧节点既未办结、又被随后的 ensure 覆盖 biz_id 而成为孤儿（见 deliverEnsure 已知限制）。
			// purpose 写 "take" 只用于日志/落库；实际办结哪条由 task.BizID 决定，
			// 故对「记录里指向的是哪个节点」不敏感，对存量记录同样安全。
			ops = append(ops, s.dealOp(o, o.AssigneeEmpID, "take", true))
		}
		title, content := returnedCopy(o, remark)
		ops = append(ops, s.ensureOp(o, o.AssigneeEmpID, "return", title, content))

	case model.RepairActionCancel:
		// #9 撤单 → 删除该 sourceId 下所有人（不填 openids）。
		//
		// 产品定义把撤单拆成两行（action 同为 cancel，靠**跃迁前状态**区分）：
		//   行 10（报修人撤单，pending→cancelled）：全体管理员标已办，无需通知报修人本人；
		//   行 11（管理员取消，accepted/dispatched→cancelled，**P0**）：报修人此前可能正持有
		//         活跃待办（派工 / 报完工后），必须让他知道「报修已被管理员取消」。
		//
		// 先 delete-all 清场（覆盖行 10：删掉=终结，效果等价），再按 prevStatus 分流：
		//   非 pending（管理员取消）→ 给报修人**重新 create** 一条「已被管理员取消」；
		//   若不分流、只 delete-all，则会把报修人那条一并删掉，他永远不知道被取消
		//   ——这正是行 11 要堵的缺口。
		// 顺序关键：先 delete-all 再 create——deliverDeleteAll 会把报修人本地状态置 deleted，
		// 随后 deliverEnsure 读到的正是 deleted（非 active/dealt，无需再删），create 后置 active，
		// 报修人最终恰好一条「已取消」待办，不残留悬挂项。
		ops = append(ops, s.deleteAllOp(o))
		if prevStatus != model.RepairPending {
			title, content := cancelledCopy(o)
			ops = append(ops, s.ensureOp(o, o.ReporterEmpID, "cancel", title, content))
		}

	default:
		// #10 approve/scrap 属 P1；未知动作不产生通知。
		log.Printf("notify: repair=%s action=%q 无对应通知（P0 不产生该跃迁）", o.Code, action)
	}

	return ops
}

// ensureOp 构造「确保该人有一条未办待办」的操作。
func (s *NotifyService) ensureOp(o *model.RepairOrder, empID int64, purpose, title, content string) notifyOp {
	return notifyOp{
		kind:      opEnsure,
		empID:     empID,
		openID:    s.resolveOpenID(empID),
		purpose:   purpose,
		title:     title,
		content:   content,
		itemTitle: itemTitleOf(o),
	}
}

// ensureReadOp 构造「新建一条**已读**待办」的操作（信息性告知专用，见产品定义 Q2）。
//
// 与 ensureOp 的唯一区别是 withRead=true：创建成功后立刻 action{read:1} 置已读。
// 为什么必须两步：云之家 generatetodo 的 params 每项只有 openId/url/content，**没有 read 字段**，
// 「新建即已读」只能 create 成功后紧跟一条 action{read:1}（见 deliverEnsure）。
func (s *NotifyService) ensureReadOp(o *model.RepairOrder, empID int64, purpose, title, content string) notifyOp {
	op := s.ensureOp(o, empID, purpose, title, content)
	op.withRead = true
	return op
}

// shouldSendInformationalNotice 判断当前通道是否应发送「信息性告知」（新建已读待办）。
//
// 返回 false 表示该通道无法满足「推，但不打扰」语义，调用方应跳过并记日志。
// 目前只有流程中心返回 false：它只有 todo/done（办结）与 carboncopy/read（抄送语义），
// **没有「待办置已读」**；为一条告知而 create 节点会污染 A 方案维护的节点留痕序列，不划算。
//
// 抽成**纯函数**（不碰 store / 网络 / 时钟），以便在集成测试因安全阀全部 SKIP 的环境下，
// 仍能直接单测这条关键决策（见 notify_plan_informational_test.go）。
func shouldSendInformationalNotice(flowCenterEnabled bool) bool {
	return !flowCenterEnabled
}

// informationalNotice 生成一条「信息性告知」操作（产品定义 Q2：推，但置「已读」不打扰）。
//
// 返回 nil 表示该通道无法满足「不打扰」语义，故跳过（不产生任何 op）——判断见
// shouldSendInformationalNotice。copyFn 为该类告知的文案生成函数（acceptedCopy / repairingCopy /
// doneCopy），与收件人一起由调用方（planFor 的三处）传入。
func (s *NotifyService) informationalNotice(o *model.RepairOrder, empID int64, purpose string,
	copyFn func(*model.RepairOrder) (string, string)) []notifyOp {
	if !shouldSendInformationalNotice(s.flowCenterEnabled) {
		log.Printf("notify: repair=%s purpose=%s 信息性告知在流程中心通道下跳过（该通道无「待办置已读」能力）",
			o.Code, purpose)
		return nil
	}
	title, content := copyFn(o)
	return []notifyOp{s.ensureReadOp(o, empID, purpose, title, content)}
}

// dealOp 构造「把该人当前待办流转为已办」的操作。
func (s *NotifyService) dealOp(o *model.RepairOrder, empID int64, purpose string, withRead bool) notifyOp {
	return notifyOp{
		kind:     opDeal,
		empID:    empID,
		openID:   s.resolveOpenID(empID),
		purpose:  purpose,
		withRead: withRead,
	}
}

// readOp 构造「把该人当前待办只置已读」的操作（本地状态保持 active，见 opRead 注释）。
func (s *NotifyService) readOp(o *model.RepairOrder, empID int64, purpose string) notifyOp {
	return notifyOp{
		kind:    opRead,
		empID:   empID,
		openID:  s.resolveOpenID(empID),
		purpose: purpose,
	}
}

// deleteAllOp 构造「删除该 sourceId 下所有人」的操作，并记录本地需置 deleted 的收件人。
func (s *NotifyService) deleteAllOp(o *model.RepairOrder) notifyOp {
	seen := map[int64]bool{}
	var emps []int64
	add := func(id int64) {
		if id > 0 && !seen[id] {
			seen[id] = true
			emps = append(emps, id)
		}
	}
	for _, id := range s.assetManagerEmpIDs() {
		add(id)
	}
	add(o.ReporterEmpID)
	add(o.AssigneeEmpID)
	return notifyOp{
		kind:              opDeleteAll,
		purpose:           "cancel",
		markDeletedEmpIDs: emps,
	}
}

// finishFlowOp 构造「流程结案」操作：整单级、无收件人（与 deleteAllOp 同构）。
// 仅流程中心通道产生（planFor 的 confirm 分支在 s.flowCenterEnabled 时才追加）。
func (s *NotifyService) finishFlowOp(o *model.RepairOrder) notifyOp {
	return notifyOp{
		kind:    opFinishFlow,
		purpose: "confirm",
	}
}

// assetManagerEmpIDs 取受理人（§3.1）：本系统无「受理人」字段，受理 / 驳回 / 派工 / 取消的
// 权限位是 repair.dispatch，当前只有 asset_manager 与 admin 拥有。收件人取持有 asset_manager
// 角色（roles 列含它）且启用的账号绑定的 employee_id；admin 账号通常未绑定员工（employee_id=0），
// 天然被排除。
//
// 多角色（一人可多角色）：用 FIND_IN_SET(?, roles) 而不是 role = ?，否则「主角色不是 asset_manager
// 但角色集合里含 asset_manager」的账号会被漏掉。迁移脚本（deploy/migrations/20260924_multi_role.sql）
// 已保证 roles 列完整，故**不再 OR role = ?**。
//
// 查询直接在 api 层用 store 暴露的 *sql.DB 完成——store 包由另一位工程师占用，不改 store/。
func (s *NotifyService) assetManagerEmpIDs() []int64 {
	rows, err := s.st.DB().Query(
		`SELECT employee_id FROM sys_user
		  WHERE enabled = 1 AND employee_id > 0 AND FIND_IN_SET(?, roles) > 0`,
		model.RoleAssetManager)
	if err != nil {
		log.Printf("notify: 查询受理人失败 err=%v", err)
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			log.Printf("notify: 扫描受理人失败 err=%v", err)
			return out
		}
		out = append(out, id)
	}
	return out
}

// resolveOpenID 取员工的云之家 openId；查不到返回空串并记日志（§3.2：跳过该人、不报错）。
func (s *NotifyService) resolveOpenID(empID int64) string {
	if empID <= 0 {
		return ""
	}
	openID, err := s.st.EmployeeOpenID(empID)
	if err != nil {
		log.Printf("notify: 查询 employee.open_id 失败 emp=%d err=%v", empID, err)
		return ""
	}
	if openID == "" {
		log.Printf("notify: skip emp=%d 无云之家 openId（未绑定 / 未同步）", empID)
	}
	return openID
}
