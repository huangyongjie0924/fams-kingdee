package api

import (
	"testing"

	"asset-mgr/model"
)

// 本文件覆盖「办结 / 删除**已有**待办时按记录自己的 channel 分派」这一**最容易写错、
// 且写错后静默失效**的决策：抽取出的两个纯函数 shouldUseFlowCenter / flowBizIDOf。
//
// 背景（docs/云之家流程中心集成方案.md「必读」第 2 条）：
// 灰度切换时，切换前用 im-todo 发出的**存量待办**仍在对端，必须继续用 im-todo 的 action 办结；
// 切换后新发的是流程中心待办，必须用 done 办结。若这里用「全局开关 s.flowCenterEnabled」而非
// 「这条记录自己的 channel」判断，其中一半待办会**永远办不掉**，而且用户只看到「待办一直在那儿」——
// 没有报错、没有日志，是最难定位的一类故障。故把「选哪个 API」抽成纯函数单独钉死。
//
// 依赖 model.NotifyTask.Channel / BizID（已在存储层提交落地）。

// TestShouldUseFlowCenter 钉死「办结分派」只看记录自己的 channel，不看全局开关。
//
// 反向验证：把 shouldUseFlowCenter 改成恒真（return true）⇒ 「im-todo 记录」两例变红；
// 改成恒假（return false）⇒ 「flowcenter 记录」一例变红。
func TestShouldUseFlowCenter(t *testing.T) {
	cases := []struct {
		name string
		task *model.NotifyTask
		want bool
	}{
		{"nil 记录（调用方通常已先判 nil）按 im-todo", nil, false},
		{"空 channel（历史行）按 im-todo", &model.NotifyTask{Channel: ""}, false},
		{"im-todo 记录走 im-todo action", &model.NotifyTask{Channel: notifyChannelIMTodo}, false},
		{"flowcenter 记录走流程中心 done", &model.NotifyTask{Channel: notifyChannelFlowCenter}, true},
		{"未知 channel 保守按 im-todo", &model.NotifyTask{Channel: "some-unknown-channel"}, false},
	}
	for _, c := range cases {
		if got := shouldUseFlowCenter(c.task); got != c.want {
			t.Errorf("%s: shouldUseFlowCenter = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

// TestFlowBizIDOf 钉死「取 bizId」的守卫：空 bizId 必须返回 ok=false，
// 否则会向流程中心发一个 `[""]` 的 done 请求（把别人的待办或不存在的东西办掉）。
func TestFlowBizIDOf(t *testing.T) {
	if _, ok := flowBizIDOf(nil); ok {
		t.Error("nil 记录不应返回 ok=true")
	}
	if _, ok := flowBizIDOf(&model.NotifyTask{}); ok {
		t.Error("空 bizId 不应返回 ok=true（防脏数据：不能拿空串当 bizId 去调 done）")
	}
	got, ok := flowBizIDOf(&model.NotifyTask{Channel: notifyChannelFlowCenter, BizID: "repair-1409-88-dispatch"})
	if !ok {
		t.Fatal("非空 bizId 应返回 ok=true")
	}
	if got != "repair-1409-88-dispatch" {
		t.Errorf("flowBizIDOf = %q, 期望 repair-1409-88-dispatch", got)
	}
}

// TestShouldDeleteForReactivate 钉死「重新通知前是否删旧待办」的分通道语义：
//   - flowcenter：每个节点是独立待办，**只删 active**（未办结的旧节点必须清掉，否则同一人挂两条）；
//     **dealt 的绝不删**——那是已办结的节点，删掉就丢了流程留痕，正是 A 方案要避免的；
//   - im-todo：一人一单只有一条待办，**连 dealt 也要删**（否则新待办建不出来）。
//
// 反向验证：把 flowcenter 分支的判定改成与 im-todo 共用（`active || dealt`）⇒
// 「flowcenter+dealt→false」一例变红——这正是「接单时『待接单』节点被删除而非留痕」的回归。
func TestShouldDeleteForReactivate(t *testing.T) {
	s := &NotifyService{}
	cases := []struct {
		name string
		task *model.NotifyTask
		want bool
	}{
		{"nil 不删", nil, false},
		{"flowcenter+active 删（未办结的旧节点必须清掉）",
			&model.NotifyTask{Channel: notifyChannelFlowCenter, State: model.NotifyStateActive}, true},
		{"flowcenter+dealt 不删（已办结节点留痕，A 方案核心）",
			&model.NotifyTask{Channel: notifyChannelFlowCenter, State: model.NotifyStateDealt}, false},
		{"flowcenter+failed 不删（非 active）",
			&model.NotifyTask{Channel: notifyChannelFlowCenter, State: model.NotifyStateFailed}, false},
		{"im-todo+active 删",
			&model.NotifyTask{Channel: notifyChannelIMTodo, State: model.NotifyStateActive}, true},
		{"im-todo+dealt 删（一人一单，dealt 也要删掉再建）",
			&model.NotifyTask{Channel: notifyChannelIMTodo, State: model.NotifyStateDealt}, true},
		{"im-todo+failed 不删",
			&model.NotifyTask{Channel: notifyChannelIMTodo, State: model.NotifyStateFailed}, false},
	}
	for _, c := range cases {
		if got := s.shouldDeleteForReactivate(c.task); got != c.want {
			t.Errorf("%s: shouldDeleteForReactivate = %v, 期望 %v", c.name, got, c.want)
		}
	}
}
