<template>
  <div class="page">
    <el-card>
      <div class="toolbar">
        <el-button :icon="ArrowLeft" @click="back">返回</el-button>
        <span class="title">设备报修</span>
      </div>

      <!-- 扫码引导语仅在「尚未确定资产」或「资产可维修」时显示：
           资产不可维修时会改为下方警告，避免两句话自相矛盾 -->
      <el-alert
        v-if="!card || card.category_repairable"
        type="info"
        :closable="false"
        show-icon
        title="扫码即报修：资产信息自动带出，你只需填「哪儿坏了」"
      />

      <!-- 手动输入编码：无摄像头 / 手工造标签时同样能完成流程（P0-1 验收⑤） -->
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
          <el-button type="primary" :loading="loadingCard" @click="resolveByCode(codeForm.code)">查询资产</el-button>
        </el-form-item>
      </el-form>

      <el-empty v-if="!card" description="请扫描资产标签，或手动输入资产编码" />

      <template v-else>
        <el-descriptions :column="isMobile ? 1 : 2" border class="asset-box">
          <el-descriptions-item label="资产编码">{{ card.asset_code }}</el-descriptions-item>
          <el-descriptions-item label="资产名称">{{ card.name }}</el-descriptions-item>
          <el-descriptions-item label="资产类别">{{ card.category_name }}</el-descriptions-item>
          <el-descriptions-item label="使用部门">{{ card.use_dept_name || "-" }}</el-descriptions-item>
          <el-descriptions-item label="使用人">{{ card.user_emp_name || "-" }}</el-descriptions-item>
          <el-descriptions-item label="存放地点">{{ card.location || "-" }}</el-descriptions-item>
          <el-descriptions-item label="维保供应商">{{ card.mt_vendor_name || "-" }}</el-descriptions-item>
          <el-descriptions-item label="当前状态">
            <el-tag :type="STATUS_TAG[card.display_status] || 'info'" size="small">
              {{ card.display_status }}
            </el-tag>
          </el-descriptions-item>
        </el-descriptions>

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
            <el-select v-model="form.urgency" style="width: 200px">
              <el-option
                v-for="u in REPAIR_URGENCIES"
                :key="u"
                :label="REPAIR_URGENCY_LABELS[u]"
                :value="u"
              />
            </el-select>
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
            <el-button type="primary" :loading="submitting" @click="submit">提交报修</el-button>
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
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage } from "element-plus";
import { ArrowLeft, Plus } from "@element-plus/icons-vue";
import http from "../../api/client";
import {
  STATUS_TAG,
  REPAIR_URGENCIES,
  REPAIR_URGENCY_LABELS,
} from "../../api/meta";
import { useIsMobile } from "../../composables/useIsMobile";
import { takeAssetCode } from "../../utils/deeplink";

const route = useRoute();
const router = useRouter();
const isMobile = useIsMobile();

const card = ref<any>(null);
const loadingCard = ref(false);
const submitting = ref(false);
const fileList = ref<any[]>([]);
const formRef = ref();
const codeForm = reactive({ code: "" });
const form = reactive({ fault_desc: "", urgency: "normal" });

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
  } catch {
    // 拦截器已弹出后端的「资产编码不存在：X」，这里不再重复
    card.value = null;
  } finally {
    loadingCard.value = false;
  }
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
  await formRef.value.validate();
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
  const code = String(route.query.asset_code || "").trim() || takeAssetCode();
  if (code) resolveByCode(code);
});
</script>

<style scoped>
.title {
  font-size: 16px;
  font-weight: 600;
}

.code-bar {
  margin-top: 16px;
}

.asset-box {
  margin: 12px 0 20px;
}

.report-form {
  max-width: 720px;
}

.muted {
  color: #909399;
  font-size: 12px;
  margin-top: 4px;
}
</style>
