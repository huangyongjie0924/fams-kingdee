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
        <div v-if="state === 'running'" class="frame" />
        <div v-if="state === 'starting'" class="mask">
          <el-icon class="is-loading"><Loading /></el-icon>
          <span>正在启动摄像头…</span>
        </div>
        <div v-else-if="state === 'idle'" class="mask"><span>摄像头未启动</span></div>
      </div>

      <el-alert v-if="hint" class="hint" type="warning" :closable="false" show-icon :title="hint" />

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
import { useRouter } from "vue-router";
import { ElMessage } from "element-plus";
import { ArrowLeft, Camera, Loading } from "@element-plus/icons-vue";
import jsQR from "jsqr";
import { extractAssetCode } from "../../utils/qr";

// 取景解码：先降到 480px 宽再取像素，满帧解码会让中端安卓持续满载。
// 限到约 8fps 同理 —— 人手持扫码不需要 30fps 的识别频率。
const SCAN_W = 480;
const FRAME_MS = 125;
const PHOTO_MAX = 1000;

const router = useRouter();
const video = ref<HTMLVideoElement | null>(null);
const fileInput = ref<HTMLInputElement | null>(null);
const state = ref<"idle" | "starting" | "running" | "error">("idle");
const hint = ref("");
const manual = ref("");

const canvas = document.createElement("canvas");
const ctx = canvas.getContext("2d", { willReadFrequently: true });

let stream: MediaStream | null = null;
let rafId = 0;
let lastScan = 0;
let handled = false;
let lastMiss = 0;

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
  go(code);
}

function go(code: string) {
  stop();
  // 复用列表页那一条 asset_code 深链通路，只留一套解析实现，URL 也能分享
  router.replace({ name: "assets", query: { asset_code: code } });
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
  handled = false;
  start();
});

onBeforeUnmount(stop);
</script>

<style scoped>
.scan-page {
  max-width: 720px;
}

.title {
  font-size: 16px;
  font-weight: 600;
}

.stage {
  position: relative;
  width: 100%;
  aspect-ratio: 4 / 3;
  background: #111;
  border-radius: 6px;
  overflow: hidden;
}

.cam {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}

.frame {
  position: absolute;
  left: 50%;
  top: 50%;
  width: 62%;
  aspect-ratio: 1;
  transform: translate(-50%, -50%);
  border: 2px solid #67c23a;
  border-radius: 8px;
  box-shadow: 0 0 0 100vmax rgba(0, 0, 0, 0.35);
  pointer-events: none;
}

.mask {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  color: #dcdfe6;
  font-size: 14px;
}

.hint {
  margin-top: 12px;
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
  font-size: 13px;
  color: #606266;
}

.tip {
  font-size: 12px;
  color: #909399;
}

.file {
  display: none;
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
