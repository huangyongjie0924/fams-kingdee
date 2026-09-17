<template>
  <div class="page">
    <div class="toolbar">
      <el-button
        v-if="isMobile && auth.can('asset.manage')"
        :type="batchMode ? 'primary' : 'default'"
        @click="toggleBatch"
      >
        {{ batchMode ? "退出批量" : "批量" }}
      </el-button>
      <el-button v-if="auth.can('asset.manage')" type="primary" :icon="Plus" @click="goNew">新建</el-button>
      <el-button v-if="showManageActions" :icon="Edit" :disabled="selection.length !== 1" @click="goEdit">
        编辑
      </el-button>
      <el-dropdown v-if="showManageActions" trigger="click" @command="changeStatus">
        <el-button :icon="Switch" :disabled="!selection.length">
          调整状态<el-icon><ArrowDown /></el-icon>
        </el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item v-for="s in statuses" :key="s" :command="s">
              <el-tag :type="STATUS_TAG[s] || 'info'" size="small">{{ s }}</el-tag>
            </el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <el-button v-if="showManageActions" :icon="Delete" :disabled="!selection.length" @click="removeSelected">
        删除
      </el-button>
      <!-- 打标签跟「导出」同级：能看到台账就该能打标签，不额外加权限 -->
      <el-button v-if="selection.length" :icon="Printer" @click="printLabels">
        打印标签（{{ selection.length }}）
      </el-button>
      <el-button v-if="!isMobile" :icon="Download" @click="exportExcel">导出</el-button>
      <el-button v-if="isMobile" :icon="Camera" @click="router.push({ name: 'scan' })">扫码</el-button>
      <span v-if="!isMobile" class="spacer" />
      <el-input
        v-model="query.keyword"
        :placeholder="isMobile ? '编码/名称/使用人/地点' : '编码/名称/规格/序列号/RFID/使用人/部门/地点'"
        clearable
        :style="{ width: isMobile ? '100%' : '260px' }"
        @keyup.enter="reload(1)"
        @clear="reload(1)"
      >
        <template #prefix><el-icon><Search /></el-icon></template>
      </el-input>
      <el-button :icon="Filter" @click="searchDrawer = true">高级搜索</el-button>
      <el-button v-if="!isMobile" :icon="SetUp" @click="columnDialog = true">列配置</el-button>
    </div>

    <div class="summary-bar">
      <span>共 <b>{{ total }}</b> 条</span>
      <span>金额合计 <b>{{ money(amountTotal) }}</b></span>
      <span v-if="activeFilterCount">已启用 <b>{{ activeFilterCount }}</b> 个筛选条件</span>
      <span v-if="auth.scopeHint">{{ auth.scopeHint }}</span>
    </div>

    <el-table
      v-if="!isMobile"
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
          <el-tag v-if="col.prop === 'status'" :type="STATUS_TAG[row.status] || 'info'" size="small">
            {{ row.status }}
          </el-tag>
          <a v-else-if="col.prop === 'name'" class="link" @click="openDetail(row)">{{ row.name }}</a>
          <span v-else-if="col.money">{{ money(row[col.prop]) }}</span>
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
          <el-tag :type="STATUS_TAG[row.status] || 'info'" size="small">{{ row.status }}</el-tag>
        </div>
        <div class="m-grid">
          <span class="k">资产编码</span><span>{{ row.asset_code || "-" }}</span>
          <span class="k">使用部门</span><span>{{ row.use_dept_name || "-" }}</span>
          <span class="k">使用人</span><span>{{ row.user_emp_name || "-" }}</span>
          <span class="k">存放地点</span><span>{{ row.location || "-" }}</span>
          <span class="k">金额</span><span>{{ money(row.amount) }}</span>
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
} from "@element-plus/icons-vue";
import http, { download } from "../../api/client";
import { ASSET_COLUMNS, STATUS_TAG, money } from "../../api/meta";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";
import { takeAssetCode } from "../../utils/deeplink";
import SearchDrawer from "./SearchDrawer.vue";
import ColumnDialog from "./ColumnDialog.vue";
import CardDetail from "./CardDetail.vue";

const route = useRoute();
const router = useRouter();
const COLUMN_KEY = "asset_list_columns";
const sortableProps = new Set(["asset_code", "name", "amount", "purchase_date", "status"]);

const isMobile = useIsMobile();
const batchMode = ref(false);
const loading = ref(false);
const rows = ref<any[]>([]);
const total = ref(0);
const amountTotal = ref(0);
const selection = ref<any[]>([]);
const searchDrawer = ref(false);
const columnDialog = ref(false);
const detailVisible = ref(false);
const detailId = ref(0);

// 窄屏表格换卡片后没有多选列，选中只能靠「批量」模式勾卡片，管理类按钮平时收起来腾地方
const showManageActions = computed(
  () => auth.can("asset.manage") && (!isMobile.value || batchMode.value),
);

watch(batchMode, (on) => {
  if (!on) selection.value = [];
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

function onCardTap(row: any) {
  if (batchMode.value) toggleSelect(row, !isSelected(row));
  else openDetail(row);
}

const query = ref<Record<string, any>>({ keyword: "", page: 1, page_size: 50 });
const statuses = ref<string[]>([]);

const enabledProps = ref<string[]>(
  JSON.parse(localStorage.getItem(COLUMN_KEY) || "null") ||
    ASSET_COLUMNS.filter((c) => c.defaultOn).map((c) => c.prop),
);

const visibleColumns = computed(() => ASSET_COLUMNS.filter((c) => enabledProps.value.includes(c.prop)));

const activeFilterCount = computed(
  () =>
    Object.entries(query.value).filter(
      ([k, v]) => !["page", "page_size", "keyword", "sort_by", "sort_desc"].includes(k) && v !== "" && v != null && (!Array.isArray(v) || v.length),
    ).length,
);

async function reload(page?: number) {
  if (page) query.value.page = page;
  localStorage.setItem(COLUMN_KEY, JSON.stringify(enabledProps.value));
  loading.value = true;
  try {
    const { data } = await http.get("/assets", { params: query.value });
    rows.value = data.items || [];
    total.value = data.total;
    amountTotal.value = data.amount_total;
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
  statuses.value = enums.data.statuses || [];
});
defineExpose({ reload });
</script>

<style scoped>
.link {
  color: var(--el-color-primary);
  cursor: pointer;
}

/* 窄屏把搜索框提到第一行独占一行，按钮留在第二行，避免「高级搜索」被挤成孤行 */
@media (max-width: 767px), (max-height: 520px) {
  .toolbar .el-input {
    order: -1;
  }
}
</style>
