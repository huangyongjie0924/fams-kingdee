<template>
  <div class="page">
    <el-card>
      <div class="toolbar">
        <el-button :icon="ArrowLeft" @click="back">返回</el-button>
        <span class="title">设备报修</span>
      </div>

      <!-- 三步式：选择资产 → 描述问题 → 确认提交（方案 §6.4）。
           真实场景是「人站在设备旁」，所以第一步里扫码排在搜索与列表之前。 -->
      <el-steps :active="step - 1" align-center class="steps">
        <el-step title="选择资产" description="扫码 / 输码 / 从列表选" />
        <el-step title="描述问题" description="哪儿坏了、多急" />
        <el-step title="确认提交" description="核对后提交" />
      </el-steps>

      <!-- ================= 第 1 步：选择资产 ================= -->
      <template v-if="step === 1">
        <!-- 扫码优先：站在设备旁时比在下拉框里找编码快一个量级 -->
        <div class="scan-first">
          <el-button type="primary" :icon="Camera" class="scan-btn" @click="goScan">
            扫码选设备
          </el-button>
          <span class="muted">没有摄像头也可以手动输入编码，或从下方列表里挑</span>
        </div>

        <el-form :model="codeForm" inline class="code-bar" @submit.prevent>
          <el-form-item label="资产编码">
            <el-input
              v-model="codeForm.code"
              placeholder="扫码自动带出，也可手动输入"
              clearable
              style="width: 240px"
              @keyup.enter="resolveByCode(codeForm.code)"
            />
          </el-form-item>
          <el-form-item>
            <el-button type="primary" :loading="loadingCard" @click="resolveByCode(codeForm.code)">
              查询资产
            </el-button>
          </el-form-item>
        </el-form>

        <div class="pick-box">
          <div class="pick-hd">
            <span class="pick-title">可报修设备</span>
            <span class="spacer" />
            <el-input
              v-model="listQuery.keyword"
              placeholder="编码/名称/使用人/地点"
              clearable
              :style="{ width: isMobile ? '100%' : '260px' }"
              @keyup.enter="loadRepairable(1)"
              @clear="loadRepairable(1)"
            >
              <template #prefix><el-icon><Search /></el-icon></template>
            </el-input>
            <el-button :icon="Search" @click="loadRepairable(1)">搜索</el-button>
          </div>

          <el-table v-if="!isMobile" v-loading="listLoading" :data="listRows" border max-height="420">
            <el-table-column prop="asset_code" label="资产编码" width="150" show-overflow-tooltip>
              <template #default="{ row }"><span class="u-mono">{{ row.asset_code }}</span></template>
            </el-table-column>
            <el-table-column prop="name" label="资产名称" min-width="160" show-overflow-tooltip />
            <el-table-column prop="category_name" label="资产类别" min-width="120" show-overflow-tooltip>
              <template #default="{ row }">{{ row.category_name || "-" }}</template>
            </el-table-column>
            <el-table-column prop="use_dept_name" label="使用部门" min-width="120" show-overflow-tooltip>
              <template #default="{ row }">{{ row.use_dept_name || "-" }}</template>
            </el-table-column>
            <el-table-column prop="location" label="存放地点" min-width="140" show-overflow-tooltip>
              <template #default="{ row }">{{ row.location || "-" }}</template>
            </el-table-column>
            <el-table-column prop="display_status" label="当前状态" width="110">
              <template #default="{ row }">
                <StatusChip :value="row.display_status || ''" :label="row.display_status || '-'" />
              </template>
            </el-table-column>
            <el-table-column label="操作" width="110" align="center">
              <template #default="{ row }">
                <el-button type="primary" link @click="pickRow(row)">报修这台</el-button>
              </template>
            </el-table-column>
          </el-table>

          <!-- 窄屏跟资产列表页保持一致：表格换卡片。每行都有操作按钮，卡片更好点，
               横滚表格会把「报修这台」挤到视口外 -->
          <div v-else v-loading="listLoading" class="m-list">
            <div v-for="row in listRows" :key="row.id" class="m-card">
              <div class="m-card-hd">
                <span class="m-title">{{ row.name }}</span>
                <StatusChip :value="row.display_status || ''" :label="row.display_status || '-'" />
              </div>
              <div class="m-grid">
                <span class="k">资产编码</span><span class="u-mono">{{ row.asset_code || "-" }}</span>
                <span class="k">资产类别</span><span>{{ row.category_name || "-" }}</span>
                <span class="k">使用部门</span><span>{{ row.use_dept_name || "-" }}</span>
                <span class="k">存放地点</span><span>{{ row.location || "-" }}</span>
              </div>
              <el-button type="primary" size="small" class="m-pick" @click="pickRow(row)">报修这台</el-button>
            </div>
          </div>

          <el-pagination
            v-model:current-page="listQuery.page"
            v-model:page-size="listQuery.page_size"
            :total="listTotal"
            :page-sizes="[20, 50, 100]"
            :layout="isMobile ? 'prev, pager, next' : 'total, sizes, prev, pager, next'"
            :pager-count="isMobile ? 5 : 7"
            class="pick-page"
            @change="loadRepairable()"
          />

          <!-- 列表也为空：说明此人的可见范围内确实没有可维修分类的资产 -->
          <el-empty
            v-if="!listRows.length && !listLoading"
            description="当前权限范围内没有可报修的设备（只有可维修分类的资产会出现在这里）"
          />
        </div>
      </template>

      <!-- ================= 第 2 步：描述问题 ================= -->
      <template v-else-if="step === 2">
        <!-- 选中后固定展示，随时确认「我报的是哪台」 -->
        <div class="picked">
          <div class="picked-hd">
            <span class="picked-title">{{ card.name }}</span>
            <StatusChip :value="card.display_status || ''" :label="card.display_status || '-'" />
            <span class="spacer" />
            <el-button size="small" @click="changeAsset">换一台</el-button>
          </div>
          <div class="picked-grid">
            <span class="k">资产编码</span><span class="u-mono">{{ card.asset_code }}</span>
            <span class="k">资产类别</span><span>{{ card.category_name }}</span>
            <span class="k">使用部门</span><span>{{ card.use_dept_name || "-" }}</span>
            <span class="k">使用人</span><span>{{ card.user_emp_name || "-" }}</span>
            <span class="k">存放地点</span><span>{{ card.location || "-" }}</span>
            <span class="k">维保供应商</span><span>{{ card.mt_vendor_name || "-" }}</span>
          </div>
        </div>

        <el-form
          ref="formRef"
          :model="form"
          :rules="rules"
          :label-width="isMobile ? 'auto' : '90px'"
          :label-position="isMobile ? 'top' : 'right'"
          class="report-form"
        >
          <el-form-item label="问题描述" prop="fault_desc">
            <el-input
              v-model="form.fault_desc"
              type="textarea"
              :rows="3"
              maxlength="500"
              show-word-limit
              placeholder="例如：开机后不制冷、屏幕闪烁、有异响"
            />
          </el-form-item>
          <el-form-item label="紧急程度">
            <!-- 分段控件而非下拉：三项一眼可比（选项仍是后端枚举的 low/normal/high，未新增值） -->
            <el-radio-group v-model="form.urgency" class="seg">
              <el-radio-button v-for="u in REPAIR_URGENCIES" :key="u" :value="u">
                {{ REPAIR_URGENCY_LABELS[u] }}
              </el-radio-button>
            </el-radio-group>
          </el-form-item>
          <el-form-item label="现场照片">
            <el-upload
              v-model:file-list="fileList"
              :http-request="uploadPhoto"
              :before-upload="beforePhoto"
              :on-remove="onPhotoRemove"
              accept=".jpg,.jpeg,.png"
              list-type="picture-card"
            >
              <el-icon><Plus /></el-icon>
            </el-upload>
            <div class="muted">可选，最多 3 张，单张 ≤ 10MB</div>
          </el-form-item>

          <el-form-item v-if="card.category_repairable">
            <el-button type="primary" @click="toConfirm">下一步：确认提交</el-button>
          </el-form-item>
          <el-alert
            v-else
            type="warning"
            :closable="false"
            show-icon
            title="该资产所属分类不支持报修，请联系资产管理员"
            description="只有设备类（可维修分类）的资产才能提交维修单。"
          />
        </el-form>
      </template>

      <!-- ================= 第 3 步：确认提交 ================= -->
      <template v-else>
        <el-descriptions :column="isMobile ? 1 : 2" border class="confirm-box">
          <el-descriptions-item label="报修设备">
            {{ card.name }}
            <span class="u-mono muted">（{{ card.asset_code }}）</span>
          </el-descriptions-item>
          <el-descriptions-item label="当前状态">
            <StatusChip :value="card.display_status || ''" :label="card.display_status || '-'" />
          </el-descriptions-item>
          <el-descriptions-item label="使用部门">{{ card.use_dept_name || "-" }}</el-descriptions-item>
          <el-descriptions-item label="存放地点">{{ card.location || "-" }}</el-descriptions-item>
          <el-descriptions-item label="紧急程度">
            <b>{{ REPAIR_URGENCY_LABELS[form.urgency] }}</b>
          </el-descriptions-item>
          <el-descriptions-item label="现场照片">{{ fileList.length }} 张</el-descriptions-item>
          <el-descriptions-item label="问题描述" :span="2">{{ form.fault_desc }}</el-descriptions-item>
        </el-descriptions>

        <div class="confirm-ops">
          <el-button @click="step = 2">上一步</el-button>
          <!-- loading 必须锁死：报修提交会触发云之家待办推送（同步调用），
               网络慢时用户容易连点，产生重复单据 -->
          <el-button type="primary" :loading="submitting" :disabled="submitting" @click="submit">
            {{ submitting ? "提交中…" : "提交报修" }}
          </el-button>
        </div>
      </template>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage } from "element-plus";
import { ArrowLeft, Camera, Plus, Search } from "@element-plus/icons-vue";
import http from "../../api/client";
import { REPAIR_URGENCIES, REPAIR_URGENCY_LABELS } from "../../api/meta";
import { useIsMobile } from "../../composables/useIsMobile";
import { takeAssetCode } from "../../utils/deeplink";
import StatusChip from "../../components/StatusChip.vue";

const route = useRoute();
const router = useRouter();
const isMobile = useIsMobile();

const step = ref(1);
const card = ref<any>(null);
const loadingCard = ref(false);
const submitting = ref(false);
const fileList = ref<any[]>([]);
const formRef = ref();
const codeForm = reactive({ code: "" });
const form = reactive({ fault_desc: "", urgency: "normal" });

// 可报修设备列表：repairable=1 让服务端只回可维修分类，可见范围由服务端 assetScope 收窄，
// 前端不再做任何角色判断（否则就是第二套规则）
const listRows = ref<any[]>([]);
const listTotal = ref(0);
const listLoading = ref(false);
const listQuery = reactive({ keyword: "", page: 1, page_size: 20 });

const rules = {
  fault_desc: [{ required: true, message: "请描述设备的问题", trigger: "blur" }],
};

const MAX_PHOTO_MB = 10;
const MAX_PHOTOS = 3;

function beforePhoto(file: File) {
  if (file.size > MAX_PHOTO_MB * 1024 * 1024) {
    ElMessage.error(`单张照片不能超过 ${MAX_PHOTO_MB}MB`);
    return false;
  }
  if (fileList.value.length >= MAX_PHOTOS) {
    ElMessage.error(`最多上传 ${MAX_PHOTOS} 张照片`);
    return false;
  }
  return true;
}

// 报修时照片先于单据存在：repair_id=0 暂存，提交时由后端回填（两段式上传）
async function uploadPhoto(options: any) {
  const fd = new FormData();
  fd.append("file", options.file);
  fd.append("card_id", String(card.value?.id || 0));
  fd.append("repair_id", "0");
  const { data } = await http.post("/repair-upload", fd);
  return data;
}

function onPhotoRemove() {
  // 预上传的附件尚未绑定单据，从列表移除即可，无需删库（提交时只回填仍在列表里的 id）
}

function attachmentIds(): number[] {
  return fileList.value.map((f: any) => f.response?.id).filter((id: any) => !!id);
}

// 列表请求失败不在这里弹错：api/client 的响应拦截器已经统一弹了后端的 message
async function loadRepairable(page?: number) {
  if (page) listQuery.page = page;
  listLoading.value = true;
  try {
    const { data } = await http.get("/assets", {
      params: {
        repairable: 1,
        keyword: listQuery.keyword,
        page: listQuery.page,
        page_size: listQuery.page_size,
      },
    });
    listRows.value = data.items || [];
    listTotal.value = data.total || 0;
  } finally {
    listLoading.value = false;
  }
}

// 先用列表里的 AssetCard 顶上，界面立刻切到表单；再静默补一发 by-code，
// 把列表不带的 mt_vendor_name 等字段补全（列表接口不返回它，不补会显示“-”）
async function pickRow(row: any) {
  const code = row.asset_code || "";
  card.value = row;
  codeForm.code = code;
  step.value = 2;
  if (!code) return;
  try {
    const { data } = await http.get("/assets/by-code", { params: { code } });
    // 用户可能已经点了「换一台」，别用迟到的响应覆盖新的选择
    if (card.value?.id === row.id) card.value = data;
  } catch {
    // 拦截器已弹错；列表数据本身够提交报修，静默沿用即可
  }
}

function changeAsset() {
  card.value = null;
  codeForm.code = "";
  step.value = 1;
  // 重新拉一遍，拿到的状态是最新的（可能刚报修过一台）
  loadRepairable(listQuery.page);
}

// 带 to=repair-new：扫到码后扫码页会带着 asset_code 回到本页，而不是跳资产列表
function goScan() {
  router.push({ name: "scan", query: { to: "repair-new" } });
}

async function resolveByCode(code: string) {
  const c = String(code || "").trim();
  if (!c) {
    ElMessage.warning("请输入资产编码");
    return;
  }
  loadingCard.value = true;
  try {
    const { data } = await http.get("/assets/by-code", { params: { code: c } });
    card.value = data;
    codeForm.code = c;
    step.value = 2;
  } catch {
    // 拦截器已弹出后端的「资产编码不存在：X」，这里不再重复
    card.value = null;
  } finally {
    loadingCard.value = false;
  }
}

// 第 2 步 → 第 3 步前先过表单校验，避免用户到确认页才发现描述是空的
async function toConfirm() {
  await formRef.value.validate();
  step.value = 3;
}

async function submit() {
  if (!card.value) {
    ElMessage.warning("请先确定要报修的资产");
    return;
  }
  // 前端兜底：按钮已隐藏，但直连接口 / 状态竞争仍可能走到这里；后端才是最终底线
  if (!card.value.category_repairable) {
    ElMessage.warning("该资产所属分类不支持报修，请联系资产管理员");
    return;
  }
  if (submitting.value) return;
  // ⚠️ 这里**不能**无条件 `formRef.value.validate()`：表单住在 step===2 的分支里，
  // 到第 3 步时 el-form 已被卸载，formRef 变成 null → 抛 TypeError，
  // 请求根本发不出去（用户看到「点提交没反应」）。校验在第 2 步 → 第 3 步的
  // toConfirm() 里已经做过，第 3 步是纯只读核对页、没有任何可编辑项，
  // 保留这一句只是防御将来模板结构再变；用存在性守卫包住。
  if (formRef.value) await formRef.value.validate();
  submitting.value = true;
  try {
    const { data } = await http.post("/repairs", {
      card_id: card.value.id,
      fault_desc: form.fault_desc,
      urgency: form.urgency,
      attachment_ids: attachmentIds(),
    });
    ElMessage.success(`报修已提交，单号 ${data.code}`);
    router.replace({ name: "repair-detail", params: { id: data.id } });
  } finally {
    submitting.value = false;
  }
}

function back() {
  router.push({ name: "assets" });
}

onMounted(() => {
  // 列表无条件先拉：扫码直接带出资产、或稍后点「换一台」时都已就绪
  loadRepairable();
  const code = String(route.query.asset_code || "").trim() || takeAssetCode();
  if (code) resolveByCode(code);
});
</script>

<style scoped>
.title {
  font-size: var(--t-20);
  font-weight: 600;
  color: var(--ink);
}

.steps {
  margin: 20px 0 8px;
}

.scan-first {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin-top: 16px;
}

.scan-btn {
  min-width: 160px;
}

.code-bar {
  margin-top: 16px;
}

.pick-box {
  margin-top: 16px;
}

.pick-hd {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}

.spacer {
  flex: 1;
}

.pick-title {
  font-size: var(--t-14);
  font-weight: 600;
  color: var(--ink);
}

.pick-page {
  margin-top: 12px;
  justify-content: flex-end;
}

/* —— 第 2 步：已选资产固定卡片 —— */
.picked {
  margin: 16px 0 20px;
  padding: 12px 14px;
  background: var(--steel-100);
  border: var(--hairline);
  border-left: 3px solid var(--signal);
  border-radius: var(--r);
}

.picked-hd {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}

.picked-title {
  font-size: var(--t-16);
  font-weight: 600;
  color: var(--ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.picked-grid {
  display: grid;
  grid-template-columns: 84px 1fr 84px 1fr;
  gap: 4px 10px;
  font-size: var(--t-13);
  color: var(--steel-700);
}

.picked-grid .k {
  color: var(--steel-500);
}

.report-form {
  max-width: 720px;
}

/* 分段控件：硬边 + 2px 圆角 */
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

.confirm-box {
  margin: 16px 0 20px;
}

.confirm-ops {
  display: flex;
  gap: 8px;
}

.muted {
  color: var(--steel-500);
  font-size: var(--t-12);
  margin-top: 4px;
}

.m-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.m-card {
  border: var(--hairline);
  border-radius: var(--r);
  padding: 10px 12px;
}

.m-card-hd {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 6px;
}

.m-title {
  font-size: var(--t-14);
  font-weight: 600;
  color: var(--ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.m-grid {
  display: grid;
  grid-template-columns: 72px 1fr;
  gap: 4px 8px;
  font-size: var(--t-13);
  color: var(--steel-700);
}

.m-grid .k {
  color: var(--steel-500);
}

.m-pick {
  margin-top: 8px;
  width: 100%;
}

@media (max-width: 767px), (max-height: 520px) {
  .picked-grid {
    grid-template-columns: 72px 1fr;
  }
}
</style>
