import { ref } from "vue";

// 横屏手机只按宽度判断会掉回桌面布局（如 iPhone 14 Pro Max 横屏 932×430，高度不足 520），
// 故补一条 max-height 兜住。
const QUERY = "(max-width: 767px), (max-height: 520px)";

const mql = window.matchMedia(QUERY);
const isMobile = ref(mql.matches);

function onChange(e: MediaQueryListEvent) {
  isMobile.value = e.matches;
}

// 老旧 Android WebView（Chromium < 84）没有 addEventListener，只有废弃的 addListener。
if (typeof mql.addEventListener === "function") {
  mql.addEventListener("change", onChange);
} else {
  (mql as any).addListener(onChange);
}

export function useIsMobile() {
  return isMobile;
}
