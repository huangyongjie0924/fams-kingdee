<template>
  <div class="page">
    <el-card class="no-print">
      <div class="head">
        <div>
          <span class="title">{{ plan?.name }}</span>
          <el-tag v-if="plan" :type="statusType(plan.status)" size="small" class="ml">{{ statusLabel(plan.status) }}</el-tag>
          <span class="code">{{ plan?.code }}</span>
        </div>
        <span class="spacer" />
        <el-button v-if="canManage && plan?.status === 'counting'" type="success" @click="finish">完成盘点</el-button>
        <el-button
          v-if="canManage && (plan?.status === 'draft' || plan?.status === 'counting')"
          type="warning"
          plain
          @click="cancelPlan"
        >
          取消计划
        </el-button>
        <el-button @click="back">返回</el-button>
      </div>
    </el-card>

    <el-tabs v-model="tab" class="tabs">
      <el-tab-pane label="盘点明细" name="items">
        <el-card class="no-print">
          <div class="toolbar">
            <template v-if="canManage && plan?.status === 'counting'">
              <span class="label">指派给</span>
              <el-select v-model="assigneeId" filterable clearable placeholder="选择员工" style="width: 220px">
                <el-option v-for="e in employees" :key="e.id" :label="empLabel(e)" :value="e.id" />
              </el-select>
              <el-button type="primary" :disabled="!selected.length || !assigneeId" @click="assign">
                指派选中（{{ selected.length }}）
              </el-button>
            </template>
            <el-input v-model="keyword" placeholder="资产编码/名称" clearable style="width: 200px"
              @keyup.enter="loadItems" @clear="loadItems" />
            <el-select v-model="resultFilter" clearable placeholder="盘点结果" style="width: 140px" @change="loadItems">
              <el-option label="未盘点" value="__empty__" />
              <el-option v-for="r in countResults" :key="r" :label="r" :value="r" />
            </el-select>
            <span v-if="!isMobile" class="spacer" />
            <el-button
              v-if="!isMobile && canEnter && plan?.status === 'counting'"
              type="primary"
              :disabled="!pending.size"
              @click="saveResults"
            >
              保存录入（{{ pending.size }}）
            </el-button>
          </div>

          <el-alert
            v-if="focusMissed"
            class="focus-miss"
            type="warning"
            :closable="false"
            show-icon
            title="该盘点明细不在本计划或未指派给你"
          />

          <el-table
            v-if="!isMobile"
            :data="items"
            border
            v-loading="loading"
            row-key="id"
            :row-class-name="rowClass"
            @selection-change="onSelect"
            height="560"
          >
            <el-table-column type="selection" width="46" />
            <el-table-column prop="asset_code" label="资产编码" width="150" />
            <el-table-column prop="name" label="资产名称" min-width="180" show-overflow-tooltip />
            <el-table-column prop="category_name" label="分类" width="120" />
            <el-table-column prop="use_dept_name" label="使用部门" width="130" />
            <el-table-column prop="user_name" label="使用人" width="100" />
            <el-table-column prop="location" label="存放位置" width="150" show-overflow-tooltip />
            <el-table-column label="账面金额" width="120" align="right">
              <template #default="{ row }">{{ money(row.book_amount) }}</template>
            </el-table-column>
            <el-table-column prop="assignee_name" label="盘点人" width="110">
              <template #default="{ row }">
                <span v-if="row.assignee_name">{{ row.assignee_name }}</span>
                <span v-else class="muted">未指派</span>
              </template>
            </el-table-column>
            <el-table-column label="盘点结果" width="140">
              <template #default="{ row }">
                <el-select v-model="row.result" size="small" placeholder="未盘点" clearable
                  :disabled="!canEnter || plan?.status !== 'counting'" @change="markDirty(row)">
                  <el-option v-for="r in countResults" :key="r" :label="r" :value="r" />
                </el-select>
              </template>
            </el-table-column>
            <el-table-column label="备注" width="180">
              <template #default="{ row }">
                <el-input v-model="row.note" size="small" :disabled="!canEnter || plan?.status !== 'counting'"
                  @input="markDirty(row)" />
              </template>
            </el-table-column>
            <el-table-column prop="counted_by" label="录入人" width="100" />
          </el-table>

          <!-- 窄屏：结果与备注在表格里是嵌在 12 列中的窄控件，手指点不准，改成整行全宽 -->
          <div v-else v-loading="loading" class="m-list">
            <div
              v-for="row in visibleItems"
              :key="row.id"
              class="m-card"
              :class="{ 'focus-row': row.id === activeFocusId }"
            >
              <div class="m-card-hd">
                <el-checkbox
                  v-if="canManage && plan?.status === 'counting'"
                  :model-value="isRowSelected(row)"
                  @change="toggleRowSelect(row, $event)"
                />
                <span class="m-title">{{ row.name }}</span>
                <el-tag v-if="row.result" :type="row.result === '在库' ? 'success' : 'warning'" size="small">
                  {{ row.result }}
                </el-tag>
              </div>
              <div class="m-grid">
                <span class="k">资产编码</span><span>{{ row.asset_code || "-" }}</span>
                <span class="k">使用部门</span><span>{{ row.use_dept_name || "-" }}</span>
                <span class="k">使用人</span><span>{{ row.user_name || "-" }}</span>
                <span class="k">存放位置</span><span>{{ row.location || "-" }}</span>
                <span class="k">盘点人</span><span>{{ row.assignee_name || "未指派" }}</span>
              </div>
              <div class="m-field">
                <span class="k">盘点结果</span>
                <el-select v-model="row.result" placeholder="未盘点" clearable
                  :disabled="!canEnter || plan?.status !== 'counting'" @change="markDirty(row)">
                  <el-option v-for="r in countResults" :key="r" :label="r" :value="r" />
                </el-select>
              </div>
              <div class="m-field">
                <span class="k">备注</span>
                <el-input v-model="row.note" :disabled="!canEnter || plan?.status !== 'counting'"
                  @input="markDirty(row)" @focus="onNoteFocus" />
              </div>
            </div>

            <div v-if="visibleCount < items.length" class="m-more">
              <el-button @click="visibleCount += PAGE_SIZE">
                加载更多（还有 {{ items.length - visibleCount }} 条）
              </el-button>
            </div>
            <el-empty v-if="!items.length && !loading" description="暂无盘点明细" />
          </div>
        </el-card>
      </el-tab-pane>


      <el-tab-pane label="盘点报表" name="report">
        <el-card class="no-print">
          <div class="toolbar">
            <span class="hint">报表只反映盘点结果，不会写回资产台账。</span>
            <span class="spacer" />
            <el-button v-if="!isMobile" type="primary" @click="print">打印报表</el-button>
            <span v-else class="hint">手机端不支持打印，请用电脑打开打印。</span>
          </div>
        </el-card>

        <div class="report">
          <div class="report-head">
            <h2>固定资产盘点报表</h2>
            <div class="meta">
              <span>计划编号：{{ plan?.code || "-" }}</span>
              <span>计划名称：{{ plan?.name || "-" }}</span>
              <span>状态：{{ plan ? statusLabel(plan.status) : "-" }}</span>
              <span>制表人：{{ plan?.created_by || "-" }}</span>
              <span>打印时间：{{ now }}</span>
            </div>
          </div>

          <div class="summary">
            <div class="cell"><span class="k">盘点总数</span><span class="v">{{ summary.total }}</span></div>
            <div class="cell"><span class="k">已盘点</span><span class="v">{{ summary.counted }}</span></div>
            <div class="cell"><span class="k">未盘点</span><span class="v">{{ summary.uncounted }}</span></div>
            <div class="cell"><span class="k">在库</span><span class="v">{{ summary.normal }}</span></div>
            <div class="cell"><span class="k">盘亏</span><span class="v danger">{{ summary.loss }}</span></div>
            <div class="cell"><span class="k">盘盈</span><span class="v warn">{{ summary.gain }}</span></div>
            <div class="cell"><span class="k">损毁</span><span class="v warn">{{ summary.damaged }}</span></div>
          </div>

          <!-- 报表是真表格、要给打印用，窄屏只做横向滚动，不改成卡片 -->
          <div class="report-scroll">
          <table class="report-table">
            <thead>
              <tr>
                <th style="width: 44px">序号</th>
                <th>资产编码</th>
                <th>资产名称</th>
                <th>分类</th>
                <th>使用部门</th>
                <th>使用人</th>
                <th>存放位置</th>
                <th style="width: 90px">账面金额</th>
                <th style="width: 80px">盘点结果</th>
                <th>备注</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(it, i) in reportItems" :key="it.id" :class="{ abnormal: it.result && it.result !== '在库' }">
                <td class="c">{{ i + 1 }}</td>
                <td>{{ it.asset_code }}</td>
                <td>{{ it.name }}</td>
                <td>{{ it.category_name }}</td>
                <td>{{ it.use_dept_name }}</td>
                <td>{{ it.user_name }}</td>
                <td>{{ it.location }}</td>
                <td class="r">{{ money(it.book_amount) }}</td>
                <td class="c">{{ it.result || "未盘点" }}</td>
                <td>{{ it.note }}</td>
              </tr>
              <tr v-if="!reportItems.length">
                <td colspan="10" class="c muted">暂无盘点明细</td>
              </tr>
            </tbody>
          </table>
          </div>
        </div>
      </el-tab-pane>
    </el-tabs>

    <!-- 窄屏明细一屏放不下，保存按钮跟着滚动会点不到，吸在底部 -->
    <div v-if="isMobile && tab === 'items' && canEnter && plan?.status === 'counting'" class="m-savebar">
      <el-button type="primary" :disabled="!pending.size" @click="saveResults">
        保存录入（{{ pending.size }}）
      </el-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import http from "../../api/client";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";
import { A4_LANDSCAPE, usePrintPage } from "../../utils/printPage";

const route = useRoute();
const router = useRouter();
const planId = Number(route.params.id);

const isMobile = useIsMobile();
const plan = ref<any>(null);
const rawItems = ref<any[]>([]);
const report = ref<any>({ plan: null, summary: {}, items: [] });
const employees = ref<any[]>([]);
const countResults = ref<string[]>([]);
const loading = ref(false);
const tab = ref("items");
const selected = ref<any[]>([]);
const assigneeId = ref<number | null>(null);
// 未保存的录入按 item_id 单独存，不挂在取回来的行上：重新取数（搜索/筛选/指派）
// 会整体替换 rawItems，挂行上就跟着没了；提交成功才清空。
const pending = ref<Map<number, { result: string; note: string }>>(new Map());
const keyword = ref("");
const resultFilter = ref("");
const now = ref("");

// 明细一次全量加载且无分页，窄屏卡片 DOM 比表格行重，前端切片先渲染一屏
const PAGE_SIZE = 50;
const visibleCount = ref(PAGE_SIZE);

// 从扫码详情页「立即盘这一条」跳进来时带的目标明细
const focusItemId = ref(Number(route.query.item || 0) || 0);
const activeFocusId = ref(0);
const itemsLoaded = ref(false);
let focusTimer = 0;

const canManage = computed(() => auth.can("count.manage"));
const canEnter = computed(() => auth.can("count.enter"));

// 「未盘点」后端没有这个筛选值（空串等于不筛），在明细列表里本地过滤
const items = computed(() =>
  resultFilter.value === "__empty__" ? rawItems.value.filter((it) => !it.result) : rawItems.value,
);
const visibleItems = computed(() => items.value.slice(0, visibleCount.value));
const summary = computed(() => report.value.summary || {});
const reportItems = computed(() => report.value.items || []);

// 只有真正重新取数（换关键词/筛选/提交后）才把「加载更多」的进度收回去；
// 靠 items 会在结果变化时把列表折回 50 条。
// 定位目标行的抬升必须并进这个 watcher：另写一处会与这里的重置互相覆盖（微任务竞态），
// 结果就是目标行被折回前 50 条之外，滚动时找不到。
watch(rawItems, () => {
  const idx = focusItemId.value ? rawItems.value.findIndex((r) => r.id === focusItemId.value) : -1;
  visibleCount.value = idx >= 0 ? Math.max(PAGE_SIZE, idx + 1) : PAGE_SIZE;
  if (idx >= 0) highlight();
});

// 目标行不在本计划、或（盘点员视角）没指派给自己时，rawItems 里根本没有它。
// 必须等首次取数回来再判，否则挂载瞬间 rawItems 还是空的，会先闪一条「找不到」。
const focusMissed = computed(
  () => !!focusItemId.value && itemsLoaded.value && !rawItems.value.some((r) => r.id === focusItemId.value),
);

function highlight() {
  activeFocusId.value = focusItemId.value;
  window.clearTimeout(focusTimer);
  focusTimer = window.setTimeout(() => (activeFocusId.value = 0), 3000);
  // 等切片渲染出来再滚，否则目标行还在 DOM 之外
  nextTick(() => {
    document.querySelector(".focus-row")?.scrollIntoView({
      block: "center",
      behavior: "smooth",
    });
  });
}

function rowClass({ row }: { row: any }) {
  return row.id === activeFocusId.value ? "focus-row" : "";
}

const statusLabel = (v: string) =>
  ({ draft: "草稿", counting: "盘点中", done: "已完成", cancelled: "已取消" } as Record<string, string>)[v] ?? v;

const statusType = (v: string) =>
  ({ draft: "info", counting: "warning", done: "success", cancelled: "info" } as Record<string, string>)[v] ?? "info";

function empLabel(e: any) {
  return e.dept_name ? `${e.name}（${e.dept_name}）` : e.name;
}

function money(v: any) {
  const n = Number(v || 0);
  return n.toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

async function loadPlan() {
  const { data } = await http.get(`/count/plans/${planId}`);
  plan.value = data;
}

async function loadItems() {
  loading.value = true;
  try {
    const params: any = {};
    if (keyword.value) params.keyword = keyword.value;
    if (resultFilter.value && resultFilter.value !== "__empty__") params.result = resultFilter.value;
    const { data } = await http.get(`/count/plans/${planId}/items`, { params });
    // 未保存的录入盖回取回来的行上；这次查询没带出来的行也留着，等它再出现时接着生效
    rawItems.value = (data.items || []).map((it: any) => {
      const p = pending.value.get(it.id);
      return p ? { ...it, result: p.result, note: p.note } : it;
    });
    itemsLoaded.value = true;
  } finally {
    loading.value = false;
  }
  const { data } = await http.get(`/count/plans/${planId}/report`);
  report.value = data;
}

function onSelect(rows: any[]) {
  selected.value = rows;
}

function markDirty(row: any) {
  const next = new Map(pending.value);
  next.set(row.id, { result: row.result || "", note: row.note || "" });
  pending.value = next;
}

// 窄屏卡片没有多选列，勾选状态与表格共用同一个 selected 数组
function isRowSelected(row: any) {
  return selected.value.some((r) => r.id === row.id);
}

function toggleRowSelect(row: any, checked: boolean) {
  selected.value = checked
    ? [...selected.value.filter((r) => r.id !== row.id), row]
    : selected.value.filter((r) => r.id !== row.id);
}

async function assign() {
  // 指派成功后会 loadItems()，未保存的录入先落库，别让用户白填
  if (pending.value.size) await saveResults();
  await http.post(`/count/plans/${planId}/assign`, {
    item_ids: selected.value.map((r) => r.id),
    assignee_id: assigneeId.value,
  });
  ElMessage.success("已指派");
  await loadItems();
}

async function saveResults() {
  // 直接提交 pending：它不受当前筛选影响，被「未盘点」筛掉的行也在里面
  const items = [...pending.value].map(([item_id, v]) => ({ item_id, result: v.result, note: v.note }));
  if (!items.length) return;
  const { data } = await http.post(`/count/plans/${planId}/submit`, { items });
  pending.value = new Map();
  ElMessage.success(`已保存 ${data.updated} 条`);
  await loadItems();
}

// 安卓软键盘 adjustPan 会把聚焦的输入框顶到屏幕外，主动滚回可视区中部
function onNoteFocus(e: FocusEvent) {
  const el = e.target as HTMLElement | null;
  setTimeout(() => el?.scrollIntoView({ block: "center", behavior: "smooth" }), 300);
}

async function finish() {
  await ElMessageBox.confirm("确认完成盘点？完成后不能再录入结果。", "提示", { type: "warning" });
  await http.post(`/count/plans/${planId}/finish`);
  ElMessage.success("已完成");
  await loadPlan();
}

async function cancelPlan() {
  await ElMessageBox.confirm("确认取消该盘点计划？", "提示", { type: "warning" });
  await http.post(`/count/plans/${planId}/cancel`);
  ElMessage.success("已取消");
  await loadPlan();
}

function print() {
  now.value = new Date().toLocaleString("zh-CN");
  window.print();
}

function back() {
  router.push({ name: "count" });
}

onMounted(async () => {
  document.body.classList.add("printing-count");
  // 深链进来先清筛选：「未盘点」用的是本地过滤，会把已经录过结果的行走滤掉，
  // 目标行根本不在 items 里，后面就定位不到。清完再取数。
  if (focusItemId.value) {
    keyword.value = "";
    resultFilter.value = "";
  }
  const [, , e, en] = await Promise.all([
    loadPlan(),
    loadItems(),
    http.get("/employees"),
    http.get("/enums"),
  ]);
  employees.value = e.data || [];
  countResults.value = en.data.count_results || [];
});

onBeforeUnmount(() => {
  document.body.classList.remove("printing-count");
  window.clearTimeout(focusTimer);
});

// 盘点报表固定 A4 横向 10mm 边距；标签页是纵向无边距，两边靠这个动态注入避免互相覆盖
usePrintPage(A4_LANDSCAPE);
</script>

<style scoped>
.page {
  padding: 12px;
}

.head {
  display: flex;
  align-items: center;
  gap: 8px;
  /* 窄屏放不下标题 + 三个按钮，不换行会把「返回」挤出屏幕 */
  flex-wrap: wrap;
}

.title {
  font-size: 16px;
  font-weight: 600;
}

.focus-miss {
  margin-bottom: 8px;
}

/* el-table 的行需要 :deep 才够得到；.m-card 是本组件的元素，直接写即可 */
:deep(.el-table .focus-row > td) {
  background: #fdf6ec !important;
  transition: background 0.3s;
}

.m-card.focus-row {
  border-color: #e6a23c;
  box-shadow: 0 0 0 2px rgba(230, 162, 60, 0.35);
  transition: box-shadow 0.3s;
}

.ml {
  margin-left: 6px;
}

.code {
  margin-left: 10px;
  color: #909399;
  font-size: 13px;
}

.spacer {
  flex: 1;
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}

.label {
  color: #606266;
  font-size: 13px;
}

.hint {
  color: #909399;
  font-size: 13px;
}

.muted {
  color: #c0c4cc;
}

.report {
  background: #fff;
  padding: 16px;
}

.report-head h2 {
  margin: 0 0 8px;
  text-align: center;
  font-size: 20px;
}

.report-head .meta {
  display: flex;
  flex-wrap: wrap;
  gap: 18px;
  font-size: 13px;
  color: #606266;
  margin-bottom: 12px;
}

.summary {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 12px;
}

.summary .cell {
  border: 1px solid #dcdfe6;
  border-radius: 4px;
  padding: 6px 14px;
  display: flex;
  align-items: center;
  gap: 8px;
}

.summary .k {
  color: #909399;
  font-size: 13px;
}

.summary .v {
  font-weight: 600;
  font-size: 15px;
}

.summary .v.danger {
  color: #f56c6c;
}

.summary .v.warn {
  color: #e6a23c;
}

.report-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.report-table th,
.report-table td {
  border: 1px solid #999;
  padding: 4px 6px;
  text-align: left;
}

.report-table th {
  background: #f0f2f5;
  font-weight: 600;
}

.report-table td.c {
  text-align: center;
}

.report-table td.r {
  text-align: right;
}

.report-table tr.abnormal td {
  background: #fef0f0;
  color: #c45656;
}

.m-savebar {
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 20;
  padding: 8px 12px calc(8px + env(safe-area-inset-bottom));
  background: #fff;
  border-top: 1px solid #e4e7ed;
}

.m-savebar .el-button {
  width: 100%;
}

@media (max-width: 767px), (max-height: 520px) {
  /* 指派/搜索那几行控件都写死了内联 width，只能 !important 压掉换成整行 */
  .toolbar > .el-select,
  .toolbar > .el-input {
    width: 100% !important;
  }

  .toolbar > .label {
    width: 100%;
  }

  /* 给吸底保存条让位，否则最后一条卡片会被盖住 */
  .page {
    padding-bottom: 76px;
  }

  .report-scroll {
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
  }
}
</style>

<style>
@media print {
  /* .aside/.header 是 Main.vue 的 scoped 类名，.el-aside/.el-header 是 Element Plus
     的固定类；两者都写上，重构布局时打印规则不会静默失效。 */
  body.printing-count .aside,
  body.printing-count .header,
  body.printing-count .el-aside,
  body.printing-count .el-header,
  body.printing-count .el-overlay,
  body.printing-count .el-drawer,
  body.printing-count .no-print,
  body.printing-count .el-tabs__header {
    display: none !important;
  }

  body.printing-count .el-main {
    overflow: visible !important;
    padding: 0 !important;
  }

  body.printing-count .report {
    position: absolute;
    inset: 0;
    padding: 0;
    /* 卡片容器若带 transform/定位会翻转绘制顺序盖住报表，用 z-index 压住 */
    z-index: 10;
  }

  body.printing-count .report-table tr.abnormal td {
    background: #fef0f0 !important;
    -webkit-print-color-adjust: exact;
    print-color-adjust: exact;
  }
  /* @page 已挪到 usePrintPage 动态注入：静态写在 SFC 里会和标签页的纵向 A4 互相覆盖 */
}
</style>
