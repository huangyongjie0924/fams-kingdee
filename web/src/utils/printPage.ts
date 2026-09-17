import { onBeforeUnmount, onMounted } from "vue";

/**
 * `@page` 是全局规则，不能被子类选择器或 body class 限定作用域 —— 它对整个文档生效。
 * 盘点报表要 A4 横向、标签页要 A4 纵向，两边各写一条静态 `@page` 会在层叠里互相覆盖，
 * 谁后加载谁赢，而 SFC 样式的注入顺序不确定，于是报表可能莫名变成纵向。
 * 所以改成按路由动态注入，全文档同一时刻只留一条。
 *
 * 只删自己那一条：路由切换时新组件先挂载、旧组件后卸载，若旧组件无条件删掉
 * 「当前那一条」，会把新页面刚注入的样式一起带走。
 */
export function usePrintPage(css: string) {
  let mine: HTMLStyleElement | null = null;

  onMounted(() => {
    document.querySelectorAll("style[data-print-page]").forEach((n) => n.remove());
    mine = document.createElement("style");
    mine.dataset.printPage = "";
    mine.textContent = css;
    document.head.appendChild(mine);
  });

  onBeforeUnmount(() => {
    mine?.remove();
    mine = null;
  });
}

export const A4_PORTRAIT = "@page { size: A4 portrait; margin: 0 }";
export const A4_LANDSCAPE = "@page { size: A4 landscape; margin: 10mm }";
