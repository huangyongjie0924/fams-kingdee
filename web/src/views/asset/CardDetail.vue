<template>
  <el-drawer
    :model-value="modelValue"
    title="资产详情"
    :size="isMobile ? '100%' : '720px'"
    @update:model-value="emit('update:modelValue', $event)"
    @open="load"
    @close="onClose"
  >
    <el-empty v-if="!card && loadErr" :description="loadErr" />

    <!-- 左内容 + 右粘性操作栏（方案 §6.3）：滚到「基本信息」下半段时操作依然在视野内。
         抽屉在窄屏只有一列宽，那里退回页内按钮（见下方 .ops 的 v-if）。 -->
    <div v-if="card" class="detail-body">
      <div class="detail-main">
        <el-tabs v-model="tab">
      <el-tab-pane label="基本信息" name="basic">
        <el-descriptions :column="cols" border>
          <el-descriptions-item label="资产编码"><span class="u-mono">{{ card.asset_code }}</span></el-descriptions-item>
          <el-descriptions-item label="资产名称">{{ card.name }}</el-descriptions-item>
          <el-descriptions-item label="资产类别">{{ card.category_name }}</el-descriptions-item>
          <el-descriptions-item label="规格型号">{{ card.spec }}</el-descriptions-item>
          <el-descriptions-item label="设备序列号"><span class="u-mono">{{ card.serial_no }}</span></el-descriptions-item>
          <el-descriptions-item label="计量单位">{{ card.unit }}</el-descriptions-item>
          <el-descriptions-item label="数量"><span class="u-mono">{{ qty(card.quantity) }}</span></el-descriptions-item>
          <el-descriptions-item label="状态">
            <StatusChip :value="card.display_status || ''" :label="card.display_status || '-'" />
          </el-descriptions-item>
          <el-descriptions-item label="金额"><span class="u-mono">{{ money(card.amount) }}</span></el-descriptions-item>
          <el-descriptions-item label="使用公司">{{ card.use_company_name }}</el-descriptions-item>
          <el-descriptions-item label="使用部门">{{ card.use_dept_name }}</el-descriptions-item>
          <el-descriptions-item label="使用人">{{ card.user_emp_name }}</el-descriptions-item>
          <el-descriptions-item label="使用状态">{{ card.use_status }}</el-descriptions-item>
          <el-descriptions-item label="管理人">{{ card.manager_emp_name }}</el-descriptions-item>
          <el-descriptions-item label="所属公司">{{ card.owner_company_name }}</el-descriptions-item>
          <el-descriptions-item label="区域">{{ card.area_name }}</el-descriptions-item>
          <el-descriptions-item label="存放地点">{{ card.location }}</el-descriptions-item>
          <el-descriptions-item label="购入日期">{{ card.purchase_date }}</el-descriptions-item>
          <el-descriptions-item label="建卡时间">{{ card.card_created_at }}</el-descriptions-item>
          <el-descriptions-item label="使用期限">{{ card.use_months }} 个月</el-descriptions-item>
          <el-descriptions-item label="来源">{{ card.source }}</el-descriptions-item>
          <el-descriptions-item label="入库/收货单号">{{ card.in_stock_no }}</el-descriptions-item>
          <el-descriptions-item label="RFID">{{ card.rfid }}</el-descriptions-item>
          <el-descriptions-item label="标签" :span="2">
            <el-tag v-for="t in card.tags || []" :key="t" size="small" style="margin-right: 4px">{{ t }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="备注" :span="2">{{ card.remark }}</el-descriptions-item>
        </el-descriptions>
        <!-- 窄屏没有右栏，操作留在页内 -->
        <div v-if="isMobile" class="ops">
          <el-button :icon="Printer" @click="printLabel">打印标签</el-button>
          <!-- 扫码落地页的「报修」入口：存量 227 张标签零重印，扫完进详情再点一下（D6） -->
          <!-- 仅设备类（分类被标记为可维修）才显示报修；后端仍独立拦截，此处隐藏只是体验 -->
          <el-button v-if="auth.can('repair.report') && card?.category_repairable" type="primary" @click="goReport">报修</el-button>
          <!-- 「跟资产台账做关联生成」的起点：把这张卡带进 IT 台账，已登记过就直接打开那条 -->
          <el-button v-if="auth.can('it.manage')" @click="goITEquipment">生成 IT 设备记录</el-button>
        </div>

        <!-- 扫码后最想知道的是「这台机器现在要不要盘」，所以摆在详情正下方 -->
        <div v-if="countItems.length" class="count-strip">
          <div v-for="it in countItems" :key="it.item_id" class="count-row">
            <span class="pname">{{ it.plan_name }}</span>
            <span class="pcode">{{ it.plan_code }}</span>
            <el-tag size="small" :type="it.result ? 'success' : 'info'">{{ it.result || "未盘" }}</el-tag>
            <span class="muted">{{ it.assignee_name ? `指派给 ${it.assignee_name}` : "未指派" }}</span>
            <div class="spacer" />
            <el-button
              v-if="auth.can('count.enter')"
              size="small"
              type="primary"
              :disabled="!canCountItem(it)"
              :title="canCountItem(it) ? '' : '该盘点明细未指派给你'"
              @click="goCount(it)"
            >
              立即盘这一条
            </el-button>
          </div>
        </div>

        <!-- 一台资产的历次维修记录：判断「该不该报废」时的依据（P0-6） -->
        <div v-if="repairOrders.length" class="repair-strip">
          <div v-for="r in repairOrders" :key="r.id" class="repair-row">
            <span class="pcode u-mono">{{ r.code }}</span>
            <StatusChip kind="repair" :value="r.status" :label="r.status_label" />
            <span class="fault">{{ r.fault_desc }}</span>
            <span class="muted">{{ handlerText(r) }}</span>
            <div class="spacer" />
            <span class="muted u-mono">{{ formatTime(r.created_at) }}</span>
            <el-button size="small" link type="primary" @click="goRepair(r)">查看</el-button>
          </div>
        </div>

        <div v-if="photos.length" class="photos">
          <el-image
            v-for="p in photos"
            :key="p.id"
            :src="p.url"
            :preview-src-list="photos.map((x) => x.url)"
            fit="cover"
            class="photo"
          />
        </div>
      </el-tab-pane>

      <el-tab-pane label="财务信息" name="fin">
        <!-- 说清楚「谁维护这些字段」：金蝶同步来的卡以星瀚为准，次日同步会覆盖本地改动；
             手工建卡不在金蝶里，由本地维护。不写清楚用户会反复尝试编辑却改不动。 -->
        <el-alert
          v-if="card.synced"
          type="info"
          :closable="false"
          show-icon
          class="fin-tip"
          title="财务字段由金蝶星瀚同步维护，本地不可改"
        >
          <template #default>
            原值 / 累计折旧 / 净值等取自星瀚资产卡，每次同步都以星瀚为准覆盖本地值。
          </template>
        </el-alert>
        <el-alert
          v-else
          type="success"
          :closable="false"
          show-icon
          class="fin-tip"
          title="本资产为手工建卡，财务字段由本地维护"
        />
        <el-descriptions :column="cols" border>
          <el-descriptions-item label="资产类型">{{ card.fin_asset_type }}</el-descriptions-item>
          <el-descriptions-item label="所属公司">{{ card.owner_company_name }}</el-descriptions-item>
          <el-descriptions-item label="分摊部门">{{ card.fin_share_dept_name }}</el-descriptions-item>
          <el-descriptions-item label="供应商">{{ card.vendor_name }}</el-descriptions-item>
          <el-descriptions-item label="含税金额">{{ money(card.fin_amount_with_tax) }}</el-descriptions-item>
          <el-descriptions-item label="数量">{{ qty(card.quantity) }}</el-descriptions-item>
          <el-descriptions-item label="税额">{{ money(card.fin_tax) }}</el-descriptions-item>
          <el-descriptions-item label="原值">{{ money(card.fin_original_value) }}</el-descriptions-item>
          <el-descriptions-item label="累计折旧">{{ money(card.fin_accum_depreciation) }}</el-descriptions-item>
          <el-descriptions-item label="净值">{{ money(card.fin_net_value) }}</el-descriptions-item>
          <el-descriptions-item label="残值率">{{ card.fin_residual_rate }}%</el-descriptions-item>
          <el-descriptions-item label="财务使用期限">{{ card.fin_use_months }} 个月</el-descriptions-item>
          <el-descriptions-item label="入账期间">{{ card.fin_period }}</el-descriptions-item>
          <el-descriptions-item label="入账时间">{{ card.fin_entry_date }}</el-descriptions-item>
          <el-descriptions-item label="财务信息状态">{{ card.fin_status }}</el-descriptions-item>
        </el-descriptions>
      </el-tab-pane>

      <el-tab-pane label="维保信息" name="mt">
        <el-descriptions :column="cols" border>
          <el-descriptions-item label="维保供应商">{{ card.mt_vendor_name }}</el-descriptions-item>
          <el-descriptions-item label="供应商联系人">{{ card.mt_contact }}</el-descriptions-item>
          <el-descriptions-item label="联系方式">{{ card.mt_phone }}</el-descriptions-item>
          <el-descriptions-item label="负责人">{{ card.mt_owner_emp_name }}</el-descriptions-item>
          <el-descriptions-item label="维保到期时间">{{ card.mt_expire_date }}</el-descriptions-item>
          <el-descriptions-item label="维保说明" :span="2">{{ card.mt_remark }}</el-descriptions-item>
        </el-descriptions>
      </el-tab-pane>

      <el-tab-pane label="资产履历" name="history">
        <el-timeline>
          <el-timeline-item
            v-for="h in history"
            :key="h.id"
            :timestamp="formatTime(h.created_at)"
            placement="top"
          >
            <b>{{ actionLabel(h.action) }}</b>
            <span v-if="h.field">
              ：{{ h.field }} {{ h.old_value || "空" }} → {{ h.new_value || "空" }}
            </span>
            <span class="operator">{{ h.operator }}</span>
          </el-timeline-item>
        </el-timeline>
        <el-empty v-if="!history.length" description="暂无履历" />
      </el-tab-pane>
        </el-tabs>
      </div>

      <!-- 右栏粘性操作栏 -->
      <aside v-if="!isMobile" class="detail-rail">
        <el-button class="rail-btn" :icon="Printer" @click="printLabel">打印标签</el-button>
        <el-button
          v-if="auth.can('repair.report') && card?.category_repairable"
          class="rail-btn"
          type="primary"
          @click="goReport"
        >
          报修
        </el-button>
        <!-- 「跟资产台账做关联生成」的起点：带着这张卡跳到 IT 台账页。
             该页会先查这张卡有没有已登记的 IT 记录：有 → 直接打开编辑那条；
             没有 → 打开新增并预填编码/使用人/部门/地点，人只需补 IT 属性。 -->
        <el-button v-if="auth.can('it.manage')" class="rail-btn" @click="goITEquipment">
          生成 IT 设备记录
        </el-button>
        <p class="rail-note">扫码落地后可直接报修，无需再找资产管理员</p>
      </aside>
    </div>
  </el-drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { Printer } from "@element-plus/icons-vue";
import http from "../../api/client";
import { money, qty } from "../../api/meta";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";
import StatusChip from "../../components/StatusChip.vue";

const props = defineProps<{ modelValue: boolean; cardId: number }>();
const emit = defineEmits(["update:modelValue"]);

const router = useRouter();
const isMobile = useIsMobile();
const cols = computed(() => (isMobile.value ? 1 : 2));
const tab = ref("basic");
const card = ref<any>(null);
const history = ref<any[]>([]);
const photos = ref<any[]>([]);
const countItems = ref<any[]>([]);
const repairOrders = ref<any[]>([]);
const loadErr = ref("");

const ACTIONS: Record<string, string> = {
  create: "新建资产",
  update: "修改",
  delete: "删除资产",
  import: "批量导入",
  repair: "维修",
};

function actionLabel(a: string) {
  return ACTIONS[a] || a;
}

function handlerText(r: any): string {
  if (r?.assignee_name) return r.assignee_name;
  if (r?.vendor_name) return r.vendor_name;
  return "待指派";
}

function formatTime(t: string) {
  return (t || "").replace("T", " ").slice(0, 19);
}

// 管理类角色录盘点不受指派限制（后端 restrictEmployeeID 为 0），盘点员只能录自己的。
// 判断口径必须与后端 SubmitResults 一致，否则这里放行了、保存时会被静默丢成 0 行。
function canCountItem(it: any): boolean {
  if (!auth.can("count.enter")) return false;
  if (auth.user?.role !== "counter") return true;
  return it.assignee_id > 0 && it.assignee_id === auth.user?.employee_id;
}

function goCount(it: any) {
  router.push({ name: "count-detail", params: { id: it.plan_id }, query: { item: it.item_id } });
}

function printLabel() {
  router.push({ name: "asset-labels", query: { ids: String(props.cardId) } });
}

// 报修：带上资产编码跳报修页，报修页据此回填资产信息（D6 复用旧深链 + 落地页按钮）
function goReport() {
  router.push({ name: "repair-new", query: { asset_code: card.value?.asset_code || "" } });
}

// 「生成 IT 设备记录」：带着这张卡的 id 跳到 IT 台账页（2026-10-10 需求）。
//
// 为什么不在这里内联一个表单：IT 属性有 14 个字段，抽屉里塞不下；而且这个动作的语义
// 本来就是「去 IT 台账处理这台设备」，跳过去之后用户还能顺手看到别的设备。
// 页面侧按 from_card 判重：已登记过就直接打开那条记录，不会重复新增
// （一张资产卡最多一条 IT 记录，由 uk_it_card 保证）。
function goITEquipment() {
  router.push({ name: "it-equipment", query: { from_card: String(props.cardId) } });
}

function goRepair(r: any) {
  router.push({ name: "repair-detail", params: { id: r.id } });
}

// 打开抽屉时 @open 和 watch(cardId) 会同时触发一次加载，不挡住就会把每个请求发两遍
// （受限账号点范围外的资产会因此弹两个一模一样的错误 toast）。
// loadedId 记的是「已经加载/正在加载的卡」，关抽屉时清空，所以重新打开同一张卡仍会刷新。
let loadedId: number | null = null;

function onClose() {
  loadedId = null;
}

async function load() {
  if (!props.cardId) return;
  if (loadedId === props.cardId) return;
  const id = props.cardId;
  loadedId = id;
  tab.value = "basic";
  // 先清空：受限账号点开范围外的资产会 403，不清就会继续显示上一张卡的数据
  card.value = null;
  history.value = [];
  photos.value = [];
  countItems.value = [];
  repairOrders.value = [];
  loadErr.value = "";
  try {
    // 先单取主记录：范围外的卡在这里就 403 了，三个请求并排发会连弹三次同样的错。
    // 确认看得见，再一并取它的履历和附件。
    const c = await http.get(`/assets/${id}`);
    card.value = c.data;
  } catch (e: any) {
    // 拦截器已经弹过后端的原文，这里只负责别让抽屉留一片空白
    loadErr.value = e?.response?.data?.error || "加载资产详情失败";
    return;
  }

  // 卡已经确认在范围内，这几条不该再失败；真失败了也只是少显示某块，不拦抽屉
  const [h, a, rp] = await Promise.all([
    http.get(`/assets/${id}/history`).catch(() => ({ data: [] })),
    http.get(`/assets/${id}/attachments`).catch(() => ({ data: [] })),
    http.get(`/assets/${id}/repairs`).catch(() => ({ data: [] })),
  ]);
  if (id !== props.cardId) return;
  history.value = h.data || [];
  photos.value = (a.data || []).filter((x: any) => x.kind === "photo");
  repairOrders.value = rp.data || [];

  // 单独取，且只给能盘点的角色取：盘点员账号没绑员工时后端会 403，
  // 放进上面的 Promise.all 会让整个详情抽屉打不开。
  const items = auth.can("count.enter")
    ? await http
        .get(`/assets/${id}/count-items`)
        .then((r) => r.data.items || [])
        .catch(() => [])
    : [];
  if (id !== props.cardId) return;
  countItems.value = items;
}

watch(() => props.cardId, load);
</script>

<style scoped>
/* —— 左内容 + 右粘性操作栏 —— */
.detail-body {
  display: flex;
  align-items: flex-start;
  gap: 16px;
}

.detail-main {
  flex: 1;
  min-width: 0;
}

.detail-rail {
  flex: 0 0 auto;
  width: 128px;
  /* 抽屉 body 就是滚动容器，sticky 相对它生效 */
  position: sticky;
  top: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 10px;
  background: var(--steel-100);
  border: var(--hairline);
  border-radius: var(--r);
}

.rail-btn {
  width: 100%;
  margin-left: 0;
}

.rail-note {
  margin: 4px 0 0;
  font-size: var(--t-11);
  line-height: 1.5;
  color: var(--steel-500);
}

.fin-tip {
  margin-bottom: 12px;
}

.photos {
  display: flex;
  gap: 8px;
  margin-top: 12px;
}

.photo {
  width: 96px;
  height: 96px;
  border-radius: var(--r);
}

.operator {
  margin-left: 8px;
  color: var(--steel-500);
  font-size: var(--t-12);
}

.ops {
  margin-top: 12px;
}

.count-strip {
  margin-top: 12px;
  border: var(--hairline);
  border-left: 3px solid var(--info);
  background: var(--steel-100);
  border-radius: var(--r);
  padding: 4px 8px;
}

.repair-strip {
  margin-top: 12px;
  border: var(--hairline);
  border-left: 3px solid var(--warn);
  background: var(--paper);
  border-radius: var(--r);
  padding: 4px 8px;
}

.repair-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 6px 0;
}

.repair-row + .repair-row {
  border-top: 1px dashed var(--steel-200);
}

.fault {
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.count-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 6px 0;
}

.count-row + .count-row {
  border-top: 1px dashed var(--steel-200);
}

.pname {
  font-weight: 600;
}

.pcode {
  color: var(--steel-500);
  font-size: var(--t-12);
}

.muted {
  color: var(--steel-500);
  font-size: var(--t-12);
}

@media (max-width: 767px), (max-height: 520px) {
  .count-row .spacer {
    display: none;
  }

  .count-row .el-button {
    width: 100%;
  }
}
</style>
