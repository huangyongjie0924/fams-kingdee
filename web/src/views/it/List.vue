<template>
  <div class="page" :class="{ 'has-selbar': selection.length > 0 }">
    <div class="page-head">
      <div class="ph-title">
        <h1>IT 设备台账</h1>
        <span class="ph-sub">共 <b>{{ total }}</b> 台</span>
        <!-- 这两项是用户明确点出的两个管理盲区，放在页头而不是埋在筛选里 -->
        <span class="ph-sub">信创 <b>{{ xinchuangTotal }}</b> 台</span>
        <span class="ph-sub">无资产卡 <b>{{ unlinkedTotal }}</b> 台</span>
        <span v-if="auth.scopeHint" class="ph-sub">{{ auth.scopeHint }}</span>
      </div>
      <div class="ph-actions">
        <el-button v-if="canManage" type="primary" :icon="Plus" @click="openCreate()">
          新增 IT 设备
        </el-button>
        <el-button v-if="canManage" :icon="Upload" @click="router.push({ name: 'it-import' })">
          导入
        </el-button>
        <el-button :icon="Download" @click="exportExcel">导出</el-button>
      </div>
    </div>

    <div class="page-toolbar">
      <el-input
        v-model="query.keyword"
        class="tb-search"
        placeholder="编码/品牌型号/CPU/硬盘/系统版本/使用人/部门/地点"
        clearable
        @keyup.enter="reload(1)"
        @clear="reload(1)"
      >
        <template #prefix><el-icon><Search /></el-icon></template>
      </el-input>

      <el-select v-model="query.device_type" placeholder="电脑类型" clearable style="width: 130px" @change="reload(1)">
        <el-option v-for="t in enums.it_device_types" :key="t" :label="t" :value="t" />
      </el-select>
      <el-select v-model="query.status" placeholder="状态" clearable style="width: 120px" @change="reload(1)">
        <el-option v-for="s in enums.it_statuses" :key="s" :label="s" :value="s" />
      </el-select>
      <el-select v-model="query.purpose" placeholder="用途" clearable style="width: 110px" @change="reload(1)">
        <el-option v-for="p in enums.it_purposes" :key="p" :label="p" :value="p" />
      </el-select>
      <!-- 信创 / 关联资产 两个筛选用「全部」而不是可清空：
           它们的三态（不筛 / 是 / 否）用 clearable 表达不清晰，会让人分不清
           「清空了」和「选了否」。显式给一个「全部」选项。 -->
      <el-select v-model="query.is_xinchuang" style="width: 120px" @change="reload(1)">
        <el-option label="信创：全部" :value="''" />
        <el-option label="仅信创" value="1" />
        <el-option label="仅非信创" value="0" />
      </el-select>
      <el-select v-model="query.linked" style="width: 150px" @change="reload(1)">
        <el-option label="关联：全部" :value="''" />
        <el-option label="已关联资产卡" value="1" />
        <el-option label="无资产卡设备" value="0" />
      </el-select>

      <el-popover trigger="click" :width="260">
        <template #reference>
          <el-button :icon="SetUp">列配置</el-button>
        </template>
        <el-checkbox-group v-model="enabledProps">
          <el-checkbox v-for="c in IT_COLUMNS" :key="c.prop" :value="c.prop">{{ c.label }}</el-checkbox>
        </el-checkbox-group>
      </el-popover>
    </div>

    <el-table
      v-if="!isMobile"
      v-loading="loading"
      :data="rows"
      border
      height="calc(100vh - 250px)"
      @selection-change="selection = $event"
    >
      <el-table-column type="selection" width="44" />
      <el-table-column
        v-for="col in visibleColumns"
        :key="col.prop"
        :prop="col.prop"
        :label="col.label"
        :width="col.width"
        :align="col.align || 'left'"
        show-overflow-tooltip
      >
        <template #default="{ row }">
          <el-tag v-if="col.prop === 'status'" size="small" :type="itStatusTag(row.status)">{{ row.status }}</el-tag>
          <el-tag v-else-if="col.prop === 'is_xinchuang'" size="small" :type="row.is_xinchuang ? 'success' : 'info'">
            {{ boolLabel(row.is_xinchuang) }}
          </el-tag>
          <span v-else-if="col.prop === 'asset_code'" class="u-mono">{{ row.asset_code || "—" }}</span>
          <span v-else-if="col.prop === 'memory_gb'" class="u-mono">{{ row.memory_gb || "—" }}</span>
          <span v-else>{{ row[col.prop] || "—" }}</span>
        </template>
      </el-table-column>
    </el-table>

    <div v-else v-loading="loading" class="m-list">
      <div v-for="row in rows" :key="row.id" class="m-card" @click="canManage && openEdit(row)">
        <div class="m-card-hd">
          <span class="m-title">{{ row.brand_model || row.asset_code || "未命名设备" }}</span>
          <el-tag size="small" :type="itStatusTag(row.status)">{{ row.status }}</el-tag>
        </div>
        <div class="m-grid">
          <span class="k">资产编码</span><span class="u-mono">{{ row.asset_code || "（无资产卡）" }}</span>
          <span class="k">使用人</span><span>{{ row.user_emp_name || "—" }}</span>
          <span class="k">部门</span><span>{{ row.dept_name || "—" }}</span>
          <span class="k">类型</span><span>{{ row.device_type || "—" }}</span>
          <span class="k">是否信创</span><span>{{ boolLabel(row.is_xinchuang) }}</span>
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
      style="margin-top: 12px; justify-content: flex-end"
      @change="reload()"
    />

    <div v-if="selection.length" class="selbar" role="region" aria-label="批量操作">
      <span class="selbar-count">已选 <b>{{ selection.length }}</b> 项</span>
      <el-button v-if="canManage" text class="sb-btn" :icon="Edit" :disabled="selection.length !== 1" @click="openEdit(selection[0])">
        编辑
      </el-button>
      <el-button v-if="canManage" text class="sb-btn is-danger" :icon="Delete" @click="removeSelected">
        删除
      </el-button>
      <span class="selbar-spacer" />
      <el-button text class="sb-btn" @click="selection = []">取消</el-button>
    </div>

    <!-- 新增 / 编辑对话框 -->
    <el-dialog
      v-model="dialog"
      :title="form.id ? '编辑 IT 设备' : '新增 IT 设备'"
      :width="isMobile ? '94%' : '640px'"
      destroy-on-close
    >
      <el-form ref="formRef" :model="form" label-width="110px">
        <!-- 「关联资产」是「跟资产台账做关联」这一需求的核心控件：
             可搜索选择一张资产卡，也可清空表示「资产卡上没有这台设备」。
             选中时自动带出使用人/部门/地点，这就是「关联生成后完善属性」的流程。 -->
        <el-form-item label="关联资产">
          <el-select
            v-model="form.card_id"
            filterable
            remote
            clearable
            reserve-keyword
            placeholder="搜索资产编码或名称；留空表示资产台账里没有这台设备"
            :remote-method="searchAssets"
            :loading="assetLoading"
            style="width: 100%"
            @change="onCardChange"
          >
            <el-option
              v-for="a in assetOptions"
              :key="a.id"
              :label="`${a.asset_code} ${a.name}`"
              :value="a.id"
            >
              <span class="u-mono">{{ a.asset_code }}</span>
              <span style="margin-left: 8px">{{ a.name }}</span>
            </el-option>
          </el-select>
          <div class="hint">
            已关联的资产卡：一张卡最多登记一条 IT 记录。清空即表示「资产卡上没有的设备」。
          </div>
        </el-form-item>

        <el-form-item label="资产编码">
          <el-input v-model="form.asset_code" :disabled="!!form.card_id" placeholder="无资产卡时可留空，也可填自有编码" />
          <div v-if="form.card_id" class="hint">已关联资产卡，编码以资产台账为准。</div>
        </el-form-item>

        <el-form-item label="使用人">
          <el-select v-model="form.user_emp_id" filterable clearable placeholder="选择使用人" style="width: 100%" @change="onEmpChange">
            <el-option v-for="e in employees" :key="e.id" :label="empLabel(e)" :value="e.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="部门">
          <el-select v-model="form.dept_id" filterable clearable placeholder="选择部门" style="width: 100%">
            <el-option v-for="d in departments" :key="d.id" :label="d.name" :value="d.id" />
          </el-select>
          <div class="hint">一个人可以有多台设备（一人多机）；此处的部门是登记时的快照。</div>
        </el-form-item>

        <el-form-item label="电脑类型">
          <el-select v-model="form.device_type" clearable placeholder="选择类型" style="width: 100%">
            <el-option v-for="t in enums.it_device_types" :key="t" :label="t" :value="t" />
          </el-select>
        </el-form-item>
        <el-form-item label="使用地点">
          <el-input v-model="form.location" placeholder="如：总部 3 楼办公区 / 机房" />
        </el-form-item>
        <el-form-item label="用途">
          <el-select v-model="form.purpose" clearable placeholder="选择用途" style="width: 100%">
            <el-option v-for="p in enums.it_purposes" :key="p" :label="p" :value="p" />
          </el-select>
        </el-form-item>
        <el-form-item label="状态">
          <el-select v-model="form.status" style="width: 100%">
            <el-option v-for="s in enums.it_statuses" :key="s" :label="s" :value="s" />
          </el-select>
        </el-form-item>
        <el-form-item label="是否信创">
          <el-switch v-model="form.is_xinchuang" active-text="是" inactive-text="否" />
        </el-form-item>

        <el-form-item label="品牌.型号">
          <el-input v-model="form.brand_model" placeholder="如：联想 ThinkPad T14" />
        </el-form-item>
        <el-form-item label="CPU型号">
          <el-input v-model="form.cpu" placeholder="如：Intel i7-1260P / 麒麟 9006C" />
        </el-form-item>
        <el-form-item label="内存容量(GB)">
          <el-input-number v-model="form.memory_gb" :min="0" :max="10240" :step="8" style="width: 180px" />
        </el-form-item>
        <el-form-item label="硬盘类型及容量">
          <el-input v-model="form.disk" placeholder="如：SSD 512GB / HDD 1TB + SSD 256GB" />
        </el-form-item>
        <el-form-item label="系统版本">
          <el-input v-model="form.os_version" placeholder="如：Windows 11 专业版 / 统信 UOS 20" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" type="textarea" :rows="2" maxlength="500" show-word-limit />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import { Delete, Download, Edit, Plus, Search, SetUp, Upload } from "@element-plus/icons-vue";
import http, { download } from "../../api/client";
import { IT_COLUMNS, boolLabel, itStatusTag, pickITSubmit } from "../../api/it";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";

const route = useRoute();
const router = useRouter();
const isMobile = useIsMobile();
const canManage = computed(() => auth.can("it.manage"));

const rows = ref<any[]>([]);
const total = ref(0);
const xinchuangTotal = ref(0);
const unlinkedTotal = ref(0);
const loading = ref(false);
const selection = ref<any[]>([]);

const enums = ref<Record<string, string[]>>({ it_device_types: [], it_purposes: [], it_statuses: [] });
const employees = ref<any[]>([]);
const departments = ref<any[]>([]);

const query = ref<any>({
  keyword: "",
  device_type: "",
  status: "",
  purpose: "",
  is_xinchuang: "",
  linked: "",
  page: 1,
  page_size: 50,
});

// 列配置：默认取 defaultOn 的列，用户可自行增减。
const enabledProps = ref<string[]>(IT_COLUMNS.filter((c) => c.defaultOn).map((c) => c.prop));
const visibleColumns = computed(() => IT_COLUMNS.filter((c) => enabledProps.value.includes(c.prop)));

const dialog = ref(false);
const saving = ref(false);
const formRef = ref();
const form = ref<any>({});
const assetOptions = ref<any[]>([]);
const assetLoading = ref(false);

function emptyForm(): Record<string, any> {
  return {
    id: 0,
    card_id: 0,
    asset_code: "",
    device_type: "",
    user_emp_id: 0,
    dept_id: 0,
    location: "",
    purpose: "",
    status: "在用",
    is_xinchuang: false,
    brand_model: "",
    cpu: "",
    memory_gb: 0,
    disk: "",
    os_version: "",
    remark: "",
  };
}

function empLabel(e: any) {
  return e.dept_name ? `${e.name}（${e.dept_name}）` : e.name;
}

// 空串要转成 undefined 再交给 axios，否则 `?linked=` 会被后端当成缺失（好），
// 但 `?is_xinchuang=` 也一样 —— 两者行为一致，都表示「不筛选」。这里显式过滤掉，
// 让 URL 更干净、也便于人工排查。
function cleanParams(q: Record<string, any>): Record<string, any> {
  const out: Record<string, any> = {};
  for (const [k, v] of Object.entries(q)) {
    if (v === "" || v === null || v === undefined) continue;
    out[k] = v;
  }
  return out;
}

async function reload(page?: number) {
  if (page) query.value.page = page;
  loading.value = true;
  try {
    const { data } = await http.get("/it-equipment", { params: cleanParams(query.value) });
    rows.value = data.items || [];
    total.value = data.total || 0;
    xinchuangTotal.value = data.xinchuang_total || 0;
    unlinkedTotal.value = data.unlinked_total || 0;
  } finally {
    loading.value = false;
  }
}

function exportExcel() {
  return download("/it-equipment/export", cleanParams(query.value));
}

// —— 关联资产选择器：远程搜索资产台账 ——
// 只拉 20 条：这个选择器是用来「挑出那一台」的，不是用来浏览全库的。
async function searchAssets(kw: string) {
  assetLoading.value = true;
  try {
    const { data } = await http.get("/assets", { params: { keyword: kw || "", page_size: 20 } });
    assetOptions.value = data.items || [];
  } catch {
    assetOptions.value = [];
  } finally {
    assetLoading.value = false;
  }
}

// 选中资产卡后带出资产编码/使用人/部门/地点 —— 这就是「关联生成后完善设备属性」的第一步。
// 只填空字段，不覆盖用户已经填过的值：用户可能是先填了属性再去关联的。
function onCardChange(id: number) {
  if (!id) {
    // 清空关联 = 这台设备在资产台账里不存在。asset_code 保留原值让用户自己决定改不改
    // （无卡设备通常需要一个自编编码，直接清掉反而要重打一遍）。
    return;
  }
  const a = assetOptions.value.find((x) => x.id === id);
  if (!a) return;
  form.value.asset_code = a.asset_code || "";
  if (!form.value.user_emp_id && a.user_emp_id) form.value.user_emp_id = a.user_emp_id;
  if (!form.value.dept_id && a.use_dept_id) form.value.dept_id = a.use_dept_id;
  if (!form.value.location && a.location) form.value.location = a.location;
}

// 选了使用人就把部门带出来（人所属部门）。同样只填空字段。
function onEmpChange(id: number) {
  if (!id || form.value.dept_id) return;
  const e = employees.value.find((x) => x.id === id);
  if (e?.dept_id) form.value.dept_id = e.dept_id;
}

function openCreate(preset?: Record<string, any>) {
  form.value = { ...emptyForm(), ...(preset || {}) };
  assetOptions.value = [];
  dialog.value = true;
}

function openEdit(row: any) {
  form.value = { ...emptyForm(), ...row };
  // 回显关联资产时把当前这张卡塞进选项，否则 el-select 显示的是裸 id。
  assetOptions.value = row.card_id
    ? [{ id: row.card_id, asset_code: row.asset_code, name: row.asset_name || "" }]
    : [];
  dialog.value = true;
}

async function save() {
  if (!form.value.status) {
    ElMessage.warning("请选择状态");
    return;
  }
  saving.value = true;
  try {
    const payload = pickITSubmit(form.value);
    if (form.value.id) {
      await http.put(`/it-equipment/${form.value.id}`, payload);
      ElMessage.success("已保存");
    } else {
      await http.post("/it-equipment", payload);
      ElMessage.success("已新增");
    }
    dialog.value = false;
    await reload();
  } finally {
    saving.value = false;
  }
}

async function removeSelected() {
  const ids = selection.value.map((r) => r.id);
  if (!ids.length) return;
  const codes = selection.value.map((r) => r.asset_code || r.brand_model || `#${r.id}`);
  const shown = codes.slice(0, 8).join("、");
  const more = codes.length > 8 ? ` 等 ${codes.length} 台` : "";
  await ElMessageBox.confirm(
    `确认删除以下 ${codes.length} 条 IT 设备记录？\n${shown}${more}\n\n删除不可恢复。`,
    "删除确认",
    { type: "warning", confirmButtonText: "删除", cancelButtonText: "取消" },
  );
  for (const id of ids) {
    await http.delete(`/it-equipment/${id}`);
  }
  ElMessage.success(`已删除 ${ids.length} 条`);
  selection.value = [];
  await reload();
}

// —— 「从资产台账关联生成」的入口 ——
// 资产详情抽屉里点「生成 IT 设备记录」会带 from_card=<资产卡 id> 跳到这里。
// 已存在记录 → 直接打开编辑那条；不存在 → 打开新增并预填卡上的信息。
// 判重复用列表接口（?card_id=），不新增端点（见 model/it.go 的 CardID 注释）。
let lastFromCard = 0;

async function resolveFromCard() {
  const id = Number(route.query.from_card || 0);
  if (!id || id === lastFromCard) return;
  lastFromCard = id;
  try {
    const [cardRes, existRes] = await Promise.all([
      http.get(`/assets/${id}`),
      http.get("/it-equipment", { params: { card_id: id, page_size: 1 } }),
    ]);
    const card = cardRes.data;
    const existing = existRes.data.items?.[0];
    if (existing) {
      openEdit(existing);
      ElMessage.info("这台资产已登记过 IT 设备信息，已为你打开那条记录");
      return;
    }
    openCreate({
      card_id: card.id,
      asset_code: card.asset_code || "",
      user_emp_id: card.user_emp_id || 0,
      dept_id: card.use_dept_id || 0,
      location: card.location || "",
    });
  } catch {
    // 拦截器已提示（资产不存在 / 无权限），这里不重复报错。
    lastFromCard = 0;
  }
}

watch(() => route.query.from_card, resolveFromCard, { immediate: true });

onMounted(async () => {
  const [e, emp, dep] = await Promise.all([
    http.get("/enums"),
    http.get("/employees"),
    http.get("/departments"),
  ]);
  enums.value = {
    it_device_types: e.data.it_device_types || [],
    it_purposes: e.data.it_purposes || [],
    it_statuses: e.data.it_statuses || [],
  };
  employees.value = emp.data || [];
  departments.value = dep.data || [];
  await reload(1);
  await resolveFromCard();
});

defineExpose({ reload });
</script>

<style scoped>
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
.page-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.tb-search {
  width: 320px;
}
.hint {
  font-size: var(--t-11);
  color: var(--steel-500);
  line-height: 1.5;
}
/* 选中浮条：与资产台账同款（未选中不显示）。 */
.selbar {
  position: fixed;
  left: 50%;
  bottom: 24px;
  transform: translateX(-50%);
  z-index: 20;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
  /* 圆角用全站唯一的 --r（2px）：设计系统明确「全站唯一圆角」，不引入新值 */
  border-radius: var(--r);
  background: var(--ink);
  color: #fff;
  box-shadow: 0 6px 24px rgba(0, 0, 0, 0.24);
}
.selbar-count {
  font-size: var(--t-12);
  color: #c8cddb;
}
.selbar-count b {
  color: #fff;
}
.selbar-spacer {
  flex: 1;
  min-width: 16px;
}
.sb-btn {
  color: #fff;
}
.sb-btn.is-danger {
  color: #ffb4a8;
}
.m-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.m-grid {
  display: grid;
  grid-template-columns: 72px 1fr;
  gap: 4px 8px;
  font-size: var(--t-12);
}
.m-grid .k {
  color: var(--steel-500);
}
</style>
