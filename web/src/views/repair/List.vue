<template>
  <div class="page">
    <el-card>
      <div class="toolbar">
        <span class="title">{{ pageTitle }}</span>
        <!-- 分段切换：把原先三个侧栏入口收成「一张页、两段视图」（方案 §3.2）。
             ⚠️ 后端 /api/repairs 只认 mine=1，且语义随角色变（维护人员=派给我的，
             其余=我提交的，见 api/repair.go:177），没有区分「指派给我」与「我提交的」
             的独立参数，所以这里只做两段 —— 不臆造后端不支持的筛选。 -->
        <el-radio-group v-model="seg" class="seg" size="small">
          <el-radio-button value="all">全部</el-radio-button>
          <el-radio-button value="mine">{{ mineLabel }}</el-radio-button>
        </el-radio-group>
        <span class="spacer" />
        <!-- 批量删除：**仅超级管理员**可见（后端 POST /api/repairs/delete 挂 requireAdmin）。
             只读会话（云之家深链免登）也隐藏 —— 那种会话后端一律拒 POST，留着按钮只会变成
             「点得下去但 403」。 -->
        <el-button
          v-if="canDelete"
          type="danger"
          plain
          :icon="Delete"
          :disabled="!selection.length"
          @click="removeSelected"
        >
          删除所选{{ selection.length ? `（${selection.length}）` : "" }}
        </el-button>
        <el-button :icon="Refresh" @click="load">刷新</el-button>
      </div>

      <div class="filters">
        <el-select
          v-model="query.status"
          multiple
          collapse-tags
          clearable
          placeholder="状态"
          style="width: 220px"
          @change="reload(1)"
        >
          <el-option v-for="s in statusOptions" :key="s.value" :label="s.label" :value="s.value" />
        </el-select>
        <el-select
          v-if="!mine"
          v-model="query.use_dept_id"
          clearable
          filterable
          placeholder="使用部门"
          style="width: 180px"
          @change="reload(1)"
        >
          <el-option v-for="d in departments" :key="d.id" :label="d.name" :value="d.id" />
        </el-select>
        <el-input
          v-model="query.asset_code"
          placeholder="资产编码"
          clearable
          style="width: 180px"
          @keyup.enter="reload(1)"
          @clear="reload(1)"
        />
        <el-date-picker
          v-model="dateRange"
          type="daterange"
          value-format="YYYY-MM-DD"
          start-placeholder="开始日期"
          end-placeholder="结束日期"
          style="width: 240px"
          @change="reload(1)"
        />
      </div>

      <el-table
        v-if="!isMobile"
        ref="tableRef"
        :data="rows"
        border
        v-loading="loading"
        @row-click="go"
        @selection-change="onSelectionChange"
        class="clickable"
      >
        <!-- 只有「待受理 / 已撤单」的行可勾选：可勾选集合 == 可删除集合，用户不可能选到一张
             点了会被后端拒绝的单。禁用而不是隐藏，是为了让「为什么这张不能删」看得见。 -->
        <el-table-column v-if="canDelete" type="selection" width="44" :selectable="isDeletableRow" />
        <el-table-column prop="code" label="单号" width="160">
          <template #default="{ row }"><span class="u-mono">{{ row.code }}</span></template>
        </el-table-column>
        <el-table-column label="资产" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">{{ row.asset_name }}（{{ row.asset_code }}）</template>
        </el-table-column>
        <el-table-column prop="fault_desc" label="问题描述" min-width="160" show-overflow-tooltip />
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <StatusChip kind="repair" :value="row.status" :label="row.status_label" />
          </template>
        </el-table-column>
        <el-table-column label="报修人" width="100">
          <template #default="{ row }">{{ row.reporter_name || "-" }}</template>
        </el-table-column>
        <el-table-column label="处理人" width="120">
          <template #default="{ row }">{{ handlerText(row) }}</template>
        </el-table-column>
        <el-table-column label="使用部门" width="140">
          <template #default="{ row }">{{ row.use_dept_name || "-" }}</template>
        </el-table-column>
        <el-table-column label="提交时间" width="170">
          <template #default="{ row }"><span class="u-mono">{{ formatTime(row.created_at) }}</span></template>
        </el-table-column>
      </el-table>

      <div v-else v-loading="loading" class="m-list">
        <div v-for="row in rows" :key="row.id" class="m-card" @click="go(row)">
          <div class="m-card-hd">
            <!-- 窄屏没有表格勾选列，用卡片上的复选框承载同一套 selection 状态 -->
            <el-checkbox
              v-if="canDelete"
              :model-value="isSelected(row)"
              :disabled="!isDeletableRow(row)"
              @click.stop
              @change="toggleSelect(row, $event)"
            />
            <span class="m-title">{{ row.asset_name || row.asset_code }}</span>
            <StatusChip kind="repair" :value="row.status" :label="row.status_label" />
          </div>
          <div class="m-grid">
            <span class="k">单号</span><span class="u-mono">{{ row.code }}</span>
            <span class="k">问题</span><span>{{ row.fault_desc }}</span>
            <span class="k">报修人</span><span>{{ row.reporter_name || "-" }}</span>
            <span class="k">处理人</span><span>{{ handlerText(row) }}</span>
            <span class="k">提交时间</span><span class="u-mono">{{ formatTime(row.created_at) }}</span>
          </div>
        </div>
        <el-empty v-if="!rows.length && !loading" description="暂无维修单" />
      </div>

      <div class="pager">
        <el-pagination
          :layout="isMobile ? 'prev, pager, next' : 'total, sizes, prev, pager, next'"
          :total="total"
          :current-page="page"
          :page-size="pageSize"
          :page-sizes="[20, 50, 100]"
          :pager-count="isMobile ? 5 : 7"
          @current-change="onPage"
          @size-change="onSize"
        />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import { Delete, Refresh } from "@element-plus/icons-vue";
import http from "../../api/client";
import { REPAIR_STATUS_LABELS } from "../../api/meta";
import { useIsMobile } from "../../composables/useIsMobile";
import { repairEntry } from "../../composables/useRepairEntry";
import { auth } from "../../stores/auth";
import StatusChip from "../../components/StatusChip.vue";

const route = useRoute();
const router = useRouter();
const isMobile = useIsMobile();

const mine = computed(() => route.query.mine === "1");

// 页面标题沿用侧栏那一项的文案，用户点进来能对上（维修工单 / 待我处理 / 我的报修单）
const pageTitle = computed(() => repairEntry.value.label);

// mine=1 的语义由后端按角色决定，所以第二段的文案也得按角色写：
// 维护人员看到的是「派给我的」，其余角色看到的是「我提交的」。
const mineLabel = computed(() => (auth.can("repair.handle") ? "待我处理" : "我提交的"));

// 分段控件的值直接映射到 URL 上的 mine 参数 —— URL 是唯一真源，
// 刷新/后退/分享链接都不会丢视图。其余筛选条件原样保留在 query 里。
const seg = computed<string>({
  get: () => (mine.value ? "mine" : "all"),
  set: (v) => {
    const q: Record<string, any> = { ...route.query };
    if (v === "mine") q.mine = "1";
    else delete q.mine;
    router.replace({ path: "/repairs", query: q });
  },
});

const rows = ref<any[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(20);
const loading = ref(false);
const departments = ref<any[]>([]);
const dateRange = ref<string[]>([]);

// —— 批量删除（仅 admin）——
//
// 前端只负责「谁能看到入口」和「哪些行可勾选」；真正的判定在后端（requireAdmin +
// store 层的状态白名单 model.RepairDeletableStatuses）。前端的禁用只是提前把话说清楚，
// 不是安全边界。
const tableRef = ref<any>(null);
const selection = ref<any[]>([]);
const canDelete = computed(() => auth.isAdmin && !auth.readonly);

/**
 * 可删状态白名单：**待受理 + 已撤单**。
 *
 * ⚠️ 必须与后端 `model.RepairDeletableStatuses` 逐项一致（同 `auth.ts` 与
 * `model.rolePermissions` 的约定）。不一致的后果是「勾得下去但提交 400」，
 * 或者「后端允许但界面没入口」。
 *
 * 为什么是这两个：判定标准是「这张单是否还挂着未兑现的外部承诺」。
 * pending 从未被受理；cancelled 已是终态、撤单时已清理过云之家待办。
 * accepted / dispatched 在云之家侧还挂着他人的在办待办，删除会让待办指向 404。
 */
const DELETABLE_STATUSES = ["pending", "cancelled"];

function isDeletableRow(row: any) {
  return DELETABLE_STATUSES.includes(row?.status);
}

function isSelected(row: any) {
  return selection.value.some((r) => r.id === row.id);
}

function toggleSelect(row: any, checked: boolean) {
  selection.value = checked
    ? [...selection.value.filter((r) => r.id !== row.id), row]
    : selection.value.filter((r) => r.id !== row.id);
}

// el-table 的勾选状态由它自己维护，翻页/重新加载后它不会自动同步我们的数组，
// 所以每次换数据都清一次，避免「界面上没勾、计数却还在」。
function onSelectionChange(rows: any[]) {
  selection.value = rows;
}

function clearSelection() {
  tableRef.value?.clearSelection();
  selection.value = [];
}

async function removeSelected() {
  const rows = selection.value.filter(isDeletableRow);
  if (!rows.length) return;
  const codes = rows.map((r) => r.code);
  // 列出单号而不是只说数量：删除不可恢复，让人在点确定前能核对自己选的是哪几张。
  const shown = codes.slice(0, 10).join("、");
  const more = codes.length > 10 ? ` 等 ${codes.length} 张` : "";
  // 白名单现在有两种状态，把构成说清楚：管理员点确定前应该知道删的是哪几类单。
  const breakdown = DELETABLE_STATUSES.map((s) => ({
    label: REPAIR_STATUS_LABELS[s] ?? s,
    n: rows.filter((r) => r.status === s).length,
  }))
    .filter((x) => x.n > 0)
    .map((x) => `${x.label} ${x.n} 张`)
    .join("、");
  await ElMessageBox.confirm(
    `确认删除以下 ${codes.length} 张维修单（${breakdown}）？\n${shown}${more}\n\n删除不可恢复：单号、附件、状态流转记录与云之家待办会一并清除。`,
    "删除维修单",
    {
      type: "warning",
      confirmButtonText: "确认删除",
      confirmButtonClass: "el-button--danger",
      // 消息里有换行，靠 customClass 上的 white-space: pre-line 保留（见 style.css）
      customClass: "repair-delete-confirm",
    },
  );
  const { data } = await http.post("/repairs/delete", { ids: rows.map((r) => r.id) });
  ElMessage.success(`已删除 ${data.deleted} 张维修单`);
  clearSelection();
  await load();
}

// 从 URL 查询参数初始化筛选：首页「待我处理」的 link 形如 /repairs?status=pending，
// 需要「只看我的」时附 &mine=1。status 是逗号分隔的状态码串（如 "accepted,assigned"），
// 转成 string[] 赋给 query.status；load() 里再 join(",") 回传给后端。mine 由 route.query.mine 响应式处理。
function parseStatusQuery(): string[] {
  const raw = route.query.status;
  const s = Array.isArray(raw) ? raw.join(",") : raw;
  return String(s ?? "")
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);
}

const query = ref<Record<string, any>>({
  status: parseStatusQuery(),
  use_dept_id: 0,
  asset_code: "",
});

// 状态下拉来自后端枚举，标签用中文白话（业务可预期，不暴露内部码）
const statusOptions = computed(() =>
  Object.entries(REPAIR_STATUS_LABELS).map(([value, label]) => ({ value, label })),
);

function handlerText(row: any): string {
  if (row.assignee_name) return row.assignee_name;
  if (row.vendor_name) return row.vendor_name;
  return "待指派";
}

function formatTime(t: string) {
  return (t || "").replace("T", " ").slice(0, 19);
}

async function load() {
  loading.value = true;
  try {
    const params: Record<string, any> = {
      page: page.value,
      page_size: pageSize.value,
      status: query.value.status.join(","),
      use_dept_id: query.value.use_dept_id || 0,
      asset_code: query.value.asset_code || "",
    };
    if (mine.value) params.mine = 1;
    if (dateRange.value && dateRange.value.length === 2) {
      params.from = dateRange.value[0];
      params.to = dateRange.value[1];
    }
    const { data } = await http.get("/repairs", { params });
    rows.value = data.items || [];
    total.value = data.total || 0;
    // 换了一批行对象，旧的选中项已不在表里；不清会让「已选 N」的计数与新数据对不上。
    selection.value = [];
  } finally {
    loading.value = false;
  }
}

function reload(p?: number) {
  if (p) page.value = p;
  load();
}

function onPage(p: number) {
  page.value = p;
  load();
}

function onSize(s: number) {
  pageSize.value = s;
  page.value = 1;
  load();
}

function go(row: any) {
  router.push({ name: "repair-detail", params: { id: row.id } });
}

onMounted(async () => {
  if (!mine.value) {
    const { data } = await http.get("/departments");
    departments.value = data || [];
  }
  await load();
});

// 切段即换视图，回到第 1 页重新查（否则会停在上一次的页码上，出现空页）
watch(mine, () => {
  page.value = 1;
  load();
});
</script>

<style scoped>
.title {
  font-size: var(--t-20);
  font-weight: 600;
  color: var(--ink);
}

/* 分段控件：硬边 + 2px 圆角，与全站工业语汇一致 */
.seg :deep(.el-radio-button__inner) {
  border-color: var(--steel-200);
  color: var(--steel-700);
  font-weight: 500;
}

.seg :deep(.el-radio-button__original-radio:checked + .el-radio-button__inner) {
  background: var(--ink);
  border-color: var(--ink);
  color: #fff;
  box-shadow: none;
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}

.spacer {
  flex: 1;
}

.filters {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 12px;
}

.clickable :deep(.el-table__row) {
  cursor: pointer;
}

.pager {
  margin-top: 12px;
  display: flex;
  justify-content: flex-end;
}
</style>
