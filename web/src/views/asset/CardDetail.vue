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
    <el-tabs v-if="card" v-model="tab">
      <el-tab-pane label="基本信息" name="basic">
        <el-descriptions :column="cols" border>
          <el-descriptions-item label="资产编码">{{ card.asset_code }}</el-descriptions-item>
          <el-descriptions-item label="资产名称">{{ card.name }}</el-descriptions-item>
          <el-descriptions-item label="资产类别">{{ card.category_name }}</el-descriptions-item>
          <el-descriptions-item label="规格型号">{{ card.spec }}</el-descriptions-item>
          <el-descriptions-item label="设备序列号">{{ card.serial_no }}</el-descriptions-item>
          <el-descriptions-item label="计量单位">{{ card.unit }}</el-descriptions-item>
          <el-descriptions-item label="数量">{{ qty(card.quantity) }}</el-descriptions-item>
          <el-descriptions-item label="状态">
            <el-tag :type="STATUS_TAG[card.display_status] || 'info'" size="small">{{ card.display_status }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="金额">{{ money(card.amount) }}</el-descriptions-item>
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
        <div class="ops">
          <el-button :icon="Printer" @click="printLabel">打印标签</el-button>
          <!-- 扫码落地页的「报修」入口：存量 227 张标签零重印，扫完进详情再点一下（D6） -->
          <!-- 仅设备类（分类被标记为可维修）才显示报修；后端仍独立拦截，此处隐藏只是体验 -->
          <el-button v-if="auth.can('repair.report') && card?.category_repairable" type="primary" @click="goReport">报修</el-button>
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
            <span class="pcode">{{ r.code }}</span>
            <el-tag :type="repairStatusTag(r.status)" size="small">{{ r.status_label }}</el-tag>
            <span class="fault">{{ r.fault_desc }}</span>
            <span class="muted">{{ handlerText(r) }}</span>
            <div class="spacer" />
            <span class="muted">{{ formatTime(r.created_at) }}</span>
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
  </el-drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { Printer } from "@element-plus/icons-vue";
import http from "../../api/client";
import { STATUS_TAG, money, qty, repairStatusTag } from "../../api/meta";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";

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
.photos {
  display: flex;
  gap: 8px;
  margin-top: 12px;
}

.photo {
  width: 96px;
  height: 96px;
  border-radius: 4px;
}

.operator {
  margin-left: 8px;
  color: #909399;
  font-size: 12px;
}

.ops {
  margin-top: 12px;
}

.count-strip {
  margin-top: 12px;
  border: 1px solid #d9ecff;
  background: #f4f9ff;
  border-radius: 4px;
  padding: 4px 8px;
}

.repair-strip {
  margin-top: 12px;
  border: 1px solid #fde2e2;
  background: #fef4f4;
  border-radius: 4px;
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
  border-top: 1px dashed #fde2e2;
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
  border-top: 1px dashed #d9ecff;
}

.pname {
  font-weight: 600;
}

.pcode {
  color: #909399;
  font-size: 12px;
}

.muted {
  color: #909399;
  font-size: 12px;
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
