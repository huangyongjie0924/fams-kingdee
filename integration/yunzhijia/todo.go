package yunzhijia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// 待办相关接口路径（均使用 app 级 accessToken）。
const (
	pathGenerateTodo    = "/gateway/newtodo/open/generatetodo.json"
	pathActionTodo      = "/gateway/newtodo/open/action.json"
	pathCheckCreateTodo = "/gateway/newtodo/open/checkcreatetodo.json"
)

// TodoParam 是按人定制的待办参数（可选）。注意：服务端对空的 url/content
// 会**忽略**而非覆盖为空（K7），故若想按人定制文案，只填非空值。
type TodoParam struct {
	OpenID  string `json:"openId"`
	URL     string `json:"url,omitempty"`
	Content string `json:"content,omitempty"`
}

// TodoRequest 对应 generatetodo.json 的请求体。
//
// 关于 sync：文档默认 false（异步），但**建议同步**。本客户端按 P0 设计统一固定
// `sync=true`（方案 §3.2：P0 全部同步创建，简单可靠；异步 + checkcreatetodo 轮询补偿为 P1），
// 因此本结构体不暴露 sync 字段，由 CreateTodo 在发送前强制置 true，避免零值陷阱。
type TodoRequest struct {
	AppID     string      `json:"appId"`
	SourceID  string      `json:"sourceId"`
	URL       string      `json:"url"`
	Content   string      `json:"content"`
	Title     string      `json:"title"`
	ItemTitle string      `json:"itemtitle,omitempty"`
	HeadImg   string      `json:"headImg"`
	SenderID  string      `json:"senderId,omitempty"`
	// TodoType：0=通知中心 / 1=待审批。nil 表示不传（服务端默认通知中心）。
	TodoType *int        `json:"todoType,omitempty"`
	TagID    string      `json:"tagId,omitempty"`
	OpenIDs  []string    `json:"openId,omitempty"`
	Params   []TodoParam `json:"params,omitempty"`

	// sync 固定为 true（见类型注释），由 CreateTodo 赋值。
	sync bool
}

// MarshalJSON 把固定为 true 的 sync 真正注入请求体。
//
// ⚠️ 这是一个 P0 修复，务必理解其必要性：sync 设计为**强制、调用方不可设**（见类型注释），
// 故声明为**未导出**字段；但 encoding/json **只序列化导出字段**，会静默跳过未导出的 sync。
// 后果：CreateTodo 里的 `req.sync = true` 从未出现在请求体中，服务端按默认 sync=false（异步）
// 处理 —— 紧随 create 之后的 `action{read:1}` 必然失败（生产实测 errorCode=1），
// informational「新建即已读」因此完全失效，收件人看到的是未读待办。
//
// 修法：用 alias 保留原有字段与 tag（未导出的 sync 仍会被跳过），再在外层补一个真正的
// `"sync":true`。**不改成导出 Sync 字段**——那会把「强制」变成「调用方可改」，与设计声明相悖。
func (r TodoRequest) MarshalJSON() ([]byte, error) {
	type alias TodoRequest
	return json.Marshal(struct {
		alias
		Sync bool `json:"sync"`
	}{alias(r), true})
}

// TodoActionType 是待办流转的目标状态。
//
// ⚠️ K1（本集成最关键的坑）：action.json 的**参数表**把 deal/read/delete 列为**顶层字段**，
// 但官方给出的 3 个 JSON 示例（变已读 / 变已处理 / 删除）**全部使用嵌套 actiontype 对象**，
// 且错误示例明确返回 `param error:actiontype is null.`。
//   - 参数表：server-api_im_im-todo.md:176-184
//   - 示例：server-api_im_im-todo.md:190-233
//   - 错误示例：server-api_im_im-todo.md:272-278
//
// → **必须使用嵌套结构**（如 {"actiontype":{"read":1,"deal":1}}）；顶层平铺的
// read/deal/delete **无效**，会被服务端判为 actiontype is null。切勿"照参数表写"。
type TodoActionType struct {
	Read   *int `json:"read,omitempty"`
	Deal   *int `json:"deal,omitempty"`
	Delete *int `json:"delete,omitempty"`
}

// TodoActionRequest 对应 action.json 的请求体。
// OpenIDs 为空（nil/空切片，omitempty 省略）时，服务端会变更该 sourceitemid 下**所有人**的待办状态
// （server-api_im_im-todo.md:183）——撤单场景正需要此行为。
type TodoActionRequest struct {
	SourceType   string         `json:"sourcetype"`
	SourceItemID string         `json:"sourceitemid"`
	ActionType   TodoActionType `json:"actiontype"`
	OpenIDs      []string       `json:"openids,omitempty"`

	// sync 固定为 true，由 ActionTodo 赋值（见 TodoRequest 说明）。
	sync bool
}

// MarshalJSON 与 TodoRequest.MarshalJSON 同因同法：把未导出的 sync 以 `"sync":true`
// 真正注入请求体（否则 action{read:1} 在异步模式下必然失败，见 TodoRequest.MarshalJSON 注释）。
// 除新增 sync 外，sourcetype / sourceitemid / actiontype（K1 嵌套形态）/ openids 逐字不变。
func (r TodoActionRequest) MarshalJSON() ([]byte, error) {
	type alias TodoActionRequest
	return json.Marshal(struct {
		alias
		Sync bool `json:"sync"`
	}{alias(r), true})
}

// TodoResult 是待办接口（创建 / 流转）的通用返回。
//
// data 字段类型不固定，故用 json.RawMessage 保留原文，再由 parseData 归一化：
//   - 对象 {}（正常）→ 忽略；
//   - 字符串（参数异常，如 "param error:actiontype is null."）→ DataMessage；
//   - 字符串数组（501/502/505 部分失败）→ FailedOpenIDs（K3：这是**部分成功**，
//     其余人的待办已处理，不能整体判失败重试）。
type TodoResult struct {
	ErrorCode int             `json:"errorCode"`
	Error     string          `json:"error"`
	Success   flexBool        `json:"success"`
	Data      json.RawMessage `json:"data"`

	// FailedOpenIDs / DataMessage 由 parseData 从 Data 解析填充。
	FailedOpenIDs []string `json:"-"`
	DataMessage   string   `json:"-"`
}

// OK 判断是否整体成功。
func (r *TodoResult) OK() bool { return r != nil && r.Success.Bool() }

// parseData 归一化 data 字段（见类型注释）。
func (r *TodoResult) parseData() {
	raw := bytes.TrimSpace(r.Data)
	if len(raw) == 0 {
		return
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		r.DataMessage = s
		return
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		r.FailedOpenIDs = list
		return
	}
	// 其余形态（如 {} 或 checkcreatetodo 的明细对象）保留 RawData 供调用方解析。
}

// CreateTodo 创建待办（generatetodo.json）。
// 固定 sync=true（见 TodoRequest 注释）；AppID 为空时回退客户端 appID。
func (c *Client) CreateTodo(ctx context.Context, req *TodoRequest) (*TodoResult, error) {
	if req == nil {
		return nil, errors.New("yunzhijia create todo: 请求为空")
	}
	if req.AppID == "" {
		req.AppID = c.appID
	}
	// 云之家 generatetodo 要求 params 非空（否则返回 errorCode=1, data="param error:'params' is null!"）。
	// 若调用方未显式传 Params，则按 OpenIDs 自动构建（每条只含 openId）。
	if len(req.Params) == 0 && len(req.OpenIDs) > 0 {
		req.Params = make([]TodoParam, len(req.OpenIDs))
		for i, oid := range req.OpenIDs {
			req.Params[i] = TodoParam{OpenID: oid}
		}
	}
	req.sync = true
	path, token, err := c.tokenPath(ctx, pathGenerateTodo)
	if err != nil {
		return nil, err
	}
	var resp TodoResult
	if err := c.doJSON(ctx, path, token, req, &resp); err != nil {
		return nil, fmt.Errorf("yunzhijia create todo: %w", err)
	}
	resp.parseData()
	return &resp, nil
}

// ActionTodo 流转待办状态（action.json）：已读 / 已办 / 删除。
// SourceType 为空时回退客户端 appID；固定 sync=true。
// ⚠️ 流转状态**必须**通过嵌套 ActionType 传递（K1，见 TodoActionType 注释）。
func (c *Client) ActionTodo(ctx context.Context, req *TodoActionRequest) (*TodoResult, error) {
	if req == nil {
		return nil, errors.New("yunzhijia action todo: 请求为空")
	}
	if req.SourceType == "" {
		req.SourceType = c.appID
	}
	req.sync = true
	path, token, err := c.tokenPath(ctx, pathActionTodo)
	if err != nil {
		return nil, err
	}
	var resp TodoResult
	if err := c.doJSON(ctx, path, token, req, &resp); err != nil {
		return nil, fmt.Errorf("yunzhijia action todo: %w", err)
	}
	resp.parseData()
	return &resp, nil
}

// CheckTodoRequest 对应 checkcreatetodo.json 的请求体。
type CheckTodoRequest struct {
	SourceType   string `json:"sourcetype"`
	SourceItemID string `json:"sourceitemid"`
	OpenID       string `json:"openId"`
}

// CheckTodoResult 是待办状态查询结果（checkcreatetodo.json）。
//
// 语义（server-api_im_im-todo.md:388-394）：
//   - DealCount=0, UnDelCount=0：单据不存在；
//   - DealCount=0, UnDelCount=1：单据存在，待处理；
//   - DealCount=1, UnDelCount=0：待办已生成且已办。
type CheckTodoResult struct {
	SourceID   string   `json:"sourceid"`
	AppID      string   `json:"appid"`
	Check      flexBool `json:"check"`
	DealCount  int      `json:"dealCount"`
	UnDelCount int      `json:"undelCount"`
}

// CheckCreateTodo 查询待办状态（checkcreatetodo.json）。它是**零副作用**的连通性自检：
// 不创建任何待办即可验证「凭证有效 + 接口可达 + appId 正确」（方案 §10.5）。
// SourceType 为空时回退客户端 appID。
func (c *Client) CheckCreateTodo(ctx context.Context, req *CheckTodoRequest) (*CheckTodoResult, error) {
	if req == nil {
		return nil, errors.New("yunzhijia check todo: 请求为空")
	}
	if req.SourceType == "" {
		req.SourceType = c.appID
	}
	path, token, err := c.tokenPath(ctx, pathCheckCreateTodo)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data      CheckTodoResult `json:"data"`
		ErrorCode int             `json:"errorCode"`
		Success   flexBool        `json:"success"`
		Error     string          `json:"error"`
	}
	if err := c.doJSON(ctx, path, token, req, &resp); err != nil {
		return nil, fmt.Errorf("yunzhijia check todo: %w", err)
	}
	if !resp.Success.Bool() {
		return nil, fmt.Errorf("yunzhijia check todo denied: errorCode=%d error=%s", resp.ErrorCode, resp.Error)
	}
	return &resp.Data, nil
}
