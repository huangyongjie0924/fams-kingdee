package syncer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"asset-mgr/config"
	"asset-mgr/integration/kingdee"
	"asset-mgr/model"
	"asset-mgr/store"
)

const (
	sourceName   = "kingdee"
	resourceCard = "asset_card"
	lockName     = "asset_mgr_sync_kingdee"
	// syncOperator 同步写入履历时记录的操作人
	syncOperator = "kingdee-sync"
	// dryRunPendingMasterID dry-run 下代表"本应新建的主数据"，仅用于比对出受影响资产，不会落库
	dryRunPendingMasterID = -1
)

// Service 编排金蝶资产卡同步。
type Service struct {
	st     *store.Store
	client *kingdee.Client
	cfg    *config.Config
	mu     sync.Mutex
	// pendingMasters 记录 dry-run 期间本应新建的主数据，键为 kind|extKey|name。
	// 仅在持有 mu 的 Run 内使用。
	pendingMasters map[string]bool
	// resolveCache 缓存本次运行内的主数据解析结果，键为 kind|extKey。
	// 必须缓存：同步在主事务内新建的主数据对 s.db 上的查询（另一条连接）不可见，
	// 不缓存会对同一主数据反复新建。仅在持有 mu 的 Run 内使用。
	resolveCache map[string]int64
}

// NewService 创建同步服务。调用方应保证 kingdee 配置完整。
func NewService(st *store.Store, client *kingdee.Client, cfg *config.Config) *Service {
	return &Service{st: st, client: client, cfg: cfg}
}

// TestConnect 验证金蝶 token 接口连通性。
func (s *Service) TestConnect(ctx context.Context) error {
	if s.client == nil {
		return errors.New("kingdee client not configured")
	}
	return s.client.TestConnect(ctx)
}

// Run 执行一次同步。mode 为 incremental 或 full。
func (s *Service) Run(ctx context.Context, mode, triggeredBy string) (*model.SyncRun, error) {
	if s.client == nil {
		return nil, errors.New("kingdee client not configured")
	}

	// 进程内互斥
	s.mu.Lock()
	defer s.mu.Unlock()

	// 数据库 advisory lock 防止多实例并发
	conn, err := s.st.DB().Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("get db conn: %w", err)
	}
	defer conn.Close()

	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 10)", lockName).Scan(&locked); err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	if !locked {
		return nil, errors.New("无法获取同步锁，可能其他实例正在同步")
	}
	defer conn.ExecContext(ctx, "DO RELEASE_LOCK(?)", lockName)

	tx, err := s.st.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	state, err := s.st.GetSyncState(sourceName, resourceCard)
	if err != nil {
		return nil, err
	}

	cursor := ""
	if mode == model.SyncModeIncremental && state != nil {
		cursor = state.CursorValue
	}

	runID, err := s.st.CreateSyncRun(tx, sourceName, mode, triggeredBy, cursor)
	if err != nil {
		return nil, err
	}

	// 增量游标仍然带上，但实测 Select_AssetCard 会忽略该过滤条件：
	// incremental 每次仍拉回全量（幂等所以结果正确，只是不省流量）。保留是为了
	// 接口开始支持时自动生效，不能据此认为"未返回=已删除"，故删除只在 full 模式做。
	filter := ""
	if mode == model.SyncModeIncremental && cursor != "" {
		filter = fmt.Sprintf("modifytime > '%s'", cursor)
	}

	cards, err := s.client.SelectAssetCards(ctx, filter)
	if err != nil {
		_ = store.FinishSyncRun(tx, runID, model.SyncStatusFailed, err.Error())
		_ = store.UpdateSyncRunStats(tx, runID, model.SyncRun{FailedCount: 1})
		if cErr := tx.Commit(); cErr != nil {
			return nil, cErr
		}
		return &model.SyncRun{ID: runID, Status: model.SyncStatusFailed}, nil
	}

	// 接口对同一资产编号可能返回多张卡（合并卡），且返回顺序不稳定。
	// 排序后同编号内最后处理的是 modifytime 最新、其次 id 最大的一张，结果可复现。
	sortCards(cards)

	// dry-run 不写库，同编号的多张合并卡会各自与同一份旧数据比对，
	// 于是 affected 数量只会高估、不会低估（实测 predicted updated=30 / actual 15）。
	// 作为上线闸门是安全的，不必强求精确。
	s.pendingMasters = nil
	s.resolveCache = make(map[string]int64)
	if s.cfg.Sync.DryRun {
		s.pendingMasters = make(map[string]bool)
	}

	stats := model.SyncRun{TotalCount: len(cards)}
	activeExternalIDs := make(map[string]bool, len(cards))
	maxModifyTime := cursor

	for _, src := range cards {
		activeExternalIDs[src.ID] = true
		if t := parseDateTime(src.ModifyTime); t != "" && (maxModifyTime == "" || t > maxModifyTime) {
			maxModifyTime = t
		}

		action, err := s.applyOne(tx, runID, &src)
		switch action {
		case store.CardActionCreated:
			stats.CreatedCount++
		case store.CardActionUpdated:
			stats.UpdatedCount++
		case store.CardActionUnchanged:
			stats.SkippedCount++
		}
		if err != nil {
			stats.FailedCount++
			retryable := !errors.Is(err, errMappingPermanent)
			_ = store.AddSyncRunError(tx, runID, model.SyncRunError{
				ExternalID:   src.ID,
				Stage:        "apply",
				ErrorMessage: truncate(err.Error(), 500),
				Retryable:    retryable,
			})
		}
	}

	// 完整同步成功后，处理本地已存在但本次未返回的外部资产（软删除）
	if mode == model.SyncModeFull {
		deleted, err := s.softDeleteMissing(tx, runID, activeExternalIDs, s.cfg.Sync.DryRun)
		if err != nil {
			stats.FailedCount++
			_ = store.AddSyncRunError(tx, runID, model.SyncRunError{
				Stage:        "delete_missing",
				ErrorMessage: truncate(err.Error(), 500),
				Retryable:    true,
			})
		} else {
			stats.DeletedCount = deleted
		}
	}

	status := model.SyncStatusSuccess
	if stats.FailedCount > 0 {
		status = model.SyncStatusPartial
	}
	summary := fmt.Sprintf("total=%d created=%d updated=%d skipped=%d deleted=%d failed=%d",
		stats.TotalCount, stats.CreatedCount, stats.UpdatedCount, stats.SkippedCount, stats.DeletedCount, stats.FailedCount)
	if s.cfg.Sync.DryRun {
		summary += fmt.Sprintf(" masters_to_create=%d", len(s.pendingMasters))
	}
	if err := store.UpdateSyncRunStats(tx, runID, stats); err != nil {
		return nil, err
	}
	if err := store.FinishSyncRun(tx, runID, status, summary); err != nil {
		return nil, err
	}

	// 只有成功或部分成功才推进游标
	if status != model.SyncStatusFailed && maxModifyTime != "" {
		if err := store.UpdateSyncState(tx, sourceName, resourceCard, maxModifyTime, runID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &model.SyncRun{
		ID:           runID,
		Source:       sourceName,
		Mode:         mode,
		TriggeredBy:  triggeredBy,
		CursorValue:  maxModifyTime,
		Status:       status,
		TotalCount:   stats.TotalCount,
		CreatedCount: stats.CreatedCount,
		UpdatedCount: stats.UpdatedCount,
		SkippedCount: stats.SkippedCount,
		DeletedCount: stats.DeletedCount,
		FailedCount:  stats.FailedCount,
		ErrorSummary: summary,
	}, nil
}

// sortCards 让同资产编号的多张外部卡按 (modifytime, id) 升序排列，
// 使编号相同时最后落库的是最新的一条，跨次同步结果稳定。
func sortCards(cards []kingdee.AssetCard) {
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].Number != cards[j].Number {
			return cards[i].Number < cards[j].Number
		}
		ti, tj := parseDateTime(cards[i].ModifyTime), parseDateTime(cards[j].ModifyTime)
		if ti != tj {
			return ti < tj
		}
		return cardIDLess(cards[i].ID, cards[j].ID)
	})
}

// cardIDLess 比较金蝶 id；id 为十进制雪花号，按长度+字典序等价于按数值比较。
func cardIDLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

var errMappingPermanent = errors.New("主数据映射失败")

func (s *Service) applyOne(tx *sql.Tx, runID int64, src *kingdee.AssetCard) (string, error) {
	payloadHash, err := store.HashPayload(src)
	if err != nil {
		return "", err
	}

	c, mappingErr := s.mapCard(tx, src)
	if mappingErr != nil {
		return "", mappingErr
	}

	cardID, action, err := store.ApplyOwnedCardTx(tx, c, syncOperator, s.cfg.Sync.DryRun)
	if err != nil {
		return "", err
	}

	if s.cfg.Sync.DryRun {
		return action, nil
	}

	m := &model.ExternalAssetMap{
		Source:          sourceName,
		ExternalID:      src.ID,
		ExternalCode:    src.Number,
		CardID:          cardID,
		ExternalVersion: src.ModifyTime,
		PayloadHash:     payloadHash,
		LastSyncRunID:   runID,
		Status:          model.ExternalMapStatusActive,
	}
	if err := store.UpsertExternalAssetMap(tx, m); err != nil {
		return "", err
	}

	return action, nil
}

func (s *Service) mapCard(tx *sql.Tx, src *kingdee.AssetCard) (*model.AssetCard, error) {
	if strings.TrimSpace(src.Number) == "" {
		return nil, fmt.Errorf("%w: 金蝶资产编码为空 (id=%s)", errMappingPermanent, src.ID)
	}

	// 只映射金蝶真正提供、且台账托管的字段。
	// 数量（assetamount）金蝶必给，正常同步；金额类（含税金额 price、财务分录的原值/净值）
	// 实测全为 0，由 mergeOwnedFields 只在大于 0 时覆盖，避免把人工填的金额抹掉。
	// 金蝶不返回税额，原始报文一律进 ext_json。
	c := &model.AssetCard{
		AssetCode: src.Number,
		Name:      src.AssetName,
		Spec:      src.Model,
		Unit:      src.UnitName,
		// 星瀚的 assetamount 就是「数量」：房屋按平方米（194.5200000000），设备按台/辆。
		// 计量单位在 unit_name（平方米 / 台 / 辆），已映射到 c.Unit。
		Quantity:     parseAmount(src.AssetAmount),
		Source:       "金蝶同步",
		FinAssetType: src.AssetCategoryName,
		// 金蝶没有独立的「资产类型」字段，按口径用资产类别名称填充；price 大于 0 时才是含税金额
		FinAmountWithTax: parseAmount(src.Price),
	}
	// 财务分录子表：实测每张卡最多一条。这里照实解析，但星瀚全库给的都是 0——
	// 资产原值 / 累计折旧 / 净值取不到，只能人工维护，详见 store.mergeOwnedFields。
	if len(src.FinEntry) > 0 {
		c.FinOriginalValue = parseAmount(src.FinEntry[0].FinOriginalVal)
		c.FinNetValue = parseAmount(src.FinEntry[0].FinNetWorth)
	}

	// 金蝶的 usestatus（使用状态）原样存进 use_status；同时按映射表折算成台账状态枚举，
	// 折算不出时保持空，由 mergeOwnedFields 保留台账本地状态。金蝶 bizstatus 仍不参与。
	if st, ok := mapUseStatus(src.UseStatusName); ok {
		c.Status = st
	}

	purchaseDate := parseDate(src.RealAccountDate)
	if purchaseDate == "" {
		purchaseDate = parseDate(src.UsedDate)
	}
	c.PurchaseDate = purchaseDate

	// 主数据映射：编码优先（金蝶编码稳定唯一），名称只在本地唯一命中时兜底，都匹配不到则按金蝶自动创建
	catID, err := s.resolveMaster(tx, model.MasterKindCategory, src.AssetCategoryNumber, src.AssetCategoryName,
		s.st.LookupCategoryByCode,
		func(name string) (int64, error) { return s.st.LookupUniqueByName("asset_category", name) })
	if err != nil {
		return nil, err
	}
	if catID == 0 && !s.cfg.Sync.DryRun {
		return nil, fmt.Errorf("%w: 资产类别 %s", errMappingPermanent, src.AssetCategoryNumber)
	}
	c.CategoryID = catID

	// 复用类别上的使用期限与残值率（新建时初始化，后续作为本地字段保留）
	if catID > 0 {
		if months, rate, err := s.st.LookupCategoryMonthsAndRate(catID); err == nil {
			c.UseMonths = months
			c.FinUseMonths = months
			c.FinResidualRate = rate
		}
	}

	deptID, err := s.resolveMaster(tx, model.MasterKindDepartment, src.UseDepartmentNumber, src.UseDepartmentName,
		s.st.LookupDepartmentByCode,
		func(name string) (int64, error) { return s.st.LookupUniqueByName("department", name) })
	if err != nil {
		return nil, err
	}
	c.UseDeptID = deptID

	userID, err := s.resolveMaster(tx, model.MasterKindEmployee, src.HeadUsePersonNumber, src.HeadUsePersonName,
		s.st.LookupEmployeeByNumber,
		func(name string) (int64, error) { return s.st.LookupUniqueByName("employee", name) })
	if err != nil {
		return nil, err
	}
	c.UserEmpID = userID
	c.UseStatus = src.UseStatusName
	c.CardCreatedAt = parseDateTime(src.CreateTime)

	vendorID, err := s.resolveMaster(tx, model.MasterKindVendor, src.SupplierNumber, src.SupplierName,
		s.st.LookupVendorByCode,
		func(name string) (int64, error) { return s.st.LookupUniqueByName("vendor", name) })
	if err != nil {
		return nil, err
	}
	c.VendorID = vendorID

	// 金蝶的「资产组织」是资产的归属法人，映射到台账「所属公司」
	companyID, err := s.resolveMaster(tx, model.MasterKindCompany, src.AssetUnitNumber, src.AssetUnitName,
		s.st.LookupCompanyByCode,
		func(name string) (int64, error) { return s.st.LookupUniqueByName("company", name) })
	if err != nil {
		return nil, err
	}
	c.OwnerCompanyID = companyID

	areaID, err := s.resolveMaster(tx, model.MasterKindArea, src.StorePlaceNumber, src.StorePlaceName,
		s.st.LookupAreaByCode,
		func(name string) (int64, error) { return s.st.LookupUniqueByName("asset_area", name) })
	if err != nil {
		return nil, err
	}
	c.AreaID = areaID
	c.Location = src.StorePlaceName

	// 未映射字段存入 ext_json 便于排查
	if b, err := json.Marshal(src); err == nil {
		c.ExtJSON = string(b)
	}

	return c, nil
}

// resolveMaster 把外部主数据引用解析成本地 ID。
// 顺序：本次运行缓存 → 外部映射表 → 本地编码 → 本地名称 → 自动创建（金蝶为主数据源）。
// 自动创建要求名称为非空，否则只能返回 0 由台账侧人工补齐。
func (s *Service) resolveMaster(tx *sql.Tx, kind, code, name string,
	byCode func(string) (int64, error), byName func(string) (int64, error)) (int64, error) {
	code, name = strings.TrimSpace(code), strings.TrimSpace(name)
	if code == "" && name == "" {
		return 0, nil
	}

	// 外部编码可能为空（金蝶供应商就是这样），此时用名称当映射身份，
	// 否则所有无编码的主数据会挤在 external_id='' 上互相覆盖。
	extKey := code
	if extKey == "" {
		extKey = name
	}
	cacheKey := kind + "|" + extKey
	if id, ok := s.resolveCache[cacheKey]; ok {
		return id, nil
	}

	// dry-run 必须完全只读：命中映射的登记也一并跳过
	remember := func(id int64) error {
		if s.cfg.Sync.DryRun {
			return nil
		}
		return store.UpsertExternalMasterMap(tx, &model.ExternalMasterMap{
			Source:       sourceName,
			Kind:         kind,
			ExternalID:   extKey,
			ExternalCode: code,
			ExternalName: name,
			LocalID:      id,
		})
	}

	// 命中的解析结果统一登记映射并写缓存
	found := func(id int64) (int64, error) {
		if err := remember(id); err != nil {
			return 0, err
		}
		s.resolveCache[cacheKey] = id
		return id, nil
	}

	// 1. 外部映射表
	if id, err := s.st.LookupExternalMasterMap(sourceName, kind, extKey); err != nil {
		return 0, err
	} else if id > 0 {
		s.resolveCache[cacheKey] = id
		return id, nil
	}
	// 2. 按编码
	if code != "" {
		if id, err := byCode(code); err != nil {
			return 0, err
		} else if id > 0 {
			return found(id)
		}
	}
	// 3. 按名称兜底
	if name != "" {
		if id, err := byName(name); err != nil {
			return 0, err
		} else if id > 0 {
			return found(id)
		}
	}
	// 4. 自动创建
	if name == "" {
		return 0, nil
	}
	if s.cfg.Sync.DryRun {
		if s.pendingMasters != nil {
			s.pendingMasters[kind+"|"+extKey+"|"+name] = true
		}
		// -1 占位：让比对结果与现有 0 不同，dry-run 才能如实报告会受影响的资产
		s.resolveCache[cacheKey] = dryRunPendingMasterID
		return dryRunPendingMasterID, nil
	}
	id, err := store.CreateMasterTx(tx, kind, code, name)
	if err != nil {
		return 0, err
	}
	return found(id)
}

func (s *Service) softDeleteMissing(tx *sql.Tx, runID int64, activeExternalIDs map[string]bool, dryRun bool) (int, error) {
	rows, err := tx.Query(`SELECT m.card_id, m.external_id, c.deleted_at FROM external_asset_map m
		LEFT JOIN asset_card c ON c.id = m.card_id
		WHERE m.source = ? AND m.status = ? AND m.deleted_at IS NULL`, sourceName, model.ExternalMapStatusActive)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	deleted := 0
	seen := make(map[int64]bool)
	for rows.Next() {
		var cardID int64
		var extID string
		var cardDeleted sql.NullTime
		if err := rows.Scan(&cardID, &extID, &cardDeleted); err != nil {
			return 0, err
		}
		if activeExternalIDs[extID] || cardDeleted.Valid || seen[cardID] {
			continue
		}
		seen[cardID] = true
		if dryRun {
			deleted++
			continue
		}
		// 同一张台账卡可能被多张外部卡引用，第一张删掉后其余会拿到 ErrNoRows，忽略即可
		if err := store.SoftDeleteCardTx(tx, cardID, syncOperator); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return deleted, err
		}
		if _, err := tx.Exec(`UPDATE external_asset_map SET status = ?, deleted_at = ?, last_sync_run_id = ?
			WHERE source = ? AND external_id = ?`, model.ExternalMapStatusDeleted, time.Now(), runID, sourceName, extID); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, rows.Err()
}

func parseDateTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Format("2006-01-02 15:04:05")
		}
	}
	return ""
}

// useStatusMap 金蝶「使用状态」（usestatus_name）→ 台账状态枚举。
// 实测金蝶 227 张卡全是「正常使用」，其余取值待用户确认后补充；
// 未命中的一律不写状态，保留台账本地维护值。
var useStatusMap = map[string]string{
	"正常使用": model.StatusInUse,
}

func mapUseStatus(name string) (string, bool) {
	st, ok := useStatusMap[strings.TrimSpace(name)]
	return st, ok
}

// parseAmount 把金蝶的数值字段转成 float64。用 json.Number 解码，取不到就按 0 处理。
func parseAmount(n json.Number) float64 {
	f, err := n.Float64()
	if err != nil {
		return 0
	}
	return f
}

func parseDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", "2006/01/02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
