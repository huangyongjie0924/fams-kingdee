<template>
  <div class="login-page">
    <!-- 左侧：工业风品牌面板。窄屏 / 矮屏整块隐藏，改由 .auth-head 承担品牌信息。 -->
    <section class="brand">
      <span class="crop crop-tl" aria-hidden="true" />
      <span class="crop crop-tr" aria-hidden="true" />
      <span class="crop crop-bl" aria-hidden="true" />
      <span class="crop crop-br" aria-hidden="true" />

      <!-- 等距立方体水印：底部右侧出血裁切，作为几何底纹 -->
      <svg class="brand-watermark" viewBox="0 0 280 340" aria-hidden="true">
        <g
          transform="translate(140,170)"
          fill="none"
          stroke="currentColor"
          stroke-width="1.4"
          stroke-linejoin="round"
        >
          <path d="M0 -160 L138.6 -80 L138.6 80 L0 160 L-138.6 80 L-138.6 -80 Z" />
          <path d="M0 0 L0 160 M0 0 L-138.6 -80 M0 0 L138.6 -80" />
        </g>
      </svg>

      <div class="brand-inner">
        <!-- 企业铭牌：集团 + 旗下品牌，还原原始 .ai 里带分隔线的锁定组合。
             logo 必须用原色（品牌规范），但它的灰色文字在石墨黑上只有 1.9:1，
             所以整块承载在浅色铭牌上 —— 顺带就是设备铭牌的语汇。 -->
        <div class="nameplate">
          <img class="nameplate-logo" :src="gcgsLogo" alt="示例集团" />
          <span class="nameplate-div" aria-hidden="true" />
          <img class="nameplate-logo" :src="triangleLogo" alt="示例牌A" />
          <span class="nameplate-div" aria-hidden="true" />
          <img class="nameplate-logo" :src="diamondLogo" alt="示例牌B" />
        </div>

        <div class="brand-mid">
          <p class="eyebrow u-mono">FAMS · 固定资产管理平台</p>
          <h1 class="brand-title">固定资产<br />台账系统</h1>
          <p class="brand-desc">
            资产入账、扫码盘点、报修维修、标签打印，<br />一套台账贯穿设备全生命周期。
          </p>

          <div class="barcode" aria-hidden="true" />

          <ul class="specs">
            <li><span class="k u-mono">资产台账</span><span class="v">星瀚同步 · 实时对账</span></li>
            <li><span class="k u-mono">盘点作业</span><span class="v">扫码盘点 · 差异留痕</span></li>
            <li><span class="k u-mono">维修流程</span><span class="v">报修到完工 · 全程可追</span></li>
          </ul>
        </div>

        <p class="brand-foot u-mono">星瀚同步 · 云之家集成 · 内网部署</p>
      </div>
    </section>

    <!-- 右侧：登录表单 -->
    <section class="auth">
      <div class="auth-col">
        <div class="auth-head">
          <img class="auth-head-logo" :src="gcgsLogo" alt="示例集团" />
          <b>固定资产台账系统</b>
        </div>

        <div class="auth-body">
          <p class="eyebrow u-mono">Sign in</p>
          <h2 class="auth-title">登录</h2>
          <p class="auth-sub">请使用企业账号登录</p>

          <el-form :model="form" @submit.prevent>
            <el-form-item>
              <el-input v-model="form.username" placeholder="用户名" size="large" @keyup.enter="submit">
                <template #prefix><el-icon><User /></el-icon></template>
              </el-input>
            </el-form-item>
            <el-form-item>
              <el-input
                v-model="form.password"
                type="password"
                placeholder="密码"
                size="large"
                show-password
                @keyup.enter="submit"
              >
                <template #prefix><el-icon><Lock /></el-icon></template>
              </el-input>
            </el-form-item>
            <el-button class="submit" type="primary" size="large" :loading="loading" @click="submit">
              登录
            </el-button>
          </el-form>

          <p class="auth-hint">
            <span class="hint-bar" aria-hidden="true" />
            在云之家 / 云之家 客户端内打开可免登录
          </p>
        </div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import http from "../api/client";
import { auth } from "../stores/auth";
// 集团与品牌 logo：从原始 .ai（PDF 兼容）按绘图对象重建的矢量 SVG，
// 保留品牌原色，不含 id（可安全内联）。改 logo 只需替换这些文件。
import gcgsLogo from "../assets/logos/gcgs.svg";
import triangleLogo from "../assets/logos/triangle.svg";
import diamondLogo from "../assets/logos/diamond.svg";

const route = useRoute();
const router = useRouter();
const loading = ref(false);
const form = ref({ username: "", password: "" });

// 开放重定向防护：只接受以单个 "/" 开头的站内路径（拒绝 "//evil.com"、绝对 URL 等），
// 否则丢弃回首页。redirect 来自路由守卫写入的 to.fullPath。
function safeRedirect(raw: unknown): string {
  if (typeof raw === "string" && raw.startsWith("/") && !raw.startsWith("//")) {
    return raw;
  }
  return "";
}

async function submit() {
  if (!form.value.username || !form.value.password) return;
  loading.value = true;
  try {
    const { data } = await http.post("/login", form.value);
    // 密码登录是完整权限会话，readonly 默认 false。
    auth.login(data.token, data.user);
    const dest = safeRedirect(route.query.redirect);
    if (dest) {
      router.push(dest);
    } else {
      router.push({ name: "home" });
    }
  } finally {
    loading.value = false;
  }
}
</script>

<style scoped>
/* ── 工业简约设计令牌 ───────────────────────────────────────────────
   中性色阶（石墨 → 钢灰 → 纸白）承担全部结构，唯一强调色是信号黄，
   只出现在「需要被看见」的地方：角标、聚焦环、按钮 hover、状态点。
   圆角一律 2px 硬边，不使用阴影与渐变填充（等距水印除外）。 */
.login-page {
  /* ⚠️ 这里刻意重复一份令牌，与 styles/tokens.css 的 :root 保持同值。
     原因：P0 的回滚动作是「删掉 main.ts 里那一行 tokens.css import」，
     若本页依赖 :root 提供变量，回滚就会把已上线的登录页一起打坏。
     两份值必须同步 —— 改任一处都要改另一处（--steel-500 曾因此不一致）。 */
  --ink: #14171b;
  --ink-soft: #1c2127;
  --steel-700: #3d454f;
  --steel-600: #5c6470;
  --steel-500: #656d79;
  --steel-400: #8d959f;
  --steel-300: #c3c9d0;
  --steel-200: #dde1e6;
  --paper: #f6f7f9;
  --signal: #f5b301;
  --signal-deep: #cf9700;
  --u-mono: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;

  /* 让 Element Plus 的 large 尺寸跟着工业风的块面走 */
  --el-component-size-large: 48px;

  height: 100%;
  display: grid;
  grid-template-columns: minmax(0, 1.08fr) minmax(0, 0.92fr);
  background: var(--paper);
  color: var(--ink);
  overflow: hidden;
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
}

.u-mono {
  font-family: var(--u-mono);
  font-weight: 400;
}

/* ── 左侧品牌面板 ─────────────────────────────────────────────────── */
.brand {
  position: relative;
  display: flex;
  flex-direction: column;
  padding: 52px 52px 36px;
  background: var(--ink);
  color: #fff;
  overflow: hidden;
}

/* 工程网格底纹 */
.brand::before {
  content: "";
  position: absolute;
  inset: 0;
  background-image:
    linear-gradient(to right, rgba(255, 255, 255, 0.045) 1px, transparent 1px),
    linear-gradient(to bottom, rgba(255, 255, 255, 0.045) 1px, transparent 1px);
  background-size: 44px 44px;
  pointer-events: none;
}

/* 左上角的信号色辉光：给深色面板一点色彩纵深，避免死黑 */
.brand::after {
  content: "";
  position: absolute;
  inset: 0;
  background: radial-gradient(120% 78% at 8% -10%, rgba(245, 179, 1, 0.11), transparent 62%);
  pointer-events: none;
}

.brand-inner {
  position: relative;
  z-index: 1;
  flex: 1;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
}

/* 四角制图角标（crop marks） */
.crop {
  position: absolute;
  width: 14px;
  height: 14px;
  border: 0 solid rgba(255, 255, 255, 0.22);
  pointer-events: none;
}
.crop-tl { top: 18px; left: 18px; border-top-width: 1px; border-left-width: 1px; }
.crop-tr { top: 18px; right: 18px; border-top-width: 1px; border-right-width: 1px; }
.crop-bl { bottom: 18px; left: 18px; border-bottom-width: 1px; border-left-width: 1px; }
.crop-br { bottom: 18px; right: 18px; border-bottom-width: 1px; border-right-width: 1px; }

.brand-watermark {
  position: absolute;
  right: -70px;
  bottom: -130px;
  width: 500px;
  height: 607px;
  color: #fff;
  /* 只做肌理，不做图形：再高一点就会抢标题的注意力 */
  opacity: 0.035;
  pointer-events: none;
}

/* 企业铭牌：浅底承载 logo 原色，左侧一道信号黄边 = 设备铭牌 */
.nameplate {
  display: inline-flex;
  align-items: center;
  align-self: flex-start;
  gap: 17px;
  padding: 13px 20px;
  background: var(--paper);
  border-left: 3px solid var(--signal);
  border-radius: 2px;
}
.nameplate-logo {
  display: block;
  height: 62px;
  width: auto;
}
.nameplate-div {
  width: 1px;
  height: 44px;
  background: var(--steel-200);
}

/* 等宽小标签 + 前置信号色短横线，工业制图的注记感 */
.eyebrow {
  display: flex;
  align-items: center;
  margin: 0;
  font-size: 11px;
  letter-spacing: 0.2em;
  color: rgba(255, 255, 255, 0.48);
}
.eyebrow::before {
  content: "";
  flex: none;
  width: 20px;
  height: 2px;
  margin-right: 10px;
  background: var(--signal);
}

.brand-title {
  margin: 16px 0 0;
  font-size: 40px;
  line-height: 1.18;
  font-weight: 700;
  letter-spacing: 0.01em;
}

.brand-desc {
  margin: 18px 0 0;
  max-width: 420px;
  font-size: 13.5px;
  line-height: 1.85;
  color: rgba(255, 255, 255, 0.56);
}

/* 条码装饰：资产标签的意象，用重复渐变画，无需图片资源 */
.barcode {
  width: 220px;
  height: 24px;
  margin: 26px 0 0;
  color: rgba(255, 255, 255, 0.32);
  background-image: repeating-linear-gradient(
    90deg,
    currentColor 0 1px, transparent 1px 3px,
    currentColor 3px 6px, transparent 6px 7px,
    currentColor 7px 8px, transparent 8px 11px,
    currentColor 11px 15px, transparent 15px 16px,
    currentColor 16px 17px, transparent 17px 20px,
    currentColor 20px 22px, transparent 22px 25px,
    currentColor 25px 26px, transparent 26px 30px,
    currentColor 30px 33px, transparent 33px 34px
  );
}

.specs {
  margin: 28px 0 0;
  padding: 0;
  list-style: none;
  border-top: 1px solid rgba(255, 255, 255, 0.09);
}
.specs li {
  display: flex;
  align-items: baseline;
  gap: 16px;
  padding: 11px 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.09);
}
.specs .k {
  flex: none;
  width: 78px;
  font-size: 10.5px;
  letter-spacing: 0.12em;
  color: rgba(255, 255, 255, 0.55);
}
/* 每行前置一个信号色方块，像设备面板上的状态灯 */
.specs .k::before {
  content: "";
  display: inline-block;
  width: 4px;
  height: 4px;
  margin-right: 8px;
  vertical-align: 1px;
  background: var(--signal);
}
.specs .v {
  font-size: 13px;
  color: rgba(255, 255, 255, 0.72);
}

.brand-foot {
  margin: 0;
  font-size: 10.5px;
  letter-spacing: 0.1em;
  color: rgba(255, 255, 255, 0.46);
}

/* ── 右侧表单面板 ─────────────────────────────────────────────────── */
.auth {
  display: flex;
  padding: 48px 32px;
  overflow-y: auto;
}
/* 用 margin:auto 居中：内容超高时不会被 flex 居中裁掉顶部 */
.auth-col {
  margin: auto;
  width: 100%;
  max-width: 344px;
}

.auth-head {
  display: none;
  align-items: center;
  gap: 12px;
  margin-bottom: 34px;
}
.auth-head-logo {
  display: block;
  height: 52px;
  width: auto;
}
.auth-head b {
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 0.06em;
  color: var(--ink);
}

.auth-body .eyebrow {
  font-size: 10.5px;
  letter-spacing: 0.22em;
  text-transform: uppercase;
  color: var(--steel-600);
}

.auth-title {
  margin: 16px 0 0;
  font-size: 30px;
  font-weight: 700;
  letter-spacing: 0.02em;
  color: var(--ink);
}

.auth-sub {
  margin: 10px 0 30px;
  font-size: 13px;
  color: var(--steel-600);
}

.auth-body :deep(.el-form-item) { margin-bottom: 14px; }

/* 输入框：2px 硬边，聚焦时信号黄描边 + 柔和外扩环 */
.auth-body :deep(.el-input__wrapper) {
  border-radius: 2px;
  background: #fff;
  box-shadow: inset 0 0 0 1px var(--steel-200);
  transition: box-shadow 0.16s ease;
}
.auth-body :deep(.el-input__wrapper:hover) {
  box-shadow: inset 0 0 0 1px var(--steel-400);
}
.auth-body :deep(.el-input__wrapper.is-focus) {
  box-shadow: inset 0 0 0 1px var(--signal), 0 0 0 3px rgba(245, 179, 1, 0.15);
}
.auth-body :deep(.el-input__prefix) { color: var(--steel-400); }
.auth-body :deep(.el-input__inner) { color: var(--ink); }
/* 无可见标签，placeholder 就是唯一的字段说明，对比度不能压到 4.5:1 以下 */
.auth-body :deep(.el-input__inner::placeholder) { color: var(--steel-500); }

/* 主按钮：石墨黑底 → hover 翻转为信号黄底黑字 */
.submit {
  width: 100%;
  margin-top: 8px;
  border-radius: 2px;
  font-weight: 600;
  letter-spacing: 0.18em;
  /* EP 把 .el-button--large 的高度硬编码成 40px（不读 --el-component-size-large），
     所以这里必须单独给 --el-button-size，才能和 48px 的输入框等高。 */
  --el-button-size: 48px;
  --el-button-bg-color: var(--ink);
  --el-button-border-color: var(--ink);
  --el-button-text-color: #fff;
  --el-button-hover-bg-color: var(--signal);
  --el-button-hover-border-color: var(--signal);
  --el-button-hover-text-color: var(--ink);
  --el-button-active-bg-color: var(--signal-deep);
  --el-button-active-border-color: var(--signal-deep);
  --el-button-active-text-color: var(--ink);
}

.auth-hint {
  display: flex;
  align-items: flex-start;
  gap: 9px;
  margin: 22px 0 0;
  padding-top: 18px;
  border-top: 1px solid var(--steel-200);
  font-size: 12px;
  line-height: 1.6;
  color: var(--steel-600);
}
.hint-bar {
  flex: none;
  width: 3px;
  height: 13px;
  margin-top: 3px;
  background: var(--signal);
}

@media (prefers-reduced-motion: no-preference) {
  .auth-col {
    animation: login-rise 0.42s cubic-bezier(0.2, 0.7, 0.3, 1) both;
  }
  @keyframes login-rise {
    from { opacity: 0; transform: translateY(10px); }
  }
}

/* 窄屏或矮屏（横屏手机）：隐藏品牌面板，改由 .auth-head 承担标识 */
@media (max-width: 900px), (max-height: 620px) {
  .login-page { grid-template-columns: minmax(0, 1fr); }
  .brand { display: none; }
  .auth {
    padding: 56px 22px 40px;
  }
  .auth-head { display: flex; }
  .auth-title { font-size: 26px; }
  .auth-sub { margin-bottom: 24px; }
}

/* 高度紧张但仍是双栏时，收紧品牌面板的纵向节奏 */
@media (min-width: 901px) and (max-height: 780px) {
  .brand { padding: 40px 40px 28px; }
  .brand-title { font-size: 34px; }
  .brand-desc { margin-top: 14px; }
  .barcode { margin-top: 18px; }
  .specs { margin-top: 20px; }
  .specs li { padding: 9px 0; }
}
</style>
