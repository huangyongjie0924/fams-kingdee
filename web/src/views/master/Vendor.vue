<template>
  <div class="page">
    <el-card>
      <el-tabs v-model="tab">
        <el-tab-pane label="供应商" name="vendor">
          <div class="toolbar">
            <span class="spacer" />
            <el-button v-if="auth.can('master.manage')" type="primary" :icon="Plus" @click="openVendor()">新增供应商</el-button>
          </div>
          <el-table v-if="!isMobile" :data="vendors" border>
            <el-table-column prop="name" label="供应商名称" min-width="200" />
            <el-table-column prop="code" label="编码" width="120" />
            <el-table-column prop="contact" label="联系人" width="120" />
            <el-table-column prop="phone" label="联系方式" width="150" />
            <el-table-column prop="remark" label="备注" min-width="160" show-overflow-tooltip />
            <el-table-column v-if="auth.can('master.manage')" label="操作" width="140" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="openVendor(row)">编辑</el-button>
                <el-button link type="danger" @click="remove('vendors', row, loadVendors)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>

          <div v-else class="m-list">
            <div v-for="row in vendors" :key="row.id" class="m-card">
              <div class="m-card-hd">
                <span class="m-title">{{ row.name }}</span>
              </div>
              <div class="m-grid">
                <span class="k">编码</span><span>{{ row.code || "-" }}</span>
                <span class="k">联系人</span><span>{{ row.contact || "-" }}</span>
                <span class="k">联系方式</span><span>{{ row.phone || "-" }}</span>
                <span v-if="row.remark" class="k">备注</span><span v-if="row.remark">{{ row.remark }}</span>
              </div>
              <div v-if="auth.can('master.manage')" class="m-tree-ops">
                <el-button link type="primary" size="small" @click="openVendor(row)">编辑</el-button>
                <el-button link type="danger" size="small" @click="remove('vendors', row, loadVendors)">删除</el-button>
              </div>
            </div>
            <el-empty v-if="!vendors.length" description="暂无供应商" />
          </div>
        </el-tab-pane>

        <el-tab-pane label="标签" name="tag">
          <div class="toolbar">
            <span class="spacer" />
            <el-button v-if="auth.can('master.manage')" type="primary" :icon="Plus" @click="openTag()">新增标签</el-button>
          </div>
          <el-table v-if="!isMobile" :data="tags" border>
            <el-table-column label="标签" width="200">
              <template #default="{ row }">
                <el-tag :color="row.color || undefined" size="small">{{ row.name }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="color" label="颜色" width="140" />
            <el-table-column v-if="auth.can('master.manage')" label="操作" width="140" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="openTag(row)">编辑</el-button>
                <el-button link type="danger" @click="remove('tags', row, loadTags)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>

          <div v-else class="m-list">
            <div v-for="row in tags" :key="row.id" class="m-card">
              <div class="m-card-hd">
                <el-tag :color="row.color || undefined" size="small">{{ row.name }}</el-tag>
                <span class="m-muted">{{ row.color || "无颜色" }}</span>
              </div>
              <div v-if="auth.can('master.manage')" class="m-tree-ops">
                <el-button link type="primary" size="small" @click="openTag(row)">编辑</el-button>
                <el-button link type="danger" size="small" @click="remove('tags', row, loadTags)">删除</el-button>
              </div>
            </div>
            <el-empty v-if="!tags.length" description="暂无标签" />
          </div>
        </el-tab-pane>
      </el-tabs>
    </el-card>

    <el-dialog v-model="vendorDialog" :title="vendor.id ? '编辑供应商' : '新增供应商'" width="440px" :fullscreen="isMobile">
      <el-form :model="vendor" :label-width="isMobile ? 'auto' : '100px'" :label-position="isMobile ? 'top' : 'right'">
        <el-form-item label="供应商名称"><el-input v-model="vendor.name" /></el-form-item>
        <el-form-item label="编码"><el-input v-model="vendor.code" /></el-form-item>
        <el-form-item label="联系人"><el-input v-model="vendor.contact" /></el-form-item>
        <el-form-item label="联系方式"><el-input v-model="vendor.phone" /></el-form-item>
        <el-form-item label="备注"><el-input v-model="vendor.remark" type="textarea" :rows="2" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="vendorDialog = false">取消</el-button>
        <el-button type="primary" @click="saveVendor">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="tagDialog" :title="tag_.id ? '编辑标签' : '新增标签'" width="380px" :fullscreen="isMobile">
      <el-form :model="tag_" :label-width="isMobile ? 'auto' : '80px'" :label-position="isMobile ? 'top' : 'right'">
        <el-form-item label="名称"><el-input v-model="tag_.name" /></el-form-item>
        <el-form-item label="颜色"><el-color-picker v-model="tag_.color" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="tagDialog = false">取消</el-button>
        <el-button type="primary" @click="saveTag">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Plus } from "@element-plus/icons-vue";
import http from "../../api/client";
import { auth } from "../../stores/auth";
import { useIsMobile } from "../../composables/useIsMobile";

const isMobile = useIsMobile();
const tab = ref("vendor");
const vendors = ref<any[]>([]);
const tags = ref<any[]>([]);
const vendorDialog = ref(false);
const tagDialog = ref(false);
const vendor = ref<any>({});
const tag_ = ref<any>({});

async function loadVendors() {
  const { data } = await http.get("/vendors");
  vendors.value = data || [];
}

async function loadTags() {
  const { data } = await http.get("/tags");
  tags.value = data || [];
}

function openVendor(row?: any) {
  vendor.value = row ? { ...row } : { id: 0, name: "", code: "", contact: "", phone: "", remark: "" };
  vendorDialog.value = true;
}

function openTag(row?: any) {
  tag_.value = row ? { ...row } : { id: 0, name: "", color: "" };
  tagDialog.value = true;
}

async function saveVendor() {
  await http.post("/vendors", vendor.value);
  vendorDialog.value = false;
  ElMessage.success("已保存");
  loadVendors();
}

async function saveTag() {
  await http.post("/tags", { ...tag_.value, color: tag_.value.color || "" });
  tagDialog.value = false;
  ElMessage.success("已保存");
  loadTags();
}

async function remove(path: string, row: any, reload: () => Promise<void>) {
  await ElMessageBox.confirm(`确认删除「${row.name}」？被资产引用的记录不允许删除。`, "提示", { type: "warning" });
  await http.delete(`/${path}/${row.id}`);
  ElMessage.success("已删除");
  await reload();
}

onMounted(async () => {
  await Promise.all([loadVendors(), loadTags()]);
});
</script>
