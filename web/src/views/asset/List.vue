<template>
  <div class="page" :class="{ 'has-selbar': selection.length > 0 }">
    <!-- 页头：标题 + 主操作同一行 ——「一屏一个主按钮」的落点（方案 §5.1 / §6.2） -->
    <div class="page-head">
      <div class="ph-title">
        <h1>资产台账</h1>
        <span class="ph-sub">共 <b>{{ total }}</b> 条</span>
        <span v-if="auth.scopeHint" class="ph-sub">{{ auth.scopeHint }}</span>
      </div>
      <div class="ph-actions">
        <!-- 扫码找资产：本系统的特殊功能（设备上贴的标签就是它扫的码），从「更多」下拉里
             提出来单独放在页头 —— 不限权限，所有角色都能看到。
             信号黄只做左侧描边，不做文字/填充：白底 1.85:1 达不到对比度。 -->
        <el-button class="scan-btn" :icon="Camera" @click="router.push({ name: 'scan' })">
          扫码找资产
        </el-button>
        <el-button
          v-if="auth.can('asset.manage')"
          type="primary"
          :icon="Plus"
          @click="goNew"
        >
          新建资产
        </el-button>
        <el-button
          v-if="auth.can('asset.manage')"
          :icon="Upload"
          @click="router.push({ name: 'import' })"
        >
          导入
        </el-button>
      </div>
    </div>

    <!-- 工具栏：搜索 / 已启用筛选条件回显 / 筛选 / 更多（方案 §5.3） -->
    <div class="page-toolbar">
      <el-input
        v-model="query.keyword"
        class="tb-search"
        :placeholder="isMobile ? '编码/名称/使用人/地点' : '编码/名称/规格/序列号/RFID/使用人/部门/地点'"
        clearable
        @keyup.enter="reload(1)"
        @clear="reload(1)"
      >
        <template #prefix><el-icon><Search /></el-icon></template>
      </el-input>

      <!-- 把「已启用 N 个筛选条件」的计数换成具体条件，用户随时知道在看什么子集 -->
      <span v-for="f in filterChips" :key="f.key" class="fchip">
        <span class="fchip-txt">{{ f.label }}</span>
        <el-icon class="fchip-x" @click="clearFilter(f.key)"><Close /></el-icon>
      </span>

      <el-button :icon="Filter" @click="searchDrawer = true">
        筛选<span v-if="activeFilterCount" class="tb-badge">{{ activeFilterCount }}</span>
      </el-button>

      <!-- 窄屏没有多选列，进批量选择必须留一个入口 -->
      <el-button
        v-if="isMobile && auth.can('asset.manage')"
        :type="batchMode ? 'primary' : 'default'"
        @click="toggleBatch"
      >
        {{ batchMode ? "退出批量" : "批量" }}
      </el-button>

      <!-- 导出/列配置都只对桌面有意义；扫码已提到页头 ⇒ 窄屏不再渲染「更多」，
           否则会剩一个点开什么都没有的空下拉 -->
      <el-dropdown v-if="!isMobile" trigger="click" @command="onMore">
        <el-button>
          更多<el-icon><ArrowDown /></el-icon>
        </el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="export" :icon="Download">导出 Excel</el-dropdown-item>
            <el-dropdown-item command="columns" :icon="SetUp">列配置</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>

    <el-table
      v-if="!isMobile"
      ref="tableRef"
      v-loading="loading"
      :data="rows"
      border
      height="calc(100vh - 250px)"
      @selection-change="selection = $event"
      @sort-change="onSort"
    >
      <el-table-column type="selection" width="44" />
      <el-table-column
        v-for="col in visibleColumns"
        :key="col.prop"
        :prop="col.prop"
        :label="col.label"
        :width="col.width"
        :align="col.align || 'left'"
        :sortable="sortableProps.has(col.prop) ? 'custom' : false"
        show-overflow-tooltip
      >
        <template #default="{ row }">
          <!-- 实心 el-tag → 点 + 描边 chip；映射关系仍由 api/meta.ts 决定 -->
          <StatusChip v-if="col.prop === 'status'" :value="row.display_status" />
          <a v-else-if="col.prop === 'name'" class="link" @click="openDetail(row)">{{ row.name }}</a>
          <span v-else-if="col.prop === 'asset_code'" class="u-mono">{{ row[col.prop] }}</span>
          <span v-else-if="col.qty" class="u-mono">{{ qty(row[col.prop]) }}</span>
          <span v-else-if="col.money" class="u-mono">{{ money(row[col.prop]) }}</span>
          <span v-else>{{ row[col.prop] }}</span>
        </template>
      </el-table-column>
    </el-table>

    <!-- 窄屏用固定字段的卡片，不复用 visibleColumns：列配置对卡片没有意义 -->
    <div v-else v-loading="loading" class="m-list">
      <div v-for="row in rows" :key="row.id" class="m-card" @click="onCardTap(row)">
        <div class="m-card-hd">
          <el-checkbox
            v-if="batchMode"
            :model-value="isSelected(row)"
            @click.stop
            @change="toggleSelect(row, $event)"
          />
          <span class="m-title">{{ row.name }}</span>
          <StatusChip :value="row.display_status" />
        </div>
        <div class="m-grid">
          <span class="k">资产编码</span><span class="u-mono">{{ row.asset_code || "-" }}</span>
          <span class="k">使用部门</span><span>{{ row.use_dept_name || "-" }}</span>
          <span class="k">使用人</span><span>{{ row.user_emp_name || "-" }}</span>
          <span class="k">存放地点</span><span>{{ row.location || "-" }}</span>
        </div>
      </div>
      <el-empty v-if="!rows.length && !loading" description="暂无数据" />
    </div>

    <el-pagination
      v-model:current-page="query.page"
      v-model:page-size="query.page_size"
      :total="total"
      :page-sizes="[20, 50, 100, 200]"
      :layout="isMobile ? 'prev, pager, next' : 'total, sizes, prev, pager, next, jumper'"
      :pager-count="isMobile ? 5 : 7"
      style="margin-top: 12px; justify-content: flex-end"
      @change="reload()"
    />

    <!-- 选中浮条：未选中不显示。批量动作从工具栏搬到这里，既降噪，
         也消除「点了才提示请先选择」的挫败感（方案 §5.3）。 -->
    <div v-if="selection.length" class="selbar" role="region" aria-label="批量操作">
      <span class="selbar-count">已选 <b>{{ selection.length }}</b> 项</span>
      <el-button v-if="showManageActions" text class="sb-btn" :icon="Edit" :disabled="selection.length !== 1" @click="goEdit">
        编辑
      </el-button>
      <el-dropdown v-if="showManageActions" trigger="click" @command="changeStatus">
        <el-button text class="sb-btn" :icon="Switch">
          调整状态<el-icon><ArrowDown /></el-icon>
        </el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item v-for="s in statuses" :key="s" :command="s">
              <StatusChip :value="s" />
            </el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <el-button text class="sb-btn" :icon="Printer" @click="printLabels">打印标签</el-button>
      <el-button v-if="showManageActions" text class="sb-btn is-danger" :icon="Delete" @click="removeSelected">
        删除
      </el-button>
      <span class="selbar-spacer" />
      <el-button v-if="!isMobile" text class="sb-btn" @click="selectAllPage">全选本页</el-button>
      <el-button text class="sb-btn" :icon="Close" @click="clearSelection">取消</el-button>
    </div>

    <SearchDrawer v-model="searchDrawer" :query="query" @apply="reload(1)" />
    <ColumnDialog v-model="columnDialog" v-model:enabled="enabledProps" />
    <CardDetail v-model="detailVisible" :card-id="detailId" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  Plus, Edit, Delete, Download, Search, Filter, SetUp, Switch, ArrowDown, Camera, Printer,
  Upload, Close,
} from "@element-plus/icons-vue";
import http, { download } from "../../api/client";
import { ASSET_COLUMNS, money, qty, selectableStatuses } from "../../api/meta";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";
import { takeAssetCode } from "../../utils/deeplink";
import StatusChip from "../../components/StatusChip.vue";
import SearchDrawer from "./SearchDrawer.vue";
import ColumnDialog from "./ColumnDialog.vue";
import CardDetail from "./CardDetail.vue";

const route = useRoute();
const router = useRouter();
// 金额列改为默认隐藏后，老浏览器里存的旧配置会「压过」新默认值（已存值优先），
// 只改 meta.ts 的 defaultOn 对他们完全无效。所以换 key 做一次性迁移：
// 读旧 key → 剔掉金额列 → 写进新 key。迁移后新 key 是权威，用户再手动打开金额列能正常保留。
const COLUMN_KEY = "asset_list_columns_v2";
const LEGACY_COLUMN_KEY = "asset_list_columns";
const MONEY_PROPS = ASSET_COLUMNS.filter((c) => c.money).map((c) => c.prop);

/** 读一份列配置；没有 / 不是数组 / JSON 坏了都返回 null，交给调用方兜底。 */
function readStoredProps(key: string): string[] | null {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed : null;
  } catch {
    // 一条坏数据不该挡住整个列表渲染
    return null;
  }
}

function initialEnabledProps(): string[] {
  const defaults = ASSET_COLUMNS.filter((c) => c.defaultOn).map((c) => c.prop);
  const current = readStoredProps(COLUMN_KEY);
  if (current) return current;
  const legacy = readStoredProps(LEGACY_COLUMN_KEY);
  if (legacy) {
    const migrated = legacy.filter((p) => !MONEY_PROPS.includes(p));
    localStorage.setItem(COLUMN_KEY, JSON.stringify(migrated));
    localStorage.removeItem(LEGACY_COLUMN_KEY);
    return migrated;
  }
  return defaults;
}
const sortableProps = new Set(["asset_code", "name", "amount", "purchase_date", "status"]);

const isMobile = useIsMobile();
const batchMode = ref(false);
const loading = ref(false);
const rows = ref<any[]>([]);
const total = ref(0);
const selection = ref<any[]>([]);
const searchDrawer = ref(false);
const columnDialog = ref(false);
const detailVisible = ref(false);
const detailId = ref(0);
const tableRef = ref();

// 窄屏表格换卡片后没有多选列，选中只能靠「批量」模式勾卡片，管理类按钮平时收起来腾地方
const showManageActions = computed(
  () => auth.can("asset.manage") && (!isMobile.value || batchMode.value),
);

watch(batchMode, (on) => {
  if (!on) clearSelection();
});

function toggleBatch() {
  batchMode.value = !batchMode.value;
}

function isSelected(row: any) {
  return selection.value.some((r) => r.id === row.id);
}

function toggleSelect(row: any, checked: boolean) {
  selection.value = checked
    ? [...selection.value.filter((r) => r.id !== row.id), row]
    : selection.value.filter((r) => r.id !== row.id);
}

/** 全选/取消要同时改 el-table 自己的勾选状态，只改 selection 数组不会让复选框跟着变。 */
function selectAllPage() {
  for (const r of rows.value) tableRef.value?.toggleRowSelection(r, true);
}

function clearSelection() {
  tableRef.value?.clearSelection();
  selection.value = [];
}

function onCardTap(row: any) {
  if (batchMode.value) toggleSelect(row, !isSelected(row));
  else openDetail(row);
}

const query = ref<Record<string, any>>({ keyword: "", page: 1, page_size: 50 });
const statuses = ref<string[]>([]);

const enabledProps = ref<string[]>(initialEnabledProps());

const visibleColumns = computed(() => ASSET_COLUMNS.filter((c) => enabledProps.value.includes(c.prop)));

const activeFilterCount = computed(
  () =>
    Object.entries(query.value).filter(
      ([k, v]) => !["page", "page_size", "keyword", "sort_by", "sort_desc"].includes(k) && v !== "" && v != null && (!Array.isArray(v) || v.length),
    ).length,
);

// —— 已启用筛选条件的可移除回显 ——
// 只做「键 → 中文名」的翻译，不参与任何查询构造：清除动作就是把该键置空后重查。
const FILTER_LABELS: Record<string, string> = {
  status: "状态",
  asset_code: "资产编码",
  name: "资产名称",
  category_id: "资产类别",
  area_id: "区域",
  use_company_id: "使用公司",
  use_dept_id: "使用部门",
  user_name: "使用人",
  location: "存放地点",
  serial_no: "设备序列号",
  manager_name: "管理人",
  source: "来源",
  purchase_from: "购入起始",
  purchase_to: "购入截止",
  amount_min: "金额下限",
  amount_max: "金额上限",
};

const filterChips = computed(() =>
  Object.entries(FILTER_LABELS)
    .filter(([k]) => {
      const v = query.value[k];
      return v !== "" && v != null && v !== 0 && (!Array.isArray(v) || v.length);
    })
    .map(([k, label]) => ({
      key: k,
      label: Array.isArray(query.value[k]) ? `${label} ${query.value[k].length} 项` : label,
    })),
);

function clearFilter(key: string) {
  query.value[key] = Array.isArray(query.value[key]) ? [] : "";
  reload(1);
}

function onMore(cmd: string) {
  if (cmd === "export") exportExcel();
  else if (cmd === "columns") columnDialog.value = true;
}

async function reload(page?: number) {
  if (page) query.value.page = page;
  localStorage.setItem(COLUMN_KEY, JSON.stringify(enabledProps.value));
  loading.value = true;
  try {
    const { data } = await http.get("/assets", { params: query.value });
    rows.value = data.items || [];
    total.value = data.total;
  } finally {
    loading.value = false;
  }
}

function onSort({ prop, order }: { prop: string; order: string | null }) {
  query.value.sort_by = order ? prop : "";
  query.value.sort_desc = order === "descending";
  reload(1);
}

function openDetail(row: any) {
  detailId.value = row.id;
  detailVisible.value = true;
}

function goNew() {
  router.push({ name: "asset-new" });
}

function goEdit() {
  router.push({ name: "asset-edit", params: { id: selection.value[0].id } });
}

async function changeStatus(status: string) {
  const ids = selection.value.map((r: any) => r.id);
  await ElMessageBox.confirm(`确认把选中的 ${ids.length} 条资产状态改为「${status}」？改动会记入每条资产的履历。`, "调整状态", {
    type: "warning",
  });
  const { data } = await http.post("/assets/status", { ids, status });
  ElMessage.success(
    data.skipped ? `已更新 ${data.updated} 条，${data.skipped} 条本来就是该状态` : `已更新 ${data.updated} 条`,
  );
  reload();
}

async function removeSelected() {
  await ElMessageBox.confirm(`确认删除选中的 ${selection.value.length} 条资产？删除后可在数据库中恢复。`, "提示", {
    type: "warning",
  });
  for (const row of selection.value) {
    await http.delete(`/assets/${row.id}`);
  }
  ElMessage.success("已删除");
  reload();
}

function exportExcel() {
  return download("/assets/export", query.value);
}

function printLabels() {
  const ids = selection.value.slice(0, 200).map((r) => r.id);
  if (selection.value.length > 200) {
    ElMessage.warning("一次最多打印 200 张标签，已取前 200 条");
  }
  router.push({ name: "asset-labels", query: { ids: ids.join(",") } });
}

// 扫码/分享链接带来的 asset_code。用 watch 而不是 onMounted：已经停在 /assets 时
// 再扫一次只是改 query，组件不会重新挂载，onMounted 不会跑。
// 取不到 query 就读 main.ts 在路由抢跑前存的兜底编码（登录跳转会把 query 冲掉）。
let lastResolvedCode = "";
let stashTaken = false;

// 兜底编码只能取一次，且无论这次用没用到都要取走：否则 query 里已经带了编码时
// 它会一直留在 sessionStorage，等用户之后随手点进「资产列表」再冒出来开一个旧抽屉。
function oneShotStash(): string {
  if (stashTaken) return "";
  stashTaken = true;
  return takeAssetCode();
}

async function resolveAssetCode() {
  const stash = oneShotStash();
  const code = String(route.query.asset_code || "").trim() || stash;
  if (!code || code === lastResolvedCode) return;
  lastResolvedCode = code;
  try {
    const { data } = await http.get("/assets/by-code", { params: { code } });
    detailId.value = data.id;
    detailVisible.value = true;
  } catch {
    // 拦截器已经弹出后端的「资产编码不存在：X」，这里不再重复提示。
    // 清掉去重标记，用户重扫同一张时还能再试。
    lastResolvedCode = "";
  }
}

watch(() => route.query.asset_code, resolveAssetCode, { immediate: true });

onMounted(async () => {
  const [enums] = await Promise.all([http.get("/enums"), reload()]);
  // 「调整状态」是让用户手选状态：排除维修中/调拨中（它们由单据驱动，手填会被同步抹掉）。
  // 高级搜索里的状态筛选仍用全量枚举——「筛维修中」是有意义的查询。
  statuses.value = selectableStatuses(enums.data.statuses || []);
});
defineExpose({ reload });
</script>

<style scoped>
/* —— 页头 —— */
.page-head {
  display: flex;
  align-items: flex-start;
  gap: 16px;
  padding-bottom: 12px;
  margin-bottom: 12px;
  border-bottom: var(--hairline);
}

.ph-title {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: baseline;
  gap: 12px;
  flex-wrap: wrap;
}

.ph-title h1 {
  margin: 0;
  font-size: var(--t-20);
  font-weight: 600;
  color: var(--ink);
}

.ph-sub {
  font-size: var(--t-12);
  color: var(--steel-500);
}

.ph-sub b {
  color: var(--ink);
  font-weight: 600;
}

.ph-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 0 0 auto;
}

/* 扫码是系统的特殊功能（设备标签上贴的就是它扫的码），从「更多」下拉里提出来单独放页头。
   信号黄只做左侧描边 —— 白底 1.85:1 达不到对比度，不能用于文字或填充色。 */
.scan-btn {
  box-shadow: inset 3px 0 0 var(--signal);
}

/* —— 工具栏 —— */
.page-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}

.tb-search {
  width: 280px;
}

.tb-badge {
  margin-left: 6px;
  padding: 0 5px;
  border-radius: var(--r);
  background: var(--ink);
  color: #fff;
  font-size: var(--t-11);
  line-height: 16px;
}

/* —— 筛选条件回显 —— */
.fchip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 24px;
  padding: 0 4px 0 8px;
  background: var(--steel-100);
  border: 1px solid var(--steel-200);
  border-radius: var(--r);
  font-size: var(--t-12);
  color: var(--steel-700);
}

.fchip-x {
  cursor: pointer;
  color: var(--steel-500);
}

.fchip-x:hover {
  color: var(--danger);
}

.link {
  color: var(--el-color-primary);
  cursor: pointer;
}

/* —— 选中浮条：深色，固定在内容区底部 —— */
.selbar {
  position: fixed;
  left: 0;
  right: 0;
  bottom: calc(52px + env(safe-area-inset-bottom));
  z-index: 15;
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 6px 12px;
  background: var(--ink);
  border-top: 2px solid var(--signal);
}

.selbar-count {
  margin-right: 8px;
  font-size: var(--t-13);
  color: #c8cddb;
  white-space: nowrap;
}

.selbar-count b {
  color: #fff;
  font-family: var(--u-mono);
}

.selbar-spacer {
  flex: 1;
}

.sb-btn {
  color: #c8cddb;
}

.sb-btn:hover {
  color: var(--signal);
}

.sb-btn.is-danger:hover {
  color: #ff8a7a;
}

/* 浮条占掉的高度要让出来，否则列表最后一行永远被压住 */
.page.has-selbar {
  padding-bottom: 72px;
}

/* 桌面端侧栏 200px，浮条不要压在侧栏下面 */
@media (min-width: 768px) and (min-height: 521px) {
  .selbar {
    left: 200px;
    bottom: 0;
  }
}

/* 窄屏把搜索框提到第一行独占一行，按钮留在第二行，避免「更多」被挤成孤行 */
@media (max-width: 767px), (max-height: 520px) {
  .page-toolbar .tb-search {
    order: -1;
    width: 100%;
  }

  .page-head {
    flex-direction: column;
    align-items: stretch;
    gap: 10px;
  }

  .ph-actions {
    justify-content: flex-end;
  }
}
</style>
