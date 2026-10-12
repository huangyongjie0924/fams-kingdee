package model

import "time"

// 维修待办通知任务状态（repair_notify_task.state）。
//
// 状态机（见 docs/云之家待办通知集成方案.md §4.3）：
//
//	（无记录）                        --create-->   pending / active
//	active + 本次是「同一待办推进」     --流转-->     dealt
//	active/dealt + 本次要「重新激活」   --先删后建--> active
//	active/dealt + 撤单                --流转-->     deleted
//	任何操作失败                       -->           failed（记 last_error，等待重试）
//
// state 一律走这里的常量，不在 store / api 里散落字符串字面量——
// 散落字符串一旦拼错，查询用的与写入用的就不是同一个状态，而这种故障是静默的。
const (
	NotifyStatePending = "pending" // 已落库待投递（队列满或首次入队）
	NotifyStateActive  = "active"  // 待办已创建、对方未办
	NotifyStateDealt   = "dealt"   // 对方已办（已读并处理）
	NotifyStateDeleted = "deleted" // 已删除（撤单 / 终态）
	NotifyStateFailed  = "failed"  // 投递失败，等待重试
)

// NotifyStates 是全部状态码，供测试与校验遍历（顺序即业务推进顺序）。
var NotifyStates = []string{
	NotifyStatePending, NotifyStateActive, NotifyStateDealt, NotifyStateDeleted, NotifyStateFailed,
}

// IsValidNotifyState 校验状态码是否合法。
func IsValidNotifyState(s string) bool {
	for _, v := range NotifyStates {
		if v == s {
			return true
		}
	}
	return false
}

// 待办通道标识（repair_notify_task.channel）。**取值即落库字符串**，改字面量会破坏存量行。
//
// 定义在 model 而非 api：store 落库 / 读回都要用同一个值，散落两处字面量一旦拼错，
// 「写进去的通道」与「读出来判断的通道」就不是同一个，而这种故障是静默的。
// 这是灰度切换的地基——见 docs/云之家流程中心集成方案.md「必读」第 2 条。
const (
	NotifyChannelIMTodo     = "im-todo"    // 云之家待办通知（generatetodo/action）
	NotifyChannelFlowCenter = "flowcenter" // 统一流程中心（flowcenter/*）
)

// NotifyTask 是 repair_notify_task 的领域模型：**每人每单每 sourceId** 的待办投递状态。
//
// 它是待办通知的「本地唯一真相」，用于幂等——**绝不**依赖云之家 create 报错去重（见 §4.3）：
// 同一 (repair_id, emp_id, source_id) 只保留一行（UNIQUE KEY uk_notify），
// 重试器据 state / attempts 决定该 create 还是该流转。
//
// open_id 冗余落库（与 employee.open_id 同值）：排障时不必再去 join employee 就能看出
// 「当时是发给谁的」，且员工后来解绑云之家也不会让历史任务变得不可解释。
type NotifyTask struct {
	ID       int64  `json:"id"`
	RepairID int64  `json:"repair_id"`
	EmpID    int64  `json:"emp_id"`
	OpenID   string `json:"open_id"`
	SourceID string `json:"source_id"`
	// Purpose 标记本轮通知的用途（intake/dispatch/confirm/return…），
	// 供「重新激活」判定与排障使用。
	Purpose string `json:"purpose"`
	// Channel 记录**这条待办是用哪个通道发出去的**（model.NotifyChannel*）。
	//
	// 这是灰度切换的地基：切换前用 im-todo 发出的存量待办，办结时必须继续走 im-todo 的
	// action 接口；切换后新发的流程中心待办必须走 done 接口。任何「只看全局开关」的办结
	// 逻辑都会让其中一半永远办不掉，而且是静默的（见 docs/云之家流程中心集成方案.md「必读」第 2 条）。
	Channel string `json:"channel"`
	// BizID 是流程中心侧的 bizId（`{sourceId}-{empID}-{purpose}`，**节点级**）；
	// im-todo 通道下为空串。办结 / 删除流程中心待办时据它定位节点级待办
	// （A 方案下同一维修工先后持有「待接单」「维修中」两个节点，必须能分别办结）。
	BizID     string    `json:"biz_id"`
	State     string    `json:"state"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
