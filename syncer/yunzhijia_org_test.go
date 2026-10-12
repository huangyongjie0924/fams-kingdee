package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"asset-mgr/config"
	"asset-mgr/integration/yunzhijia"
	"asset-mgr/model"
	"asset-mgr/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// 通讯录增量同步（syncer/yunzhijia_org.go）测试
//
// 用 httptest 造假云之家；真库连不上就 t.Skipf（沿用 store/api 集成测试的约定）。
//
// ⚠️ 测试**绝不污染生产 sync_state**：sync_state 唯一键是 (source, resource)，
// 若用生产名 ("yunzhijia", "yzj_org") 跑测试会覆盖生产游标。因此每个用例都用
// yzjProbeNames() 生成探针名，并在结束时清理该 source 的 run / state / error。
// ─────────────────────────────────────────────────────────────────────────────

// yzjTestDSN 取集成测试用的 DSN：优先环境变量，其次项目根的 config.yaml；
// 两者都拿不到就 Skip。
func yzjTestDSN(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("ASSET_MGR_TEST_DSN"); dsn != "" {
		return dsn
	}
	cfg, err := config.Load(filepath.Join("..", "config.yaml"))
	if err != nil {
		t.Skipf("未设置 ASSET_MGR_TEST_DSN，且读不到 ../config.yaml，跳过需要数据库的测试: %v", err)
	}
	return cfg.DSN()
}

func yzjTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(yzjTestDSN(t))
	if err != nil {
		t.Skipf("连接数据库失败，跳过: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// yzjProbeNames 返回一对唯一的探针 source/resource，避免与生产行相撞。
//
// 名字必须短：sync_run.source / sync_state.source 是 VARCHAR(32)。
var yzjProbeSeq int64

func yzjProbeNames() (string, string) {
	n := atomic.AddInt64(&yzjProbeSeq, 1)
	suffix := fmt.Sprintf("%d_%d", time.Now().UnixNano()%1_000_000, n)
	return "yzjT_" + suffix, "yzjOrgT_" + suffix
}

// yzjCleanup 清理探针 source 在同步表里的全部痕迹。
func yzjCleanup(t *testing.T, st *store.Store, source string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = st.DB().Exec(`DELETE e FROM sync_run_error e JOIN sync_run r ON r.id = e.run_id WHERE r.source = ?`, source)
		_, _ = st.DB().Exec(`DELETE FROM sync_run WHERE source = ?`, source)
		_, _ = st.DB().Exec(`DELETE FROM sync_state WHERE source = ?`, source)
	})
}

// yzjNewClient 起一个假云之家 httptest.Server 并返回指向它的客户端。
//
// 生产默认对 getallpersons 节流 2.5s；测试用小间隔，避免多页用例
// （如 maxPages=3、ctx 5s）被节流拖到超时。节流机制本身由
// integration/yunzhijia/person_test.go 覆盖。
func yzjNewClient(t *testing.T, handler http.HandlerFunc) *yunzhijia.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := yunzhijia.NewClient(srv.URL, "app-test", "secret-test", 5*time.Second)
	c.SetPersonsMinInterval(5 * time.Millisecond)
	return c
}

// yzjService 构造一个只配置了云之家、指向假客户端的 Service。
func yzjService(st *store.Store, client *yunzhijia.Client, eid string, enabled bool) *Service {
	cfg := &config.Config{}
	cfg.Yunzhijia.BaseURL = "http://example.invalid"
	cfg.Yunzhijia.AppID = "app-test"
	cfg.Yunzhijia.Secret = "secret-test"
	cfg.Yunzhijia.EID = eid
	cfg.Yunzhijia.DirectorySyncEnabled = enabled
	svc := NewService(st, nil, cfg)
	svc.SetYunzhijiaClient(client)
	return svc
}

// yzjTokenHandler 只处理认证接口。
func yzjIsToken(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, "/getAccessToken")
}

func yzjWriteToken(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"data":{"accessToken":"tok","refreshToken":"r","expireIn":7200},"errorCode":0,"success":true,"error":""}`))
}

func yzjPerson(jobNo, openID string) map[string]string {
	return map[string]string{"jobNo": jobNo, "openId": openID, "name": "探针"}
}

func yzjWritePersons(w http.ResponseWriter, persons []map[string]string, tm string) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(map[string]any{
		"success":   true,
		"errorCode": 0,
		"error":     "",
		"data":      map[string]any{"persons": persons, "time": tm},
	})
	_, _ = w.Write(b)
}

func yzjBegin(r *http.Request) int {
	_ = r.ParseForm()
	n, _ := strconv.Atoi(r.PostForm.Get("begin"))
	return n
}

func yzjCountErrors(t *testing.T, st *store.Store, source, jobNo string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sync_run_error e
		JOIN sync_run r ON r.id = e.run_id
		WHERE r.source = ? AND e.external_id = ? AND e.stage = ?`,
		source, jobNo, yzjStageOpenID).Scan(&n); err != nil {
		t.Fatalf("查询 sync_run_error 失败: %v", err)
	}
	return n
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. 分页终止：第一页满 → 拉第二页；第二页不足 → 停
// ─────────────────────────────────────────────────────────────────────────────
func TestYzjOrgPaginationTerminates(t *testing.T) {
	st := yzjTestStore(t)
	source, resource := yzjProbeNames()
	yzjCleanup(t, st, source)

	var begins []int
	client := yzjNewClient(t, func(w http.ResponseWriter, r *http.Request) {
		if yzjIsToken(r) {
			yzjWriteToken(w)
			return
		}
		begin := yzjBegin(r)
		begins = append(begins, begin)
		if begin == 0 {
			// 满页（== pageSize），触发翻页。
			yzjWritePersons(w, []map[string]string{yzjPerson("", ""), yzjPerson("", "")}, "2026-01-01 00:00:01")
			return
		}
		// 不足一页 → 分页结束。
		yzjWritePersons(w, []map[string]string{yzjPerson("", "")}, "2026-01-01 00:00:02")
	})
	svc := yzjService(st, client, "eid-test", true)

	res, err := svc.syncYunzhijiaOrg(context.Background(), yzjOrgOptions{
		source: source, resource: resource, triggeredBy: "test", pageSize: 2,
	})
	if err != nil {
		t.Fatalf("同步出错: %v", err)
	}
	if res.Pages != 2 {
		t.Fatalf("应拉取 2 页，实际 %d", res.Pages)
	}
	if want := []int{0, 2}; !intSliceEq(begins, want) {
		t.Fatalf("begin 递增序列应为 %v，实际 %v", want, begins)
	}
	if res.Fetched != 3 {
		t.Fatalf("应命中 3 人，实际 %d", res.Fetched)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. 防跑飞：服务端一直返回满页 → 必须在最大迭代上限处停下（不得死循环）
//
// 设 ctx 超时兜底：万一护栏被移除，FetchAllPersons 会因 ctx 到期返回错误而终止，
// 测试变红而不是把 CI 挂死。
// ─────────────────────────────────────────────────────────────────────────────
func TestYzjOrgStopsAtMaxPages(t *testing.T) {
	st := yzjTestStore(t)
	source, resource := yzjProbeNames()
	yzjCleanup(t, st, source)

	var personsReqs int32
	client := yzjNewClient(t, func(w http.ResponseWriter, r *http.Request) {
		if yzjIsToken(r) {
			yzjWriteToken(w)
			return
		}
		atomic.AddInt32(&personsReqs, 1)
		begin := yzjBegin(r)
		// 永远返回满页（2 条），迫使主循环依赖 maxPages 护栏才停。
		yzjWritePersons(w, []map[string]string{yzjPerson("", ""), yzjPerson("", "")},
			fmt.Sprintf("2026-01-01 00:00:%02d", begin/2+1))
	})
	svc := yzjService(st, client, "eid-test", true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := svc.syncYunzhijiaOrg(ctx, yzjOrgOptions{
		source: source, resource: resource, triggeredBy: "test", pageSize: 2, maxPages: 3,
	})
	if err != nil {
		t.Fatalf("同步出错（不应因满页而死循环/超时）: %v", err)
	}
	if res.Pages != 3 {
		t.Fatalf("应在 maxPages=3 处停下，实际页数 %d", res.Pages)
	}
	if got := atomic.LoadInt32(&personsReqs); got != 3 {
		t.Fatalf("应恰好请求 3 次，实际 %d（护栏失效？）", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. 增量覆盖语义：同一工号前后两页 openId 不同 → 落库为**后出现**的那个
// ─────────────────────────────────────────────────────────────────────────────
func TestYzjOrgLaterOpenIDOverwrites(t *testing.T) {
	st := yzjTestStore(t)
	source, resource := yzjProbeNames()
	yzjCleanup(t, st, source)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	empNo := "ZZ-YZJ-OVERWRITE-" + suffix
	res, err := st.DB().Exec(`INSERT INTO employee (emp_no, name, active) VALUES (?, 'ZZ 探针-覆盖', 1)`, empNo)
	if err != nil {
		t.Fatalf("建探针员工失败: %v", err)
	}
	empID, _ := res.LastInsertId()
	t.Cleanup(func() { _, _ = st.DB().Exec(`DELETE FROM employee WHERE id = ?`, empID) })

	client := yzjNewClient(t, func(w http.ResponseWriter, r *http.Request) {
		if yzjIsToken(r) {
			yzjWriteToken(w)
			return
		}
		if yzjBegin(r) == 0 {
			// 满页：该工号先出现，openId = first。
			yzjWritePersons(w, []map[string]string{yzjPerson(empNo, "openid-first"), yzjPerson("", "")}, "2026-01-01 00:00:01")
			return
		}
		// 第二页（不足）：同一工号再次出现，openId = second → 应覆盖 first。
		yzjWritePersons(w, []map[string]string{yzjPerson(empNo, "openid-second")}, "2026-01-01 00:00:02")
	})
	svc := yzjService(st, client, "eid-test", true)

	if _, err := svc.syncYunzhijiaOrg(context.Background(), yzjOrgOptions{
		source: source, resource: resource, triggeredBy: "test", pageSize: 2,
	}); err != nil {
		t.Fatalf("同步出错: %v", err)
	}

	got, err := st.EmployeeOpenID(empID)
	if err != nil {
		t.Fatalf("读 open_id 失败: %v", err)
	}
	if got != "openid-second" {
		t.Fatalf("后出现的 openId 应覆盖先前的，期望 openid-second，实际 %q", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. 命中分类：工号重复=真失败（落 error）、查无此人=正常（不落 error）、唯一命中=回填
//    且都不中断后续人员
// ─────────────────────────────────────────────────────────────────────────────
func TestYzjOrgDuplicateEmpNoRecordsErrorAndContinues(t *testing.T) {
	st := yzjTestStore(t)
	source, resource := yzjProbeNames()
	yzjCleanup(t, st, source)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	dupNo := "ZZ-YZJ-DUP-" + suffix
	goodNo := "ZZ-YZJ-GOOD-" + suffix
	missingNo := "ZZ-YZJ-NONE-" + suffix

	r1, err := st.DB().Exec(`INSERT INTO employee (emp_no, name, active) VALUES (?, 'ZZ 探针-重复A', 1)`, dupNo)
	if err != nil {
		t.Fatalf("建重复工号 A 失败: %v", err)
	}
	idA, _ := r1.LastInsertId()
	r2, err := st.DB().Exec(`INSERT INTO employee (emp_no, name, active) VALUES (?, 'ZZ 探针-重复B', 1)`, dupNo)
	if err != nil {
		t.Fatalf("建重复工号 B 失败: %v", err)
	}
	idB, _ := r2.LastInsertId()
	r3, err := st.DB().Exec(`INSERT INTO employee (emp_no, name, active) VALUES (?, 'ZZ 探针-正常', 1)`, goodNo)
	if err != nil {
		t.Fatalf("建正常工号失败: %v", err)
	}
	idGood, _ := r3.LastInsertId()
	t.Cleanup(func() {
		_, _ = st.DB().Exec(`DELETE FROM employee WHERE emp_no IN (?, ?, ?)`, dupNo, goodNo, missingNo)
	})

	client := yzjNewClient(t, func(w http.ResponseWriter, r *http.Request) {
		if yzjIsToken(r) {
			yzjWriteToken(w)
			return
		}
		// 不足一页：dupNo（工号不唯一）、goodNo（唯一）、missingNo（查无此人）。
		yzjWritePersons(w, []map[string]string{
			yzjPerson(dupNo, "openid-dup"),
			yzjPerson(goodNo, "openid-good"),
			yzjPerson(missingNo, "openid-none"),
		}, "2026-01-01 00:00:01")
	})
	svc := yzjService(st, client, "eid-test", true)

	res, err := svc.syncYunzhijiaOrg(context.Background(), yzjOrgOptions{
		source: source, resource: resource, triggeredBy: "test", pageSize: 10,
	})
	if err != nil {
		t.Fatalf("同步出错: %v", err)
	}
	if res.Updated != 1 {
		t.Fatalf("应成功回填 1 人（goodNo），实际 %d", res.Updated)
	}
	// 只有 dupNo（工号重复）是真异常；missingNo（本系统无此工号）是**正常情况**。
	if res.Failed != 1 {
		t.Fatalf("应有 1 人失败（仅 dupNo 工号重复），实际 %d", res.Failed)
	}
	if res.NotInSystem != 1 {
		t.Fatalf("missingNo（本系统无此工号）应计 1 人，实际 %d", res.NotInSystem)
	}
	if res.Status != model.SyncStatusPartial {
		t.Fatalf("有真失败时应为 partial，实际 %q", res.Status)
	}

	// 工号重复必须留下 sync_run_error（可见的真异常）。
	if n := yzjCountErrors(t, st, source, dupNo); n != 1 {
		t.Fatalf("dupNo 应落 1 条 sync_run_error，实际 %d", n)
	}
	// 「本系统无此工号」是正常情况：**绝不**写 sync_run_error（否则每次跑批写数千条、表被撑爆）。
	if n := yzjCountErrors(t, st, source, missingNo); n != 0 {
		t.Fatalf("missingNo（本系统无此工号）不得写 sync_run_error，实际 %d 条", n)
	}

	// 「不中断」：goodNo 在 dupNo 之后仍被处理。
	if got, err := st.EmployeeOpenID(idGood); err != nil || got != "openid-good" {
		t.Fatalf("后续人员应仍被处理：open_id=%q err=%v", got, err)
	}
	// 工号重复时两行都不得被写。
	for _, id := range []int64{idA, idB} {
		if got, err := st.EmployeeOpenID(id); err != nil || got != "" {
			t.Fatalf("工号重复时不得写入：id=%d open_id=%q err=%v", id, got, err)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4b. 「本系统无此工号」是**正常情况**，不是失败
//
// 云之家通讯录（约 4465 人，整个工作圈）远大于本系统花名册（employee 表仅百余人），
// 因此绝大多数通讯录人员在本系统**没有对应工号**。若把它记为失败：
//   - 退出码恒为 1、用户以为同步坏了（信号被噪声淹没）；
//   - 每跑一次写数千条 sync_run_error（接入每日批次后表会被撑爆）。
//
// 本测试钉死：这类人计入 NotInSystem、**不计 Failed**、**不写 sync_run_error**，
// 且整轮 status 仍是 success（不是 partial）——这是「判读信号干净」的核心保证。
// ─────────────────────────────────────────────────────────────────────────────
func TestYzjOrgNotInSystemIsNormalNotFailure(t *testing.T) {
	st := yzjTestStore(t)
	source, resource := yzjProbeNames()
	yzjCleanup(t, st, source)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	goodNo := "ZZ-YZJ-INSYS-" + suffix
	missingNo := "ZZ-YZJ-NOSYS-" + suffix

	r1, err := st.DB().Exec(`INSERT INTO employee (emp_no, name, active) VALUES (?, 'ZZ 探针-在册', 1)`, goodNo)
	if err != nil {
		t.Fatalf("建在册探针失败: %v", err)
	}
	idGood, _ := r1.LastInsertId()
	t.Cleanup(func() {
		_, _ = st.DB().Exec(`DELETE FROM employee WHERE emp_no IN (?, ?)`, goodNo, missingNo)
	})

	client := yzjNewClient(t, func(w http.ResponseWriter, r *http.Request) {
		if yzjIsToken(r) {
			yzjWriteToken(w)
			return
		}
		// 不足一页：一个本系统有（唯一命中），一个本系统无（正常跳过）。
		yzjWritePersons(w, []map[string]string{
			yzjPerson(goodNo, "openid-good"),
			yzjPerson(missingNo, "openid-none"),
		}, "2026-03-03 00:00:01")
	})
	svc := yzjService(st, client, "eid-test", true)

	res, err := svc.syncYunzhijiaOrg(context.Background(), yzjOrgOptions{
		source: source, resource: resource, triggeredBy: "test", pageSize: 10,
	})
	if err != nil {
		t.Fatalf("同步出错: %v", err)
	}
	// 唯一命中 → Updated 增 1。
	if res.Updated != 1 {
		t.Fatalf("应成功回填 1 人（本系统有此人），实际 %d", res.Updated)
	}
	// 本系统无此工号 → NotInSystem 增 1。
	if res.NotInSystem != 1 {
		t.Fatalf("「本系统无此工号」应计 1 人，实际 %d", res.NotInSystem)
	}
	// 且**不得**计入 Failed。
	if res.Failed != 0 {
		t.Fatalf("「本系统无此工号」不得计入 Failed，实际 Failed=%d", res.Failed)
	}
	// 整轮必须判为 success（不是 partial）——否则运行状态/退出码会被正常情况污染。
	if res.Status != model.SyncStatusSuccess {
		t.Fatalf("只有正常跳过时状态应为 success，实际 %q", res.Status)
	}
	// 关键：绝不写 sync_run_error（防表被撑爆）。
	if n := yzjCountErrors(t, st, source, missingNo); n != 0 {
		t.Fatalf("「本系统无此工号」不得写 sync_run_error，实际 %d 条", n)
	}
	// 本系统有的人照常被回填。
	if got, err := st.EmployeeOpenID(idGood); err != nil || got != "openid-good" {
		t.Fatalf("本系统有此人应被回填：open_id=%q err=%v", got, err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. 游标只在成功后推进
// ─────────────────────────────────────────────────────────────────────────────
func TestYzjOrgCursorAdvancesOnSuccess(t *testing.T) {
	st := yzjTestStore(t)
	source, resource := yzjProbeNames()
	yzjCleanup(t, st, source)

	client := yzjNewClient(t, func(w http.ResponseWriter, r *http.Request) {
		if yzjIsToken(r) {
			yzjWriteToken(w)
			return
		}
		if yzjBegin(r) == 0 {
			yzjWritePersons(w, []map[string]string{yzjPerson("", ""), yzjPerson("", "")}, "2026-02-02 03:04:01")
			return
		}
		yzjWritePersons(w, []map[string]string{yzjPerson("", "")}, "2026-02-02 03:04:05")
	})
	svc := yzjService(st, client, "eid-test", true)

	res, err := svc.syncYunzhijiaOrg(context.Background(), yzjOrgOptions{
		source: source, resource: resource, triggeredBy: "test", pageSize: 2,
	})
	if err != nil {
		t.Fatalf("同步出错: %v", err)
	}
	if res.Cursor != "2026-02-02 03:04:05" {
		t.Fatalf("游标应为最后一页的 time，实际 %q", res.Cursor)
	}
	state, err := st.GetSyncState(source, resource)
	if err != nil {
		t.Fatalf("读 sync_state 失败: %v", err)
	}
	if state == nil || state.CursorValue != "2026-02-02 03:04:05" {
		t.Fatalf("成功后应推进游标到 2026-02-02 03:04:05，实际 %+v", state)
	}
}

func TestYzjOrgCursorNotAdvancedOnFailure(t *testing.T) {
	st := yzjTestStore(t)
	source, resource := yzjProbeNames()
	yzjCleanup(t, st, source)

	var fail atomic.Bool
	client := yzjNewClient(t, func(w http.ResponseWriter, r *http.Request) {
		if yzjIsToken(r) {
			yzjWriteToken(w)
			return
		}
		begin := yzjBegin(r)
		if begin == 0 {
			yzjWritePersons(w, []map[string]string{yzjPerson("", ""), yzjPerson("", "")}, "2026-02-02 03:04:01")
			return
		}
		// 中途失败：第二页 500。
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		yzjWritePersons(w, []map[string]string{yzjPerson("", "")}, "2026-02-02 03:04:05")
	})
	svc := yzjService(st, client, "eid-test", true)

	// 第一次：成功 → 游标推进到 T1。
	if _, err := svc.syncYunzhijiaOrg(context.Background(), yzjOrgOptions{
		source: source, resource: resource, triggeredBy: "test", pageSize: 2,
	}); err != nil {
		t.Fatalf("首次同步出错: %v", err)
	}
	const t1 = "2026-02-02 03:04:05"
	if state, _ := st.GetSyncState(source, resource); state == nil || state.CursorValue != t1 {
		t.Fatalf("首次成功后游标应为 %q，实际 %+v", t1, state)
	}

	// 第二次：中途 500 → 失败，游标**保持不变**。
	fail.Store(true)
	res, err := svc.syncYunzhijiaOrg(context.Background(), yzjOrgOptions{
		source: source, resource: resource, triggeredBy: "test", pageSize: 2,
	})
	if err == nil {
		t.Fatalf("第二页 500 时同步应返回错误")
	}
	if res == nil || res.Status != model.SyncStatusFailed {
		t.Fatalf("失败运行状态应为 failed，实际 %+v", res)
	}
	state, err := st.GetSyncState(source, resource)
	if err != nil {
		t.Fatalf("读 sync_state 失败: %v", err)
	}
	if state == nil || state.CursorValue != t1 {
		t.Fatalf("失败时游标不得推进，应仍为 %q，实际 %+v", t1, state)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. 门禁：关闭开关 / eid 为空 → 零请求且返回 nil error
// ─────────────────────────────────────────────────────────────────────────────
func TestYzjOrgGateDisabledSendsNoRequest(t *testing.T) {
	var hits int32
	client := yzjNewClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	})
	// 门禁在触库前返回，故用 nil store 也可（同时钉死"不碰库"）。
	svc := yzjService(nil, client, "eid-test", false)

	res, err := svc.SyncYunzhijiaOrg(context.Background(), "test")
	if err != nil {
		t.Fatalf("关闭开关时应正常返回，实际 error=%v", err)
	}
	if res != nil {
		t.Fatalf("关闭开关时不应有结果，实际 %+v", res)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("关闭开关时不得发起任何请求，实际 %d", got)
	}
}

func TestYzjOrgGateEmptyEIDSendsNoRequest(t *testing.T) {
	var hits int32
	client := yzjNewClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	})
	svc := yzjService(nil, client, "", true)

	res, err := svc.SyncYunzhijiaOrg(context.Background(), "test")
	if err != nil {
		t.Fatalf("eid 为空时应正常返回（不是失败），实际 error=%v", err)
	}
	if res != nil {
		t.Fatalf("eid 为空时不应有结果，实际 %+v", res)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("eid 为空时不得发起任何请求，实际 %d", got)
	}
}

func intSliceEq(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
