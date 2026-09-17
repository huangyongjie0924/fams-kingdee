<template>
  <div class="page">
    <div class="toolbar">
      <el-button :icon="ArrowLeft" @click="back">返回</el-button>
      <span class="title">{{ isEdit ? "编辑资产" : "新增资产" }}</span>
      <span class="spacer" />
      <el-button type="primary" :loading="saving" @click="save">保存</el-button>
    </div>

    <el-card>
      <el-form
        ref="formRef"
        :model="form"
        :rules="rules"
        :label-width="isMobile ? 'auto' : '120px'"
        :label-position="isMobile ? 'top' : 'right'"
      >
        <el-tabs v-model="tab">
          <el-tab-pane label="基本信息" name="basic">
            <el-row :gutter="16">
              <el-col :span="8">
                <el-form-item label="资产编码">
                  <el-input v-model="form.asset_code" :placeholder="codePreview" :disabled="!isEdit" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="资产名称" prop="name">
                  <el-input v-model="form.name" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="资产类别" prop="category_id">
                  <el-tree-select
                    v-model="form.category_id"
                    :data="md.categories"
                    :props="treeProps"
                    node-key="id"
                    check-strictly
                    style="width: 100%"
                    @change="onCategoryChange"
                  />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="规格型号">
                  <el-input v-model="form.spec" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="设备序列号">
                  <el-input v-model="form.serial_no" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="计量单位">
                  <el-input v-model="form.unit" placeholder="台 / 把 / 张" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <!-- 数量：金蝶同步来的卡由星瀚托管，改了也会被下次同步覆盖，所以置灰只读；
                     手工新建的卡不在金蝶里，可以自己填 -->
                <el-form-item label="数量">
                  <el-input-number
                    v-if="!form.synced"
                    v-model="form.quantity"
                    :min="0"
                    :precision="4"
                    :controls="false"
                    style="width: 100%"
                  />
                  <el-input
                    v-else
                    :model-value="qty(form.quantity)"
                    disabled
                    title="由金蝶同步，不可编辑"
                  />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="状态">
                  <el-select v-model="form.status" style="width: 100%">
                    <el-option v-for="s in md.enums.statuses" :key="s" :label="s" :value="s" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="金额">
                  <el-input-number v-model="form.amount" :min="0" :precision="2" :controls="false" style="width: 100%" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="来源">
                  <el-select v-model="form.source" clearable style="width: 100%">
                    <el-option v-for="s in sourceOptions" :key="s" :label="s" :value="s" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="所属公司">
                  <el-select v-model="form.owner_company_id" clearable style="width: 100%">
                    <el-option v-for="c in md.companies" :key="c.id" :label="c.name" :value="c.id" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="使用公司">
                  <el-select v-model="form.use_company_id" clearable style="width: 100%">
                    <el-option v-for="c in md.companies" :key="c.id" :label="c.name" :value="c.id" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="使用部门">
                  <el-select v-model="form.use_dept_id" clearable style="width: 100%">
                    <el-option v-for="d in md.departments" :key="d.id" :label="d.name" :value="d.id" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="使用人">
                  <el-select v-model="form.user_emp_id" clearable filterable style="width: 100%">
                    <el-option v-for="e in md.employees" :key="e.id" :label="e.name" :value="e.id" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="管理人">
                  <el-select v-model="form.manager_emp_id" clearable filterable style="width: 100%">
                    <el-option v-for="e in md.employees" :key="e.id" :label="e.name" :value="e.id" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="使用状态">
                  <el-input v-model="form.use_status" placeholder="由金蝶同步，可本地修正" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="区域">
                  <el-tree-select
                    v-model="form.area_id"
                    :data="md.areas"
                    :props="treeProps"
                    node-key="id"
                    check-strictly
                    clearable
                    style="width: 100%"
                  />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="存放地点">
                  <el-input v-model="form.location" placeholder="6楼 / 208机房" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="购入日期">
                  <DateSelect v-if="isMobile" v-model="form.purchase_date" />
                  <el-date-picker v-else v-model="form.purchase_date" type="date" value-format="YYYY-MM-DD" style="width: 100%" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="使用期限(月)">
                  <el-input-number v-model="form.use_months" :min="0" :controls="false" style="width: 100%" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="入库/收货单号">
                  <el-input v-model="form.in_stock_no" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="RFID">
                  <el-input v-model="form.rfid" />
                </el-form-item>
              </el-col>
              <el-col :span="24">
                <el-form-item label="标签">
                  <el-select v-model="form.tag_ids" multiple clearable style="width: 100%" placeholder="可多选">
                    <el-option v-for="t in md.tags" :key="t.id" :label="t.name" :value="t.id" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="24">
                <el-form-item label="备注">
                  <el-input v-model="form.remark" type="textarea" :rows="2" maxlength="500" show-word-limit />
                </el-form-item>
              </el-col>
              <el-col :span="24">
                <el-form-item label="照片">
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
                </el-form-item>
              </el-col>


            </el-row>
          </el-tab-pane>

          <el-tab-pane label="财务信息" name="fin">
            <!-- 金蝶同步来的卡：金额以星瀚为准，改了下次同步就没了，所以置灰只读；
                 手工新建的卡不在金蝶里，自己维护 -->
            <el-alert
              v-if="form.synced"
              type="info"
              :closable="false"
              show-icon
              class="fin-tip"
              title="原值 / 累计折旧 / 净值 由星瀚同步维护"
            >
              <template #default>
                这三个数取自星瀚资产卡的财务信息明细，每次同步都会以星瀚为准覆盖，因此不可手工修改。
                <b>净值由星瀚给出</b>，与「原值 − 累计折旧」一致。
              </template>
            </el-alert>
            <el-alert
              v-else
              type="info"
              :closable="false"
              show-icon
              class="fin-tip"
              title="原值 / 累计折旧 / 净值 由台账人工维护"
            >
              <template #default>
                手工建的卡不在金蝶里，这三个数自己填。
                <b>净值自动按「原值 − 累计折旧」计算</b>，不用手填。
              </template>
            </el-alert>
            <el-row :gutter="16">
              <el-col :span="8">
                <el-form-item label="资产类型">
                  <el-select
                    v-model="form.fin_asset_type"
                    clearable
                    filterable
                    allow-create
                    default-first-option
                    style="width: 100%"
                  >
                    <el-option v-for="t in finAssetTypeOptions" :key="t" :label="t" :value="t" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="分摊部门">
                  <el-select v-model="form.fin_share_dept_id" clearable style="width: 100%">
                    <el-option v-for="d in md.departments" :key="d.id" :label="d.name" :value="d.id" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="供应商">
                  <el-select v-model="form.vendor_id" clearable filterable style="width: 100%">
                    <el-option v-for="v in md.vendors" :key="v.id" :label="v.name" :value="v.id" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="含税金额">
                  <el-input-number v-model="form.fin_amount_with_tax" :min="0" :precision="2" :controls="false" style="width: 100%" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="税额">
                  <el-input-number v-model="form.fin_tax" :min="0" :precision="2" :controls="false" style="width: 100%" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="原值">
                  <el-input-number
                    v-if="!form.synced"
                    v-model="form.fin_original_value"
                    :min="0"
                    :precision="2"
                    :controls="false"
                    style="width: 100%"
                  />
                  <el-input
                    v-else
                    :model-value="money(form.fin_original_value)"
                    disabled
                    title="由金蝶同步，不可编辑"
                  />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="累计折旧">
                  <el-input-number
                    v-if="!form.synced"
                    v-model="form.fin_accum_depreciation"
                    :min="0"
                    :precision="2"
                    :controls="false"
                    style="width: 100%"
                  />
                  <el-input
                    v-else
                    :model-value="money(form.fin_accum_depreciation)"
                    disabled
                    title="由金蝶同步，不可编辑"
                  />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="净值">
                  <el-input :model-value="money(form.fin_net_value)" disabled />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="残值率(%)">
                  <el-input-number
                    v-if="!form.synced"
                    v-model="form.fin_residual_rate"
                    :min="0"
                    :max="100"
                    :precision="2"
                    :controls="false"
                    style="width: 100%"
                  />
                  <el-input
                    v-else
                    :model-value="String(form.fin_residual_rate ?? '')"
                    disabled
                    title="由金蝶同步，不可编辑"
                  />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="财务使用期限">
                  <el-input-number
                    v-if="!form.synced"
                    v-model="form.fin_use_months"
                    :min="0"
                    :controls="false"
                    style="width: 100%"
                  />
                  <el-input
                    v-else
                    :model-value="String(form.fin_use_months ?? '')"
                    disabled
                    title="由金蝶同步，不可编辑"
                  />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="入账期间">
                  <DateSelect v-if="isMobile" v-model="form.fin_period" type="month" />
                  <el-date-picker v-else v-model="form.fin_period" type="month" value-format="YYYY-MM" style="width: 100%" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="入账时间">
                  <DateSelect v-if="isMobile" v-model="form.fin_entry_date" />
                  <el-date-picker v-else v-model="form.fin_entry_date" type="date" value-format="YYYY-MM-DD" style="width: 100%" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="财务信息状态">
                  <el-select v-model="form.fin_status" style="width: 100%">
                    <el-option v-for="s in md.enums.fin_status" :key="s" :label="s" :value="s" />
                  </el-select>
                </el-form-item>
              </el-col>
            </el-row>
          </el-tab-pane>

          <el-tab-pane label="维保信息" name="mt">
            <el-row :gutter="16">
              <el-col :span="8">
                <el-form-item label="维保供应商">
                  <el-select v-model="form.mt_vendor_id" clearable filterable style="width: 100%">
                    <el-option v-for="v in md.vendors" :key="v.id" :label="v.name" :value="v.id" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="供应商联系人">
                  <el-input v-model="form.mt_contact" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="联系方式">
                  <el-input v-model="form.mt_phone" />
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="负责人">
                  <el-select v-model="form.mt_owner_emp_id" clearable filterable style="width: 100%">
                    <el-option v-for="e in md.employees" :key="e.id" :label="e.name" :value="e.id" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :span="8">
                <el-form-item label="维保到期时间">
                  <DateSelect v-if="isMobile" v-model="form.mt_expire_date" />
                  <el-date-picker v-else v-model="form.mt_expire_date" type="date" value-format="YYYY-MM-DD" style="width: 100%" />
                </el-form-item>
              </el-col>
              <el-col :span="24">
                <el-form-item label="维保说明">
                  <el-input v-model="form.mt_remark" type="textarea" :rows="2" maxlength="500" show-word-limit />
                </el-form-item>
              </el-col>

            </el-row>
          </el-tab-pane>
        </el-tabs>
      </el-form>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage } from "element-plus";
import { ArrowLeft, Plus } from "@element-plus/icons-vue";
import http from "../../api/client";
import { money, pickSubmit, qty } from "../../api/meta";
import { useIsMobile } from "../../composables/useIsMobile";
import DateSelect from "../../components/DateSelect.vue";

const route = useRoute();
const router = useRouter();
const isMobile = useIsMobile();
const treeProps = { label: "name", children: "children" };

const isEdit = computed(() => !!route.params.id);
const tab = ref("basic");
const saving = ref(false);
const formRef = ref();
const codePreview = ref("保存后自动生成");
const fileList = ref<any[]>([]);

const md = ref<any>({
  enums: { statuses: [], sources: [], fin_asset_type: [], fin_status: [] },
  categories: [], areas: [], companies: [], departments: [], employees: [], vendors: [], tags: [],
});

const form = ref<Record<string, any>>({
  asset_code: "", name: "", category_id: null, spec: "", serial_no: "", unit: "", quantity: 0,
  status: "闲置", amount: 0, use_company_id: null, use_dept_id: null, user_emp_id: null,
  use_status: "", manager_emp_id: null, owner_company_id: null, area_id: null, location: "",
  purchase_date: "", use_months: 0, source: "", in_stock_no: "", rfid: "", remark: "",
  fin_asset_type: "", fin_share_dept_id: null, vendor_id: null, fin_amount_with_tax: 0,
  fin_tax: 0, fin_original_value: 0, fin_net_value: 0, fin_accum_depreciation: 0,
  fin_residual_rate: 0, fin_use_months: 0, fin_period: "", fin_entry_date: "", fin_status: "未入账",
  mt_vendor_id: null, mt_contact: "", mt_phone: "", mt_owner_emp_id: null,
  mt_expire_date: "", mt_remark: "", tag_ids: [],
});

const rules = {
  name: [{ required: true, message: "资产名称必填", trigger: "blur" }],
  category_id: [{ required: true, message: "资产类别必填", trigger: "change" }],
};

// 「金蝶同步」是系统写入的来源值，不在人工可选的枚举里。同步卡打开编辑时必须把它
// 作为一个选项挂上去，否则下拉框显示空白，保存还会被后端按「来源取值非法」挡回来。
// 资产类型同理：同步填的是金蝶资产类别名（如「房屋及建筑物」），也不在枚举里。
// 做法统一成「把当前值补进候选列表」，不动枚举本身。
function withCurrent(list: string[], cur: string): string[] {
  return cur && !list.includes(cur) ? [...list, cur] : list;
}

const sourceOptions = computed(() =>
  withCurrent(md.value.enums.sources || [], form.value.source),
);

const finAssetTypeOptions = computed(() =>
  withCurrent(md.value.enums.fin_asset_type || [], form.value.fin_asset_type),
);

// 净值 = 原值 - 累计折旧，与后端 validateCard 的兜底口径一致
watch(
  () => [form.value.fin_original_value, form.value.fin_accum_depreciation],
  ([orig, accum]) => {
    form.value.fin_net_value = Number(((orig || 0) - (accum || 0)).toFixed(2));
  },
);

async function onCategoryChange(id: number) {
  const node = findNode(md.value.categories, id);
  if (node) {
    if (!form.value.use_months) form.value.use_months = node.use_months || 0;
    if (!form.value.fin_residual_rate) form.value.fin_residual_rate = node.residual_rate || 0;
    if (!form.value.fin_use_months) form.value.fin_use_months = node.use_months || 0;
  }
  if (!isEdit.value) {
    const { data } = await http.get("/code-rule/next", { params: { category_id: id || 0 } });
    codePreview.value = `${data.asset_code}（保存后自动生成）`;
  }
}

function findNode(nodes: any[], id: number): any {
  for (const n of nodes || []) {
    if (n.id === id) return n;
    const hit = findNode(n.children, id);
    if (hit) return hit;
  }
  return null;
}

const MAX_PHOTO_MB = 10;

function beforePhoto(file: File) {
  if (file.size > MAX_PHOTO_MB * 1024 * 1024) {
    ElMessage.error(`单张照片不能超过 ${MAX_PHOTO_MB}MB`);
    return false;
  }
  return true;
}

// 返回值会被 el-upload 挂到 file.response 上，保存时据此收集新上传的附件 id
async function uploadPhoto(options: any) {
  const fd = new FormData();
  fd.append("file", options.file);
  const { data } = await http.post("/upload", fd);
  return data;
}

// 已存在的照片（编辑时加载出来的）带 attId，移除时要真的删掉；新上传的只需从列表里去掉
async function onPhotoRemove(file: any) {
  const existingId = file.attId;
  if (existingId) {
    await http.delete(`/attachments/${existingId}`);
  }
}

function newAttachmentIds(): number[] {
  return fileList.value.map((f: any) => f.response?.id).filter((id: any) => !!id);
}

async function save() {
  await formRef.value.validate();
  saving.value = true;
  try {
    const payload = pickSubmit({ ...form.value, attachment_ids: newAttachmentIds() });
    if (isEdit.value) {
      await http.put(`/assets/${route.params.id}`, payload);
      ElMessage.success("已保存");
    } else {
      const { data } = await http.post("/assets", payload);
      ElMessage.success(`已新增，资产编码 ${data.asset_code}`);
    }
    router.push({ name: "assets" });
  } finally {
    saving.value = false;
  }
}

function back() {
  router.push({ name: "assets" });
}

onMounted(async () => {
  const [e, cat, area, co, dept, emp, ven, tag] = await Promise.all([
    http.get("/enums"), http.get("/categories"), http.get("/areas"), http.get("/companies"),
    http.get("/departments"), http.get("/employees"), http.get("/vendors"), http.get("/tags"),
  ]);
  md.value = {
    enums: e.data,
    categories: cat.data || [],
    areas: area.data || [],
    companies: co.data || [],
    departments: dept.data || [],
    employees: emp.data || [],
    vendors: ven.data || [],
    tags: tag.data || [],
  };

  if (isEdit.value) {
    const { data } = await http.get(`/assets/${route.params.id}`);
    for (const k of Object.keys(form.value)) {
      if (data[k] !== undefined && data[k] !== null) form.value[k] = data[k];
    }
    // synced 是派生字段、不在 form 的默认键里，得单独接一下：数量按它决定只读还是可填
    form.value.synced = !!data.synced;
    form.value.tag_ids = data.tag_ids || [];
    const { data: atts } = await http.get(`/assets/${route.params.id}/attachments`);
    fileList.value = (atts || [])
      .filter((a: any) => a.kind === "photo")
      .map((a: any) => ({ name: a.origin_name, url: a.url, status: "success", attId: a.id }));
  } else {
    await onCategoryChange(0);
  }
});
</script>

<style scoped>
.title {
  font-size: 15px;
  font-weight: 600;
}

/* 财务信息页签顶部那条「人工维护」说明，别贴到下面的表单上 */
.fin-tip {
  margin-bottom: 16px;
}
</style>
