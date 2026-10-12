package yunzhijia

import (
	"context"
	"errors"
	"fmt"
)

// 统一流程中心（cloudflow）接口路径（均使用 **team 级** accessToken）。
//
// ⚠️ 与待办通知（im-todo，见 todo.go）不同：流程中心原生集成要求 scope=team 的
// accessToken（server-api_business_flowcenter2.md:85「原生集成accesstoken的scope为team」）。
// app 级 token 调本组接口返回 10000401 认证失败。
const (
	pathFlowInit           = "/gateway/flowcenter/native/init"
	pathFlowTodoSend       = "/gateway/flowcenter/native/todo/send"
	pathFlowTodoDone       = "/gateway/flowcenter/native/todo/done"
	pathFlowTodoDelete     = "/gateway/flowcenter/native/todo/delete"
	pathFlowInstanceFinish = "/gateway/flowcenter/native/instance/finish"
	pathFlowInstanceDelete = "/gateway/flowcenter/native/instance/delete"
	pathFlowInstanceReset  = "/gateway/flowcenter/native/instance/reset"
)

// FlowResult 是流程中心接口的通用返回信封。
//
// 与 TodoResult 同构（{success,errorCode,error,data}），故直接别名复用：
//   - 流程中心的 data 通常为 null，parseData 不命中任何分支（无害）；
//   - **不套用** FailedOpenIDs——流程中心无「部分失败 openId 列表」概念。
type FlowResult = TodoResult

// InitFlowField 是初始化接口注册的**字段定义**（init 的 field[]）。
//
// ⚠️ 与 SendFlowField 是**两种不同结构，绝不可共用**：
//   - init 注册用 title（+可选 type）——server-api_business_flowcenter2.md:108-113；
//   - send 传值用 name/value——server-api_business_flowcenter2.md:205-210。
//
// 注册的 title 必须与发送时 field[].name **完全一致**。
type InitFlowField struct {
	Title string `json:"title"`
	// Type：textWidget（默认）/ numberWidget / moneyWidget；空则服务端按 textWidget。
	Type string `json:"type,omitempty"`
}

// InitFlowRequest 对应 /gateway/flowcenter/native/init 的请求体。
// AppID 为空时回退客户端 appID（见 InitFlowTemplate）。
type InitFlowRequest struct {
	// BUID：模板所属业务单元（云之家部门 id / orgId）。不填默认属根组织。
	BUID     string `json:"buid,omitempty"`
	TypeID   string `json:"typeId"`
	TypeName string `json:"typeName"`
	AppID    string `json:"appId"`
	AppName  string `json:"appName"`
	// MobileURL / WebURL：发起页面地址；不填则该端不可见。
	MobileURL string `json:"mobileUrl,omitempty"`
	WebURL    string `json:"webUrl,omitempty"`
	// Field：自定义表单字段定义（取前 3 个作为重要字段，最多 50 个）。
	Field   []InitFlowField `json:"field,omitempty"`
	Creator string          `json:"creator"`
	// Description：模板描述，最多 200 字。
	Description string `json:"description,omitempty"`
}

// SendFlowField 是发送待办时的**字段值**（send 的 field[]）。
// ⚠️ 与 InitFlowField 不同：这里是 name/value 传值结构，不是 title 注册结构。
type SendFlowField struct {
	// Name：字段名，即 init 接口 field[].title。
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SendFlowTodoItem 是发送待办的审批信息（todo[]）。
type SendFlowTodoItem struct {
	Approver string `json:"approver"`
	// BizID：业务系统待办 id，在特定 appId 下须唯一。
	BizID string `json:"bizId"`
	// MessageMode：MODE_TITLE（默认）/ MODE_MESSAGE / MODE_TITLE_MESSAGE。
	MessageMode string `json:"messageMode,omitempty"`
	// MessageContent：messageMode 为 MODE_MESSAGE / MODE_TITLE_MESSAGE 时生效。
	MessageContent string `json:"messageContent,omitempty"`
}

// SendFlowTodoRequest 对应 /gateway/flowcenter/native/todo/send 的请求体。
// AppID 为空时回退客户端 appID（见 SendFlowTodo）。
type SendFlowTodoRequest struct {
	WebLink        string `json:"webLink"`
	MobileLink     string `json:"mobileLink,omitempty"`
	WebFlowLink    string `json:"webFlowLink"`
	MobileFlowLink string `json:"mobileFlowLink,omitempty"`
	AppID          string `json:"appId"`
	// Todo：审批信息（至少一条）。
	Todo   []SendFlowTodoItem `json:"todo"`
	TypeID string             `json:"typeId"`
	// Urgency：紧急程度，取值 1–5。
	Urgency int `json:"urgency"`
	// ProcessURL：快捷同意 url（P0 暂不使用）。
	ProcessURL string `json:"processUrl,omitempty"`
	FlowID     string `json:"flowId"`
	Title      string `json:"title"`
	SerialNo   string `json:"serialNo,omitempty"`
	// Field：字段值；**仅首次发送待办时生效**，后续发送会被忽略。
	Field   []SendFlowField `json:"field,omitempty"`
	Creator string          `json:"creator"`
	// CreatorOrgID：流程发起人所属部门 id；不填默认发起人主职部门。
	CreatorOrgID string `json:"creatorOrgId,omitempty"`
	// TTLType：standard（标准工时，排除非工作时间）/ natural（自然时间）。空=不启用超时。
	TTLType string `json:"ttlType,omitempty"`
	// TTL：节点处理超时分钟数。用 *int 区分「不传」与「显式 0/-1」：
	//   -1 = 系统默认时间段；>0 = 指定分钟；0 = 不启用超时；nil = 不传。
	TTL          *int   `json:"ttl,omitempty"`
	ActivityID   string `json:"activityId"`
	ActivityName string `json:"activityName"`
}

// FlowBizRequest 是按 bizId 批量操作的请求体（todo/done、todo/delete）。
type FlowBizRequest struct {
	AppID string `json:"appId"`
	// BizID：业务系统待办 id **数组**。
	BizID []string `json:"bizId"`
}

// FlowInstanceRequest 是按 flowId 批量操作的请求体（instance/finish、delete、reset）。
type FlowInstanceRequest struct {
	AppID string `json:"appId"`
	// FlowID：业务系统流程实例 id **数组**。
	FlowID []string `json:"flowId"`
}

// resolveAppID 在请求未显式指定 appId 时回退客户端 appID
// （与 CreateTodo 的既有做法一致，见 todo.go:126-128）。
func (c *Client) resolveAppID(appID string) string {
	if appID == "" {
		return c.appID
	}
	return appID
}

// postFlow 以 **team 级 token** + JSON 发送流程中心请求并解析信封。
// 流程中心全部接口同构（POST + JSON + ?accessToken=<team token>），故统一收敛到此处。
// op 仅用于错误信息可读性（如 "flowcenter send todo"）。
func (c *Client) postFlow(ctx context.Context, path, op string, body any) (*FlowResult, error) {
	pathWithToken, token, err := c.tokenPathScope(ctx, path, scopeTeam)
	if err != nil {
		return nil, err
	}
	var resp FlowResult
	if err := c.doJSON(ctx, pathWithToken, token, body, &resp); err != nil {
		return nil, fmt.Errorf("yunzhijia %s: %w", op, err)
	}
	resp.parseData()
	return &resp, nil
}

// InitFlowTemplate 初始化（或更新）流程中心模板（幂等）。
//
// ⚠️ 官方「必读」第 1 条：集成上线后**请勿再修改集成参数，尤其是 AppId**，否则可能导致
// 流程中心待办无法正常处理。
// ⚠️ field 为**累加**语义：对同一 typeId 多次初始化会追加字段而非覆盖，注册清单须一次定好。
//
// AppID 为空时回退客户端 appID。
func (c *Client) InitFlowTemplate(ctx context.Context, req *InitFlowRequest) (*FlowResult, error) {
	if req == nil {
		return nil, errors.New("yunzhijia flowcenter init: 请求为空")
	}
	req.AppID = c.resolveAppID(req.AppID)
	return c.postFlow(ctx, pathFlowInit, "flowcenter init", req)
}

// SendFlowTodo 发送待办（核心接口）：产生审批任务。
//
// AppID 为空时回退客户端 appID。
func (c *Client) SendFlowTodo(ctx context.Context, req *SendFlowTodoRequest) (*FlowResult, error) {
	if req == nil {
		return nil, errors.New("yunzhijia flowcenter send todo: 请求为空")
	}
	req.AppID = c.resolveAppID(req.AppID)
	return c.postFlow(ctx, pathFlowTodoSend, "flowcenter send todo", req)
}

// DoneFlowTodo 标记待办为已办（已处理场景）。AppID 为空时回退客户端 appID。
func (c *Client) DoneFlowTodo(ctx context.Context, req *FlowBizRequest) (*FlowResult, error) {
	if req == nil {
		return nil, errors.New("yunzhijia flowcenter done todo: 请求为空")
	}
	req.AppID = c.resolveAppID(req.AppID)
	return c.postFlow(ctx, pathFlowTodoDone, "flowcenter done todo", req)
}

// DeleteFlowTodo 删除待办（撤回场景）。AppID 为空时回退客户端 appID。
func (c *Client) DeleteFlowTodo(ctx context.Context, req *FlowBizRequest) (*FlowResult, error) {
	if req == nil {
		return nil, errors.New("yunzhijia flowcenter delete todo: 请求为空")
	}
	req.AppID = c.resolveAppID(req.AppID)
	return c.postFlow(ctx, pathFlowTodoDelete, "flowcenter delete todo", req)
}

// FinishFlowInstance 标记流程为已完成。AppID 为空时回退客户端 appID。
func (c *Client) FinishFlowInstance(ctx context.Context, req *FlowInstanceRequest) (*FlowResult, error) {
	if req == nil {
		return nil, errors.New("yunzhijia flowcenter finish instance: 请求为空")
	}
	req.AppID = c.resolveAppID(req.AppID)
	return c.postFlow(ctx, pathFlowInstanceFinish, "flowcenter finish instance", req)
}

// DeleteFlowInstance 删除流程（同时删除该流程对应的待办与抄送）。AppID 为空时回退客户端 appID。
func (c *Client) DeleteFlowInstance(ctx context.Context, req *FlowInstanceRequest) (*FlowResult, error) {
	if req == nil {
		return nil, errors.New("yunzhijia flowcenter delete instance: 请求为空")
	}
	req.AppID = c.resolveAppID(req.AppID)
	return c.postFlow(ctx, pathFlowInstanceDelete, "flowcenter delete instance", req)
}

// ResetFlowInstance 标记流程为待发起（回到发起节点）。AppID 为空时回退客户端 appID。
func (c *Client) ResetFlowInstance(ctx context.Context, req *FlowInstanceRequest) (*FlowResult, error) {
	if req == nil {
		return nil, errors.New("yunzhijia flowcenter reset instance: 请求为空")
	}
	req.AppID = c.resolveAppID(req.AppID)
	return c.postFlow(ctx, pathFlowInstanceReset, "flowcenter reset instance", req)
}
