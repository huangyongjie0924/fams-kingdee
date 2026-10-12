<template>
  <div class="page scan-page">
    <el-card shadow="never">
      <div class="toolbar">
        <el-button :icon="ArrowLeft" @click="back">返回</el-button>
        <span class="title">扫码查资产</span>
        <div class="spacer" />
        <el-button v-if="state === 'error'" type="primary" @click="start">重试</el-button>
      </div>

      <div class="stage">
        <video ref="video" class="cam" playsinline muted autoplay />
        <!-- 取景框：四角 crop marks + 信号黄扫描线，与登录页的制图角标同一语汇 -->
        <div v-if="state === 'running'" class="frame" :class="{ 'is-hit': !!hitCode }">
          <span class="crop tl" aria-hidden="true" />
          <span class="crop tr" aria-hidden="true" />
          <span class="crop bl" aria-hidden="true" />
          <span class="crop br" aria-hidden="true" />
          <span class="scanline" aria-hidden="true" />
        </div>
        <div v-if="state === 'starting'" class="mask">
          <el-icon class="is-loading"><Loading /></el-icon>
          <span>正在启动摄像头…</span>
        </div>
        <div v-else-if="state === 'idle'" class="mask"><span>摄像头未启动</span></div>
      </div>

      <el-alert v-if="hint" class="hint" type="warning" :closable="false" show-icon :title="hint" />

      <!-- 识别结果 / 最近识别：盘点时用户会连着扫几十台，能回看刚扫了什么可以少重复劳动。
         左侧 3px 色条承载语义：绿=刚刚识别成功，灰=历史记录。 -->
      <div v-if="recent.length" class="recent">
        <div class="recent-hd">
          <span class="recent-title">最近识别</span>
          <span class="spacer" />
          <el-button text size="small" @click="clearRecent">清空</el-button>
        </div>
        <div
          v-for="(r, i) in recent"
          :key="r.code + r.at"
          class="recent-row"
          :class="i === 0 && r.ok ? 'is-fresh' : 'is-old'"
          role="button"
          tabindex="0"
          @click="go(r.code)"
          @keyup.enter="go(r.code)"
        >
          <span class="recent-code u-mono">{{ r.code }}</span>
          <span class="recent-at u-mono">{{ formatAt(r.at) }}</span>
        </div>
      </div>

      <div class="fallback">
        <div class="row">
          <span class="lb">拍照识别</span>
          <el-button :icon="Camera" @click="pickPhoto">拍照 / 选图</el-button>
          <span class="tip">不需要摄像头权限，在 App 内置浏览器里通常比取景更可靠</span>
        </div>
        <div class="row">
          <span class="lb">手动输入</span>
          <el-input v-model="manual" placeholder="资产编码" style="width: 240px" @keyup.enter="submitManual" />
          <el-button type="primary" @click="submitManual">查询</el-button>
        </div>
      </div>

      <!-- 走系统文件选择器，不要求安全上下文、也不要求摄像头权限 API -->
      <input ref="fileInput" class="file" type="file" accept="image/*" capture="environment" @change="onPhoto" />
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage } from "element-plus";
import { ArrowLeft, Camera, Loading } from "@element-plus/icons-vue";
import jsQR from "jsqr";
import { extractAssetCode } from "../../utils/qr";

// 取景解码：先降到 480px 宽再取像素，满帧解码会让中端安卓持续满载。
// 限到约 8fps 同理 —— 人手持扫码不需要 30fps 的识别频率。
// ⚠️ 这三个常数是为低端手机调过的，视觉改造**不得调整**。
const SCAN_W = 480;
const FRAME_MS = 125;
const PHOTO_MAX = 1000;

// 识别成功后停顿一下再跳转，让用户看到「扫到了」的确认。220ms 是能感知又不拖沓的量级。
const HIT_HOLD_MS = 220;
const RECENT_KEY = "scan_recent";
const RECENT_MAX = 10;

const router = useRouter();
const route = useRoute();
const video = ref<HTMLVideoElement | null>(null);
const fileInput = ref<HTMLInputElement | null>(null);
const state = ref<"idle" | "starting" | "running" | "error">("idle");
const hint = ref("");
const manual = ref("");
const hitCode = ref("");
const recent = ref<{ code: string; ok: boolean; at: number }[]>([]);

const canvas = document.createElement("canvas");
const ctx = canvas.getContext("2d", { willReadFrequently: true });

let stream: MediaStream | null = null;
let rafId = 0;
let lastScan = 0;
let handled = false;
let lastMiss = 0;
let gone = false;

// 探测阶梯：不要笼统报「摄像头不可用」，三种环境的失败原因和解法完全不同。
const blocked = computed(() => {
  if (!window.isSecureContext) {
    return "当前不是 HTTPS 环境，浏览器禁止调用摄像头。请用系统相机 / 微信扫码，或手动输入编码。";
  }
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    return "当前浏览器不支持摄像头取景（多为 App 内置浏览器）。请用系统浏览器打开，或手动输入编码。";
  }
  return "";
});

// —— 最近识别：存 sessionStorage，扫完跳走再回来还能看到刚扫过什么 ——
function loadRecent() {
  try {
    const raw = sessionStorage.getItem(RECENT_KEY);
    recent.value = raw ? JSON.parse(raw) : [];
  } catch {
    recent.value = [];
  }
}

function pushRecent(code: string) {
  const list = [{ code, ok: true, at: Date.now() }, ...recent.value.filter((r) => r.code !== code)];
  recent.value = list.slice(0, RECENT_MAX);
  try {
    sessionStorage.setItem(RECENT_KEY, JSON.stringify(recent.value));
  } catch {
    // 隐私模式下写不进去也不影响扫码本身，忽略
  }
}

function clearRecent() {
  recent.value = [];
  sessionStorage.removeItem(RECENT_KEY);
}

function formatAt(t: number) {
  const d = new Date(t);
  const p = (n: number) => (n < 10 ? `0${n}` : String(n));
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function stop() {
  cancelAnimationFrame(rafId);
  rafId = 0;
  // 不 stop 轨道，摄像头的指示灯不会灭
  stream?.getTracks().forEach((t) => t.stop());
  stream = null;
  if (video.value) video.value.srcObject = null;
}

async function start() {
  hint.value = blocked.value;
  if (blocked.value) {
    state.value = "error";
    return;
  }
  state.value = "starting";
  hint.value = "";
  try {
    // facingMode 用 ideal 不用 exact：桌面浏览器没有后置摄像头，exact 会抛 OverconstrainedError
    stream = await navigator.mediaDevices.getUserMedia({
      video: { facingMode: { ideal: "environment" } },
      audio: false,
    });
    const v = video.value;
    if (!v) throw new Error("video 元素未就绪");
    v.srcObject = stream;
    await v.play();
    state.value = "running";
    lastScan = 0;
    rafId = requestAnimationFrame(tick);
  } catch (e: any) {
    state.value = "error";
    const name = e?.name || "";
    if (name === "NotAllowedError" || name === "SecurityError") {
      hint.value = "摄像头权限被拒绝。App 内置浏览器常会直接拒绝，请改用系统浏览器打开，或手动输入编码。";
    } else if (name === "NotFoundError" || name === "OverconstrainedError") {
      hint.value = "没有找到可用摄像头。请用系统相机 / 微信扫码，或手动输入编码。";
    } else if (name === "NotReadableError") {
      hint.value = "摄像头被其他应用占用或被系统阻止。关掉其他相机应用后重试，或手动输入编码。";
    } else {
      hint.value = `摄像头启动失败（${name || e?.message || "未知原因"}）。请用系统相机扫码，或手动输入编码。`;
    }
  }
}

function tick(now: number) {
  rafId = requestAnimationFrame(tick);
  if (now - lastScan < FRAME_MS) return;
  lastScan = now;

  const v = video.value;
  if (!v || !v.videoWidth || !ctx) return;

  const scale = Math.min(1, SCAN_W / v.videoWidth);
  const w = Math.max(1, Math.round(v.videoWidth * scale));
  const h = Math.max(1, Math.round(v.videoHeight * scale));
  canvas.width = w;
  canvas.height = h;
  ctx.drawImage(v, 0, 0, w, h);
  const img = ctx.getImageData(0, 0, w, h);
  // 标签是黑码白底，不做反色尝试，省一半解码时间
  const res = jsQR(img.data, w, h, { inversionAttempts: "dontInvert" });
  if (res?.data) onHit(res.data);
}

function onHit(payload: string) {
  if (handled) return;
  const code = extractAssetCode(payload);
  if (!code) {
    // 扫到了别的二维码。解码循环每 125ms 就会命中一次，节流到 3 秒一条，别刷屏。
    const now = Date.now();
    if (now - lastMiss > 3000) {
      lastMiss = now;
      ElMessage.warning("这不是本系统的资产标签");
    }
    return;
  }
  handled = true;
  // 先停解码再闪一下：否则这 220ms 里每 125ms 还会再命中一次
  stop();
  hitCode.value = code;
  pushRecent(code);
  window.setTimeout(() => {
    // 用户在这 220ms 里自己点了返回，就不要再把人拽走
    if (!gone) go(code);
  }, HIT_HOLD_MS);
}

function go(code: string) {
  stop();
  // 复用列表页那一条 asset_code 深链通路，只留一套解析实现，URL 也能分享。
  // to 由来源页指定（报修页带 to=repair-new），没带就保持原来回资产列表的行为。
  const raw = route.query.to;
  const to = (Array.isArray(raw) ? raw[0] : raw) || "assets";
  router.replace({ name: to, query: { asset_code: code } });
}

function pickPhoto() {
  fileInput.value?.click();
}

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error("图片解码失败"));
    img.src = url;
  });
}

// cropCenter 为真时只取中间约 60%：二维码在画面里偏小、四周杂物多时，裁一刀反而好认
function decodeImage(img: HTMLImageElement, cropCenter = false): string {
  if (!ctx) return "";
  let sw = img.naturalWidth;
  let sh = img.naturalHeight;
  let sx = 0;
  let sy = 0;
  if (cropCenter) {
    sw = Math.round(img.naturalWidth * 0.6);
    sh = Math.round(img.naturalHeight * 0.6);
    sx = Math.round((img.naturalWidth - sw) / 2);
    sy = Math.round((img.naturalHeight - sh) / 2);
  }
  const scale = Math.min(1, PHOTO_MAX / Math.max(sw, sh));
  const w = Math.max(1, Math.round(sw * scale));
  const h = Math.max(1, Math.round(sh * scale));
  canvas.width = w;
  canvas.height = h;
  ctx.drawImage(img, sx, sy, sw, sh, 0, 0, w, h);
  const data = ctx.getImageData(0, 0, w, h);
  // 照片可能逆光或角度刁，反色两种都试
  return jsQR(data.data, w, h, { inversionAttempts: "attemptBoth" })?.data || "";
}

async function onPhoto(e: Event) {
  const input = e.target as HTMLInputElement;
  const file = input.files?.[0];
  // 清空，否则连续选同一张图不会触发 change
  input.value = "";
  if (!file) return;

  const url = URL.createObjectURL(file);
  try {
    const img = await loadImage(url);
    const code = decodeImage(img) || decodeImage(img, true);
    if (code) {
      onHit(code);
      return;
    }
    ElMessage.warning("没识别出二维码，换一张更清晰、更正对着标签的照片试试");
  } catch {
    ElMessage.warning("这张图打不开，请换一张");
  } finally {
    URL.revokeObjectURL(url);
  }
}

function submitManual() {
  const code = extractAssetCode(manual.value) || manual.value.trim();
  if (!code) {
    ElMessage.warning("请输入资产编码");
    return;
  }
  go(code);
}

function back() {
  router.back();
}

onMounted(() => {
  gone = false;
  handled = false;
  loadRecent();
  start();
});

onBeforeUnmount(() => {
  gone = true;
  stop();
});
</script>

<style scoped>
.scan-page {
  max-width: 720px;
}

.title {
  font-size: var(--t-20);
  font-weight: 600;
  color: var(--ink);
}

.stage {
  position: relative;
  width: 100%;
  aspect-ratio: 4 / 3;
  background: #111;
  border-radius: var(--r);
  overflow: hidden;
}

.cam {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}

/* 取景框：只保留四角制图角标（crop marks）+ 扫描线，去掉原来的绿色实线方框。
   实线框在深色取景里过重，四角角标同样能框住取景区，且与登录页语汇一致。 */
.frame {
  position: absolute;
  left: 50%;
  top: 50%;
  width: 62%;
  aspect-ratio: 1;
  transform: translate(-50%, -50%);
  box-shadow: 0 0 0 100vmax rgba(0, 0, 0, 0.35);
  pointer-events: none;
}

.crop {
  position: absolute;
  width: 24px;
  height: 24px;
  border: 2px solid var(--signal);
}

.crop.tl {
  top: 0;
  left: 0;
  border-right: none;
  border-bottom: none;
}

.crop.tr {
  top: 0;
  right: 0;
  border-left: none;
  border-bottom: none;
}

.crop.bl {
  bottom: 0;
  left: 0;
  border-right: none;
  border-top: none;
}

.crop.br {
  bottom: 0;
  right: 0;
  border-left: none;
  border-top: none;
}

.scanline {
  position: absolute;
  left: 0;
  right: 0;
  height: 2px;
  background: var(--signal);
  box-shadow: 0 0 10px rgba(245, 179, 1, .85);
  animation: scan-move 2.2s ease-in-out infinite;
}

@keyframes scan-move {
  0%,
  100% {
    top: 4%;
  }
  50% {
    top: 96%;
  }
}

/* 识别成功：信号黄闪一下，给「扫到了」的确认 */
.frame.is-hit::after {
  content: "";
  position: absolute;
  inset: 0;
  background: rgba(245, 179, 1, .3);
}

.mask {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  color: #dcdfe6;
  font-size: var(--t-14);
}

.hint {
  margin-top: 12px;
}

/* —— 最近识别 —— */
.recent {
  margin-top: 16px;
}

.recent-hd {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.recent-title {
  font-size: var(--t-13);
  font-weight: 600;
  color: var(--ink);
}

.recent-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 10px 7px 12px;
  margin-bottom: 6px;
  background: var(--surface);
  border: var(--hairline);
  border-left-width: 3px;
  border-radius: var(--r);
  cursor: pointer;
}

.recent-row.is-fresh {
  border-left-color: var(--ok);
  background: #f2f8f4;
}

.recent-row.is-old {
  border-left-color: var(--steel-300);
}

.recent-code {
  flex: 1;
  min-width: 0;
  color: var(--ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.recent-at {
  flex: 0 0 auto;
  font-size: var(--t-11);
  color: var(--steel-500);
}

.fallback {
  margin-top: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.lb {
  width: 72px;
  flex: none;
  font-size: var(--t-13);
  color: var(--steel-700);
}

.tip {
  font-size: var(--t-12);
  color: var(--steel-500);
}

.file {
  display: none;
}

/* 尊重系统的「减少动态效果」设置 */
@media (prefers-reduced-motion: reduce) {
  .scanline {
    animation: none;
    top: 50%;
  }
}

@media (max-width: 767px), (max-height: 520px) {
  .lb {
    width: 100%;
  }

  .row .el-input {
    flex: 1;
  }
}
</style>
