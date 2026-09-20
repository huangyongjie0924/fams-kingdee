<template>
  <div class="page">
    <el-card v-loading="loading">
      <div class="toolbar">
        <el-button :icon="ArrowLeft" @click="back">返回</el-button>
        <span class="title">维修单 {{ order?.code || "" }}</span>
        <el-tag v-if="order" :type="repairStatusTag(order.status)" size="small">{{ order.status_label }}</el-tag>
        <span class="spacer" />
        <el-button :icon="Refresh" @click="load">刷新</el-button>
      </div>

      <template v-if="order">
        <el-descriptions :column="isMobile ? 1 : 2" border>
          <el-descriptions-item label="资产编码">{{ order.asset_code }}</el-descriptions-item>
          <el-descriptions-item label="资产名称">{{ order.asset_name }}</el-descriptions-item>
          <el-descriptions-item label="报修人">{{ order.reporter_name || "-" }}</el-descriptions-item>
          <el-descriptions-item label="使用部门">{{ order.use_dept_name || "-" }}</el-descriptions-item>
          <el-descriptions-item label="存放地点">{{ order.location || "-" }}</el-descriptions-item>
          <el-descriptions-item label="紧急程度">
            {{ REPAIR_URGENCY_LABELS[order.urgency] || order.urgency }}
          </el-descriptions-item>
          <el-descriptions-item label="处理人">{{ handlerText(order) }}</el-descriptions-item>
          <el-descriptions-item label="提交时间">{{ formatTime(order.created_at) }}</el-descriptions-item>
          <el-descriptions-item label="问题描述" :span="isMobile ? 1 : 2">{{ order.fault_desc }}</el-descriptions-item>
          <el-descriptions-item v-if="order.handler_desc" label="维修措施" :span="isMobile ? 1 : 2">
            {{ order.handler_desc }}
          </el-descriptions-item>
          <el-descriptions-item v-if="order.reject_reason" label="驳回 / 退回原因" :span="isMobile ? 1 : 2">
            {{ order.reject_reason }}
          </el-descriptions-item>
        </el-descriptions>

        <div class="ops">
          <el-button v-if="canAccept" type="primary" @click="act('accept')">受理</el-button>
          <el-button v-if="canReject" type="danger" plain @click="openRemark('reject')">驳回</el-button>
          <el-button v-if="canDispatch" type="primary" @click="openDispatch">派工</el-button>
          <el-button v-if="canTake" type="primary" @click="act('take')">接单</el-button>
          <el-button v-if="canFinish" type="primary" @click="openFinish">报完工</el-button>
          <el-button v-if="canConfirm" type="success" @click="act('confirm')">确认完工</el-button>
          <el-button v-if="canReturn" type="warning" plain @click="openRemark('return')">退回</el-button>
          <el-button v-if="canCancel" type="danger" plain @click="openRemark('cancel')">撤单 / 取消</el-button>
        </div>

        <el-divider content-position="left">维修照片 / 附件</el-divider>
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
        <div v-if="files.length" class="files">
          <a v-for="f in files" :key="f.id" :href="f.url" target="_blank" class="file-link">{{ f.origin_name }}</a>
        </div>
        <el-empty v-if="!attachments.length" description="暂无附件" :image-size="60" />

        <el-divider content-position="left">处理进度</el-divider>
        <el-timeline>
          <el-timeline-item
            v-for="l in logs"
            :key="l.id"
            :timestamp="formatTime(l.created_at)"
            placement="top"
          >
            <b>{{ l.action_label }}</b>
            <span v-if="l.from_status || l.to_status">
              ：{{ l.from_label || "—" }} → {{ l.to_label || "—" }}
            </span>
            <span v-if="l.remark" class="remark">（{{ l.remark }}）</span>
            <span class="operator">{{ l.operator }}</span>
          </el-timeline-item>
        </el-timeline>
        <el-empty v-if="!logs.length" description="暂无进度" :image-size="60" />
      </template>
      <el-empty v-else-if="!loading" description="维修单不存在" />
    </el-card>

    <!-- 派工 -->
    <el-dialog v-model="dispatchVisible" title="派工" :fullscreen="isMobile" width="460px">
      <el-form label-width="90px" :label-position="isMobile ? 'top' : 'right'">
        <el-form-item label="内部维修工">
          <el-select v-model="dispatchForm.assignee_emp_id" clearable filterable style="width: 100%">
            <el-option v-for="e in employees" :key="e.id" :label="empLabel(e)" :value="e.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="外部供应商">
          <el-select v-model="dispatchForm.vendor_id" clearable filterable style="width: 100%">
            <el-option v-for="v in vendors" :key="v.id" :label="v.name" :value="v.id" />
          </el-select>
        </el-form-item>
        <div class="muted">内部维修工与外部供应商至少填一个。</div>
      </el-form>
      <template #footer>
        <el-button @click="dispatchVisible = false">取消</el-button>
        <el-button type="primary" :loading="acting" @click="submitDispatch">确定</el-button>
      </template>
    </el-dialog>

    <!-- 报完工 -->
    <el-dialog v-model="finishVisible" title="报完工" :fullscreen="isMobile" width="520px">
      <el-form label-width="90px" :label-position="isMobile ? 'top' : 'right'">
        <el-form-item label="维修措施">
          <el-input
            v-model="finishForm.handler_desc"
            type="textarea"
            :rows="3"
            maxlength="500"
            show-word-limit
            placeholder="维修过程 / 更换配件说明"
          />
        </el-form-item>
        <el-form-item label="维修照片">
          <el-upload
            v-model:file-list="finishFiles"
            :http-request="uploadFinishPhoto"
            :before-upload="beforePhoto"
            accept=".jpg,.jpeg,.png"
            list-type="picture-card"
          >
            <el-icon><Plus /></el-icon>
          </el-upload>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="finishVisible = false">取消</el-button>
        <el-button type="primary" :loading="acting" @click="submitFinish">提交</el-button>
      </template>
    </el-dialog>

    <!-- 填原因（驳回 / 退回 / 取消） -->
    <el-dialog v-model="remarkVisible" :title="remarkTitle" :fullscreen="isMobile" width="420px">
      <el-input
        v-model="remarkText"
        type="textarea"
        :rows="3"
        maxlength="255"
        show-word-limit
        :placeholder="remarkRequired ? '必填' : '可选'"
      />
      <template #footer>
        <el-button @click="remarkVisible = false">取消</el-button>
        <el-button type="primary" :loading="acting" @click="submitRemark">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import { ArrowLeft, Refresh, Plus } from "@element-plus/icons-vue";
import http from "../../api/client";
import { repairStatusTag, REPAIR_URGENCY_LABELS } from "../../api/meta";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";

const route = useRoute();
const router = useRouter();
const isMobile = useIsMobile();

const orderId = computed(() => Number(route.params.id));
const order = ref<any>(null);
const logs = ref<any[]>([]);
const attachments = ref<any[]>([]);
const loading = ref(false);
const acting = ref(false);
const employees = ref<any[]>([]);
const vendors = ref<any[]>([]);

const dispatchVisible = ref(false);
const dispatchForm = ref<{ assignee_emp_id: number | null; vendor_id: number | null }>({
  assignee_emp_id: null,
  vendor_id: null,
});

const finishVisible = ref(false);
const finishForm = ref({ handler_desc: "" });
const finishFiles = ref<any[]>([]);

const remarkVisible = ref(false);
const remarkAction = ref<"reject" | "return" | "cancel">("reject");
const remarkText = ref("");

const photos = computed(() => attachments.value.filter((a) => a.kind === "photo"));
const files = computed(() => attachments.value.filter((a) => a.kind !== "photo"));

const isReporter = computed(
  () => !!auth.user?.employee_id && auth.user?.employee_id === order.value?.reporter_emp_id,
);
const isAssignee = computed(
  () => !!auth.user?.employee_id && auth.user?.employee_id === order.value?.assignee_emp_id,
);
const isAdmin = computed(() => auth.user?.role === "admin");
const canDispatchPerm = computed(() => auth.can("repair.dispatch"));

// 按钮可见性：与后端对象级鉴权口径保持一致（否则会出现「点得下去但 403」）
const canAccept = computed(() => order.value?.status === "pending" && canDispatchPerm.value);
const canReject = computed(() => order.value?.status === "pending" && canDispatchPerm.value);
const canDispatch = computed(() => order.value?.status === "accepted" && canDispatchPerm.value);
const canTake = computed(
  () => order.value?.status === "dispatched" && (isAssignee.value || isAdmin.value),
);
const canFinish = computed(
  () => order.value?.status === "repairing" && (isAssignee.value || isAdmin.value),
);
const canConfirm = computed(
  () => order.value?.status === "confirming" && (isReporter.value || canDispatchPerm.value || isAdmin.value),
);
const canReturn = computed(() => canConfirm.value);
const canCancel = computed(() => {
  const s = order.value?.status;
  if (s === "pending") return isReporter.value || canDispatchPerm.value || isAdmin.value;
  if (s === "accepted" || s === "dispatched") return canDispatchPerm.value || isAdmin.value;
  return false;
});

const remarkRequired = computed(() => remarkAction.value === "reject" || remarkAction.value === "return");
const remarkTitle = computed(
  () => ({ reject: "驳回", return: "退回", cancel: "撤单 / 取消" })[remarkAction.value],
);

function handlerText(o: any): string {
  if (o?.assignee_name) return o.assignee_name;
  if (o?.vendor_name) return o.vendor_name;
  return "待指派";
}

function formatTime(t: string) {
  return (t || "").replace("T", " ").slice(0, 19);
}

function empLabel(e: any) {
  return e.dept_name ? `${e.name}（${e.dept_name}）` : e.name;
}

async function load() {
  loading.value = true;
  try {
    const { data } = await http.get(`/repairs/${orderId.value}`);
    order.value = data;
    const [lg, at] = await Promise.all([
      http.get(`/repairs/${orderId.value}/logs`).catch(() => ({ data: [] })),
      http.get(`/repairs/${orderId.value}/attachments`).catch(() => ({ data: [] })),
    ]);
    logs.value = lg.data || [];
    attachments.value = at.data || [];
  } finally {
    loading.value = false;
  }
}

async function act(action: string, body: Record<string, any> = {}) {
  acting.value = true;
  try {
    await http.post(`/repairs/${orderId.value}/${action}`, body);
    ElMessage.success("已提交");
    await load();
  } finally {
    acting.value = false;
  }
}

function openDispatch() {
  dispatchForm.value = { assignee_emp_id: null, vendor_id: null };
  dispatchVisible.value = true;
}

async function submitDispatch() {
  const f = dispatchForm.value;
  if (!f.assignee_emp_id && !f.vendor_id) {
    ElMessage.warning("请指派内部维修工或外部供应商");
    return;
  }
  dispatchVisible.value = false;
  await act("dispatch", {
    assignee_emp_id: f.assignee_emp_id || 0,
    vendor_id: f.vendor_id || 0,
  });
}

function openFinish() {
  finishForm.value = { handler_desc: "" };
  finishFiles.value = [];
  finishVisible.value = true;
}

const MAX_PHOTO_MB = 10;
function beforePhoto(file: File) {
  if (file.size > MAX_PHOTO_MB * 1024 * 1024) {
    ElMessage.error(`单张照片不能超过 ${MAX_PHOTO_MB}MB`);
    return false;
  }
  return true;
}

async function uploadFinishPhoto(options: any) {
  const fd = new FormData();
  fd.append("file", options.file);
  fd.append("repair_id", String(orderId.value));
  fd.append("card_id", String(order.value?.card_id || 0));
  const { data } = await http.post("/repair-upload", fd);
  return data;
}

async function submitFinish() {
  if (!finishForm.value.handler_desc.trim()) {
    ElMessage.warning("请填写维修措施 / 更换配件说明");
    return;
  }
  const ids = finishFiles.value.map((f: any) => f.response?.id).filter((id: any) => !!id);
  finishVisible.value = false;
  await act("finish", { handler_desc: finishForm.value.handler_desc, attachment_ids: ids });
}

function openRemark(action: "reject" | "return" | "cancel") {
  remarkAction.value = action;
  remarkText.value = "";
  remarkVisible.value = true;
}

async function submitRemark() {
  const text = remarkText.value.trim();
  if (remarkRequired.value && !text) {
    ElMessage.warning("请填写原因");
    return;
  }
  const action = remarkAction.value;
  remarkVisible.value = false;
  await act(action, { remark: text });
}

function back() {
  router.push({ name: "repairs" });
}

onMounted(async () => {
  await load();
  // 派工对话框要用到的人员与供应商，按需加载，失败不拦页面
  if (canDispatchPerm.value) {
    const [e, v] = await Promise.all([
      http.get("/employees").catch(() => ({ data: [] })),
      http.get("/vendors").catch(() => ({ data: [] })),
    ]);
    employees.value = e.data || [];
    vendors.value = v.data || [];
  }
});
</script>

<style scoped>
.title {
  font-size: 16px;
  font-weight: 600;
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

.spacer {
  flex: 1;
}

.ops {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 16px;
}

.photos {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.photo {
  width: 96px;
  height: 96px;
  border-radius: 4px;
}

.files {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-top: 8px;
}

.file-link {
  color: var(--el-color-primary);
}

.remark {
  color: #909399;
}

.operator {
  margin-left: 8px;
  color: #909399;
  font-size: 12px;
}

.muted {
  color: #909399;
  font-size: 12px;
}
</style>
