package yunzhijia

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// pathGetAllPersons 通讯录增量拉取接口。
const pathGetAllPersons = "/gateway/opendata-control/data/getallpersons"

// firstSyncTime 是首次增量拉取的起始时间（文档给定，api_org_index.md:20）。
const firstSyncTime = "2008-08-02 01:40:38"

// maxPersonsPerPage 单次拉取上限（文档：每次限制 1000 条以内，api_org_index.md:22）。
const maxPersonsPerPage = 1000

// —— getallpersons 的限流对策（实测，见 docs/云之家待办通知集成方案.md §6.5）——
//
// 该接口有频率限制：连续调用时，第 2 次起返回
// {"success":false,"error":"opendata_req_frequen","errorCode":500011}，冷却约 2 秒。
// 实测固定 2.5s 间隔连打 5 页，5/5 全成功、零 500011。
const (
	// errCodeRateLimited 是 getallpersons 的频率限制错误码
	// （error=opendata_req_frequen，实测冷却约 2 秒）。
	errCodeRateLimited = 500011

	// personsMinInterval 是两次 getallpersons 调用之间的最小间隔（生产默认）。
	// 实测 2.5s 连打 5 页零限流（2s 冷却 + 余量）。
	personsMinInterval = 2500 * time.Millisecond

	// personsMaxAttempts 是单页拉取的最大尝试次数（有界，避免无限重试）。
	personsMaxAttempts = 4
)

// Person 是云之家通讯录人员（getallpersons 返回项）。
type Person struct {
	PhotoURL   string `json:"photoUrl"`
	Gender     string `json:"gender"`
	OpenID     string `json:"openId"`
	JobTitle   string `json:"jobTitle"`
	JobNo      string `json:"jobNo"`
	Name       string `json:"name"`
	GlobalID   string `json:"globalId"`
	IsAdmin    string `json:"isAdmin"`
	Department string `json:"department"`
	OrgID      string `json:"orgId"`
	Status     string `json:"status"`
}

// PersonsPage 是 getallpersons 的一页结果。
//
// 注意这是**增量事件接口**（K9）：同一 openId 可能多次出现，调用方应以「后出现的覆盖已有记录」；
// 返回条数不足 count 才表示分页结束。Time 为本次拉取的数据截止时刻，作为下次增量同步的游标。
type PersonsPage struct {
	Persons []Person `json:"persons"`
	Time    string   `json:"time"`
}

// FetchAllPersons 拉取一页通讯录增量数据（getallpersons）。
//
// K10：该接口的 Content-Type 是 application/x-www-form-urlencoded（api_org_index.md:13），
// 不能复用写死 JSON 的 doJSON，故走 doForm。
//
// 参数：eid 必传；timeCursor 为空时取首次起始时间；begin<0 视为 0；count<=0 取 1000，
// 并夹到上限 1000。
func (c *Client) FetchAllPersons(ctx context.Context, eid, timeCursor string, begin, count int) (*PersonsPage, error) {
	if eid == "" {
		return nil, fmt.Errorf("yunzhijia fetch persons: eid 不能为空")
	}
	if timeCursor == "" {
		timeCursor = firstSyncTime
	}
	if begin < 0 {
		begin = 0
	}
	if count <= 0 || count > maxPersonsPerPage {
		count = maxPersonsPerPage
	}

	form := url.Values{}
	form.Set("eid", eid)
	form.Set("time", timeCursor)
	form.Set("begin", strconv.Itoa(begin))
	form.Set("count", strconv.Itoa(count))

	// —— 限流对策：节流 + 有界重试 ——
	//
	// getallpersons 有频率限制：连续调用时第 2 次起返回
	// {"success":false,"error":"opendata_req_frequen","errorCode":500011}（冷却约 2 秒）。
	// 本同步是紧密分页循环，不节流则第 2 页起必被限流 ⇒ 只能回填第 1 页。
	// 这里在**每次尝试前**都走一次节流等待（首次无等待），且仅对 500011 重试。
	var lastRateLimit error
	for attempt := 1; attempt <= personsMaxAttempts; attempt++ {
		if err := c.throttlePersons(ctx); err != nil {
			return nil, err
		}
		path, token, err := c.tokenPath(ctx, pathGetAllPersons)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Data struct {
				Persons []Person `json:"persons"`
				Time    string   `json:"time"`
			} `json:"data"`
			ErrorCode int      `json:"errorCode"`
			Success   flexBool `json:"success"`
			Error     string   `json:"error"`
		}
		if err := c.doForm(ctx, path, token, form, &resp); err != nil {
			// 传输层 / 解析错误：不属于限流，重试无意义，直接返回。
			return nil, fmt.Errorf("yunzhijia fetch persons: %w", err)
		}
		if resp.Success.Bool() {
			return &PersonsPage{Persons: resp.Data.Persons, Time: resp.Data.Time}, nil
		}
		if resp.ErrorCode == errCodeRateLimited {
			// 限流：可重试。错误串保留 500011 与「限流」字样，便于一眼区分于参数错误。
			lastRateLimit = fmt.Errorf("yunzhijia fetch persons 限流: errorCode=%d (%s)，已尝试 %d/%d 次",
				errCodeRateLimited, resp.Error, attempt, personsMaxAttempts)
			continue
		}
		// 其它错误码（如 500000 / 500007）：不是限流，立刻返回，不做无谓重试。
		return nil, fmt.Errorf("yunzhijia fetch persons denied: errorCode=%d error=%s", resp.ErrorCode, resp.Error)
	}
	return nil, lastRateLimit
}

// throttlePersons 在每次调用 getallpersons 之前等待，保证距上次调用至少间隔
// personsMinInterval（可由 SetPersonsMinInterval 覆盖，生产默认 2.5s）。
//
// 等待必须尊重 ctx：ctx 取消时立即返回 ctx.Err()，绝不用 time.Sleep 死等。
// 该状态**只作用于 getallpersons**，不影响共享 Client 的其它接口
// （CreateTodo / ActionTodo / GetAppToken 均不经过此函数）。
func (c *Client) throttlePersons(ctx context.Context) error {
	c.personsMu.Lock()
	interval := c.personsInterval
	if interval <= 0 {
		interval = personsMinInterval
	}
	now := time.Now()
	if c.personsLastCall.IsZero() || !now.Before(c.personsLastCall.Add(interval)) {
		// 首次调用，或距上次调用已满间隔：无需等待。
		c.personsLastCall = now
		c.personsMu.Unlock()
		return nil
	}
	// 预占下一个时隙，避免并发调用同时通过而绕过节流。
	next := c.personsLastCall.Add(interval)
	c.personsLastCall = next
	c.personsMu.Unlock()

	timer := time.NewTimer(next.Sub(now))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetPersonsMinInterval 覆盖 getallpersons 的节流间隔，仅用于测试 / 特殊场景快速跑。
// d <= 0 时恢复默认（生产默认 personsMinInterval = 2.5s）。
func (c *Client) SetPersonsMinInterval(d time.Duration) {
	c.personsMu.Lock()
	c.personsInterval = d
	c.personsMu.Unlock()
}
