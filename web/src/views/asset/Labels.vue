<template>
  <div class="page labels-page">
    <el-card class="no-print bar" shadow="never">
      <div class="toolbar">
        <el-button :icon="ArrowLeft" @click="back">返回</el-button>
        <el-select v-model="opts.preset" style="width: 230px">
          <el-option v-for="p in PRESETS" :key="p.key" :label="p.label" :value="p.key" />
        </el-select>
        <span class="lb">起始标签</span>
        <el-input-number v-model="opts.start" :min="0" :max="Math.max(0, perPage - 1)" controls-position="right" style="width: 110px" />
        <span class="lb">横向 mm</span>
        <el-input-number v-model="opts.offsetX" :min="-30" :max="30" :step="0.5" :precision="1" controls-position="right" style="width: 110px" />
        <span class="lb">纵向 mm</span>
        <el-input-number v-model="opts.offsetY" :min="-30" :max="30" :step="0.5" :precision="1" controls-position="right" style="width: 110px" />
        <div class="spacer" />
        <el-text type="info">共 {{ cards.length }} 张 · {{ pages.length }} 页</el-text>
        <el-button type="primary" :icon="Printer" :disabled="!cards.length" @click="doPrint">打印</el-button>
      </div>

      <div class="toolbar">
        <span class="lb">标签内容</span>
        <el-checkbox v-model="opts.fields.name">名称</el-checkbox>
        <el-checkbox v-model="opts.fields.code">编码</el-checkbox>
        <el-checkbox v-model="opts.fields.user">使用人</el-checkbox>
        <el-checkbox v-model="opts.fields.dept">使用部门</el-checkbox>
        <div class="spacer" />
        <span class="lb">二维码网址前缀</span>
        <el-input v-model="opts.baseUrl" style="width: 290px" placeholder="https://<your-host>:8443" />
        <el-button :disabled="opts.baseUrl === origin" @click="opts.baseUrl = origin">用当前地址</el-button>
      </div>

      <el-alert v-if="badBase" type="error" :closable="false" show-icon
        title="这个前缀手机打不开" :description="badBase" />
      <el-alert v-else type="warning" :closable="false" show-icon
        title="打印时请在系统打印对话框里选「缩放 100%」「边距 无」"
        description="选成「适合纸张」会把整个网格缩放，标签就对不上不干胶的分格了。若整版偏移，用上面的横向/纵向微调补偿。" />
    </el-card>

    <el-empty v-if="!cards.length" class="no-print" description="没有待打印的资产" />

    <el-card v-if="isMobile && cards.length" class="no-print" shadow="never">
      手机端不支持打印标签，请在电脑上打开本页。
    </el-card>

    <div v-else-if="cards.length" class="sheets">
      <div v-for="(pg, pi) in pages" :key="pi" class="sheet-page" :style="sheetStyle">
        <div v-for="(cell, ci) in pg" :key="ci" class="label">
          <template v-if="cell">
            <div class="qr" v-html="qrOf(cell)" />
            <div class="meta">
              <div v-if="opts.fields.name && cell.name" class="nm">{{ cell.name }}</div>
              <div v-if="opts.fields.code" class="cd">{{ cell.asset_code }}</div>
              <div v-if="opts.fields.user && cell.user_emp_name" class="sm">使用人：{{ cell.user_emp_name }}</div>
              <div v-if="opts.fields.dept && cell.use_dept_name" class="sm">使用部门：{{ cell.use_dept_name }}</div>
            </div>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ArrowLeft, Printer } from "@element-plus/icons-vue";
import http from "../../api/client";
import { useIsMobile } from "../../composables/useIsMobile";
import { A4_PORTRAIT, usePrintPage } from "../../utils/printPage";
import { assetQrUrl, renderQrSvg } from "../../utils/qr";

interface AssetCard {
  id: number;
  asset_code: string;
  name: string;
  user_emp_name?: string;
  use_dept_name?: string;
}

// 三种常见的 A4 不干胶规格，尺寸就是「A4 等分」的结果（210/列数、297/行数），
// 无边距满版排布。真实纸上的不可打印边缘造成的整体偏移交给横向/纵向微调。
const PRESETS = [
  { key: "30", label: "30 格（3×10，70×29.7mm）", cols: 3, rows: 10, h: 29.7 },
  { key: "24", label: "24 格（3×8，70×37mm）", cols: 3, rows: 8, h: 37 },
  { key: "10", label: "10 格（2×5，105×59.4mm）", cols: 2, rows: 5, h: 59.4 },
];

const STORE_KEY = "asset_label_opts";
const MAX_LABELS = 200;

interface LabelOpts {
  preset: string;
  fields: { name: boolean; code: boolean; user: boolean; dept: boolean };
  start: number;
  offsetX: number;
  offsetY: number;
  baseUrl: string;
}

const DEFAULTS: LabelOpts = {
  preset: "30",
  fields: { name: true, code: true, user: true, dept: false },
  start: 0,
  offsetX: 0,
  offsetY: 0,
  baseUrl: "",
};

function loadOpts(): LabelOpts {
  const fallback: LabelOpts = { ...DEFAULTS, fields: { ...DEFAULTS.fields }, baseUrl: window.location.origin };
  try {
    const raw = localStorage.getItem(STORE_KEY);
    if (!raw) return fallback;
    const saved = JSON.parse(raw);
    return {
      ...fallback,
      ...saved,
      fields: { ...DEFAULTS.fields, ...(saved.fields || {}) },
      baseUrl: saved.baseUrl || window.location.origin,
    };
  } catch {
    return fallback;
  }
}

const route = useRoute();
const router = useRouter();
const isMobile = useIsMobile();
const opts = reactive<LabelOpts>(loadOpts());
const cards = ref<AssetCard[]>([]);
const origin = window.location.origin;

// usePrintPage 内部要注册 onMounted/onBeforeUnmount，必须在 setup 期间同步调用，
// 放进 onMounted 里就已经错过注册时机了。
usePrintPage(A4_PORTRAIT);

watch(opts, () => localStorage.setItem(STORE_KEY, JSON.stringify(opts)), { deep: true });

const preset = computed(() => PRESETS.find((p) => p.key === opts.preset) || PRESETS[0]);
const perPage = computed(() => preset.value.cols * preset.value.rows);

// 标签内各元素的尺寸按标签高度等比算，避免为每种规格硬编码一堆字号。
// 二维码留 4mm 内边距；字号封顶，否则 10 格的大标签会撑出夸张的字。
const metrics = computed(() => {
  const h = preset.value.h;
  const name = +Math.min(7, Math.max(3.6, h * 0.155)).toFixed(2);
  return { qr: +(h - 4).toFixed(2), name, code: +(name * 0.8).toFixed(2), sub: +(name * 0.72).toFixed(2) };
});

// 用 position:relative + left/top 做微调，而不是 transform —— transform 会新建
// 包含块，在分页时可能被浏览器当成一个整体处理，relative 只挪视觉位置、不影响分页。
const sheetStyle = computed(() => {
  const m = metrics.value;
  return {
    gridTemplateColumns: `repeat(${preset.value.cols}, 1fr)`,
    gridAutoRows: `calc(297mm / ${preset.value.rows})`,
    left: `${opts.offsetX}mm`,
    top: `${opts.offsetY}mm`,
    "--qr": `${m.qr}mm`,
    "--name": `${m.name}mm`,
    "--code": `${m.code}mm`,
    "--sub": `${m.sub}mm`,
  } as Record<string, string>;
});

const badBase = computed(() => {
  const b = (opts.baseUrl || "").trim();
  if (!b) return "二维码前缀不能为空。";
  if (/\/\/(localhost|127\.0\.0\.1)(:|\/|$)/.test(b)) {
    return "当前是 localhost，手机扫了打不开。请填手机能访问的地址，例如 https://<your-host>:8443。";
  }
  if (!/^https?:\/\//.test(b)) return "前缀要以 http:// 或 https:// 开头。";
  return "";
});

// 先补「起始标签」个空位跳过已用格的标签纸，再按每页格数切片。
// 分页在 JS 里算好，不依赖 break-inside：A4 高度和行高都是精确毫米值，切片最可控。
const pages = computed(() => {
  const cells: (AssetCard | null)[] = Array.from({ length: opts.start }, () => null);
  for (const c of cards.value) cells.push(c);
  const out: (AssetCard | null)[][] = [];
  for (let i = 0; i < cells.length; i += perPage.value) out.push(cells.slice(i, i + perPage.value));
  return out;
});

function qrOf(card: AssetCard): string {
  return renderQrSvg(assetQrUrl(card.asset_code, opts.baseUrl));
}

function back() {
  router.back();
}

function doPrint() {
  window.print();
}

function parseIds(): number[] {
  const raw = String(route.query.ids || "");
  const seen = new Set<number>();
  for (const part of raw.split(",")) {
    const n = Number(part.trim());
    if (Number.isInteger(n) && n > 0) seen.add(n);
  }
  return [...seen];
}

async function load() {
  const ids = parseIds();
  if (!ids.length || ids.length > MAX_LABELS) return;
  // 错误提示由 client.ts 的拦截器统一弹出，这里不再重复 toast
  const res = await http.get("/assets/labels", { params: { ids: ids.join(",") } });
  cards.value = res.data.items || [];
}

onMounted(() => {
  document.body.classList.add("printing-labels");
  load();
});

onBeforeUnmount(() => {
  document.body.classList.remove("printing-labels");
});
</script>

<style scoped>
.bar {
  margin-bottom: 12px;
}

.lb {
  font-size: 13px;
  color: #606266;
  white-space: nowrap;
}

.bar :deep(.el-alert) {
  margin-top: 8px;
}

.sheets {
  background: #e9ecf2;
  padding: 12px;
}

/* A4 满版：宽 210mm、高 297mm。height 写死 + overflow:hidden 是为了挡住舍入误差
   多吐出一张空白页；两边都有，缺一个就会多一张。 */
.sheet-page {
  position: relative;
  width: 210mm;
  height: 297mm;
  margin: 0 auto 12px;
  background: #fff;
  overflow: hidden;
  display: grid;
  box-shadow: 0 1px 6px rgba(0, 0, 0, 0.15);
  break-after: page;
}

/* 最后一页再分页会多打一张空白纸 */
.sheet-page:last-child {
  break-after: auto;
  margin-bottom: 0;
}

.label {
  display: flex;
  align-items: center;
  gap: 2mm;
  padding: 2mm;
  overflow: hidden;
  break-inside: avoid;
}

.qr {
  width: var(--qr);
  height: var(--qr);
  flex: none;
}

/* v-html 注入的 SVG 拿不到 scoped 属性，必须用 :deep 才够得到 */
.qr :deep(svg) {
  display: block;
  width: 100%;
  height: 100%;
}

.meta {
  min-width: 0;
  flex: 1;
}

.nm {
  font-size: var(--name);
  font-weight: 600;
  line-height: 1.25;
  color: #000;
  word-break: break-all;
}

.cd {
  font-size: var(--code);
  line-height: 1.35;
  color: #000;
  word-break: break-all;
}

.sm {
  font-size: var(--sub);
  line-height: 1.35;
  color: #333;
  word-break: break-all;
}
</style>

<style>
@media print {
  /* .aside/.header 是 Main.vue 的 scoped 类名，.el-* 是 Element Plus 的固定类，
     两套都写上，重构布局时打印规则不会静默失效。 */
  body.printing-labels .aside,
  body.printing-labels .header,
  body.printing-labels .el-aside,
  body.printing-labels .el-header,
  body.printing-labels .el-overlay,
  body.printing-labels .el-drawer,
  body.printing-labels .no-print {
    display: none !important;
  }

  /* 必须打断从 html 一路继承下来的高度链，否则 .el-main 的 overflow:auto 会把
     整叠标签当成一个可滚动视口，打印时只出第一页。 */
  body.printing-labels,
  body.printing-labels #app {
    height: auto !important;
    background: #fff !important;
  }

  body.printing-labels .layout,
  body.printing-labels .el-container {
    height: auto !important;
    display: block !important;
    overflow: visible !important;
  }

  body.printing-labels .el-main {
    overflow: visible !important;
    height: auto !important;
    padding: 0 !important;
  }

  body.printing-labels .page {
    padding: 0 !important;
  }

  body.printing-labels .sheets {
    background: #fff !important;
    padding: 0 !important;
  }

  body.printing-labels .sheet-page {
    margin: 0 !important;
    box-shadow: none !important;
  }
}
</style>
