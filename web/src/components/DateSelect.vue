<template>
  <div class="ds">
    <el-select v-model="y" clearable placeholder="年" class="ds-part" @change="onChange">
      <el-option v-for="v in years" :key="v" :label="v + ' 年'" :value="v" />
    </el-select>
    <el-select v-model="m" clearable placeholder="月" class="ds-part" @change="onChange">
      <el-option v-for="v in 12" :key="v" :label="v + ' 月'" :value="v" />
    </el-select>
    <el-select v-if="withDay" v-model="d" clearable placeholder="日" class="ds-part" @change="onChange">
      <el-option v-for="v in dayCount" :key="v" :label="v + ' 日'" :value="v" />
    </el-select>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";

// 窄屏专用：el-date-picker 的面板是固定宽度（单个约 322px，区间约 646px），
// 在 375px 屏上必然溢出屏幕，两侧的月份/日期点不到。换成三段下拉，
// 弹层就是个矮列表，多宽的屏都放得下。桌面端继续用日历，见各调用处的 isMobile 分支。
const props = withDefaults(
  defineProps<{
    modelValue?: string;
    type?: "date" | "month";
  }>(),
  { modelValue: "", type: "date" },
);

const emit = defineEmits(["update:modelValue"]);

const THIS_YEAR = new Date().getFullYear();
// 往前覆盖到常见资产购入年份，往后留够维保到期这类未来日期
const years = Array.from({ length: 71 }, (_, i) => THIS_YEAR + 20 - i);

const withDay = computed(() => props.type === "date");
const y = ref<number | null>(null);
const m = ref<number | null>(null);
const d = ref<number | null>(null);

const dayCount = computed(() => {
  if (!y.value || !m.value) return 31;
  return new Date(y.value, m.value, 0).getDate();
});

const pad = (n: number) => String(n).padStart(2, "0");

// 当前三段能拼出的值。任一段没选齐就是空串——调用方拿到空串等于「没填」，
// 不会把半截日期写进 form。
function toVal(): string {
  if (!y.value || !m.value) return "";
  if (withDay.value && !d.value) return "";
  return withDay.value ? `${y.value}-${pad(m.value)}-${pad(d.value)}` : `${y.value}-${pad(m.value)}`;
}

function parse(v: string) {
  const s = (v || "").trim();
  const mt = /^(\d{4})-(\d{2})(?:-(\d{2}))?$/.exec(s);
  if (!mt) {
    y.value = m.value = d.value = null;
    return;
  }
  y.value = Number(mt[1]);
  m.value = Number(mt[2]);
  d.value = mt[3] ? Number(mt[3]) : null;
}

function onChange() {
  // 先按月长收敛「日」：1/31 改成 2 月，得落到 2/28，不能拼出 2/31
  if (d.value && d.value > dayCount.value) d.value = dayCount.value;
  emit("update:modelValue", toVal());
}

// 自己发出去的值会被父组件回灌。不回灌处理的话，用户刚选完「年」、
// 值还是不完整的空串，一带而过就把三个下拉全清了，等于选不动。
watch(
  () => props.modelValue,
  (v) => {
    if ((v || "") === toVal()) return;
    parse(v || "");
  },
  { immediate: true },
);
</script>

<style scoped>
.ds {
  display: flex;
  gap: 8px;
  width: 100%;
}

/* min-width:0 关键：不加的话 select 的固有宽度会把三列撑出容器 */
.ds-part {
  flex: 1;
  min-width: 0;
}
</style>
