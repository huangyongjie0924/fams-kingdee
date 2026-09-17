<template>
  <el-drawer :model-value="modelValue" title="高级搜索" :size="isMobile ? '100%' : '420px'" @update:model-value="emit('update:modelValue', $event)">
    <el-form :label-width="isMobile ? 'auto' : '96px'" :label-position="isMobile ? 'top' : 'right'">
      <el-form-item label="状态">
        <el-select v-model="query.status" multiple clearable placeholder="全部" style="width: 100%">
          <el-option v-for="s in enums.statuses" :key="s" :label="s" :value="s" />
        </el-select>
      </el-form-item>
      <el-form-item label="资产编码">
        <el-input v-model="query.asset_code" clearable placeholder="支持模糊匹配" />
      </el-form-item>
      <el-form-item label="资产名称">
        <el-input v-model="query.name" clearable />
      </el-form-item>
      <el-form-item label="资产类别">
        <el-tree-select
          v-model="query.category_id"
          :data="categories"
          :props="treeProps"
          node-key="id"
          check-strictly
          clearable
          placeholder="全部"
          style="width: 100%"
        />
      </el-form-item>
      <el-form-item label="区域">
        <el-tree-select
          v-model="query.area_id"
          :data="areas"
          :props="treeProps"
          node-key="id"
          check-strictly
          clearable
          placeholder="全部"
          style="width: 100%"
        />
      </el-form-item>
      <el-form-item label="使用公司">
        <el-select v-model="query.use_company_id" clearable placeholder="全部" style="width: 100%">
          <el-option v-for="c in companies" :key="c.id" :label="c.name" :value="c.id" />
        </el-select>
      </el-form-item>
      <el-form-item label="使用部门">
        <el-select v-model="query.use_dept_id" clearable placeholder="全部" style="width: 100%">
          <el-option v-for="d in departments" :key="d.id" :label="d.name" :value="d.id" />
        </el-select>
      </el-form-item>
      <el-form-item label="使用人">
        <el-input v-model="query.user_name" clearable placeholder="支持模糊匹配" />
      </el-form-item>
      <el-form-item label="存放地点">
        <el-input v-model="query.location" clearable placeholder="如 6楼 / 208机房，支持模糊匹配" />
      </el-form-item>
      <el-form-item label="设备序列号">
        <el-input v-model="query.serial_no" clearable />
      </el-form-item>
      <el-form-item label="管理人">
        <el-input v-model="query.manager_name" clearable />
      </el-form-item>
      <el-form-item label="来源">
        <el-select v-model="query.source" clearable placeholder="全部" style="width: 100%">
          <el-option v-for="s in enums.sources" :key="s" :label="s" :value="s" />
        </el-select>
      </el-form-item>
      <el-form-item label="购入日期">
        <div v-if="isMobile" class="dr-mobile">
          <div class="dr-row">
            <span class="dr-cap">开始</span>
            <DateSelect v-model="query.purchase_from" />
          </div>
          <div class="dr-row">
            <span class="dr-cap">结束</span>
            <DateSelect v-model="query.purchase_to" />
          </div>
        </div>
        <el-date-picker
          v-else
          v-model="purchaseRange"
          type="daterange"
          value-format="YYYY-MM-DD"
          start-placeholder="开始"
          end-placeholder="结束"
          style="width: 100%"
        />
      </el-form-item>
      <el-form-item label="金额区间">
        <div style="display: flex; gap: 8px; width: 100%">
          <el-input v-model="query.amount_min" placeholder="最小" />
          <el-input v-model="query.amount_max" placeholder="最大" />
        </div>
      </el-form-item>
    </el-form>

    <template #footer>
      <el-button @click="reset">清空</el-button>
      <el-button type="primary" @click="apply">筛选</el-button>
    </template>
  </el-drawer>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import http from "../../api/client";
import { useIsMobile } from "../../composables/useIsMobile";
import DateSelect from "../../components/DateSelect.vue";

const props = defineProps<{ modelValue: boolean; query: Record<string, any> }>();
const emit = defineEmits(["update:modelValue", "apply"]);

const isMobile = useIsMobile();
const treeProps = { label: "name", children: "children" };
const enums = ref<any>({ statuses: [], sources: [] });
const categories = ref<any[]>([]);
const areas = ref<any[]>([]);
const companies = ref<any[]>([]);
const departments = ref<any[]>([]);

const purchaseRange = computed({
  get: () =>
    props.query.purchase_from || props.query.purchase_to
      ? [props.query.purchase_from, props.query.purchase_to]
      : null,
  set: (v: string[] | null) => {
    props.query.purchase_from = v?.[0] || "";
    props.query.purchase_to = v?.[1] || "";
  },
});

function apply() {
  emit("update:modelValue", false);
  emit("apply");
}

function reset() {
  for (const k of Object.keys(props.query)) {
    if (["page", "page_size"].includes(k)) continue;
    props.query[k] = Array.isArray(props.query[k]) ? [] : "";
  }
  apply();
}

onMounted(async () => {
  const [e, c, a, co, d] = await Promise.all([
    http.get("/enums"),
    http.get("/categories"),
    http.get("/areas"),
    http.get("/companies"),
    http.get("/departments"),
  ]);
  enums.value = e.data;
  categories.value = c.data || [];
  areas.value = a.data || [];
  companies.value = co.data || [];
  departments.value = d.data || [];
});
</script>

<style scoped>
.dr-mobile {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
}

.dr-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.dr-cap {
  flex: 0 0 auto;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
</style>
