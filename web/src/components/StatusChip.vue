<template>
  <span class="chip" :class="`is-${variant}`">
    <i class="dot" aria-hidden="true" />
    <span class="txt">{{ text }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { REPAIR_STATUS_LABELS, STATUS_TAG, repairStatusTag } from "../api/meta";

type Variant = "ok" | "ink" | "warn" | "inf" | "bad" | "sig";

/**
 * EP 的 tag type → chip 变体。
 *
 * ⚠️ 状态的**映射关系仍然住在 `api/meta.ts`**（`STATUS_TAG` / `REPAIR_STATUS_TAG`），
 * 本组件只做「EP tag 类型 → 呈现变体」这一次翻译，没有重述也没有改动任何映射。
 *
 * 改动的只有呈现形态：实心 `el-tag` 在高密度表格里形成彩虹噪声（一屏 12 行 × 6 种状态），
 * 改成「白底 + 1px 同色描边 + 6px 状态点」后色彩信息仍在，但退到背景层。
 */
const TAG_TO_VARIANT: Record<string, Variant> = {
  success: "ok",
  primary: "ink",
  warning: "warn",
  info: "inf",
  danger: "bad",
};

const props = withDefaults(
  defineProps<{
    /** `asset` 传中文状态（如「在用」）；`repair` 传英文码（如 `pending`） */
    kind?: "asset" | "repair";
    value: string;
    /** 覆盖显示文案。维修单列表直接沿用后端给的 `status_label`。 */
    label?: string;
  }>(),
  { kind: "asset", label: "" },
);

const variant = computed<Variant>(() => {
  if (props.kind === "repair") {
    // 唯一的例外，而且是刻意的：待确认是**唯一一个需要当前用户动手**的状态
    // （发起人确认完工），用全站唯一的强调色把它从其余 9 个状态里拎出来。
    if (props.value === "confirming") return "sig";
    return TAG_TO_VARIANT[repairStatusTag(props.value)] ?? "inf";
  }
  return TAG_TO_VARIANT[STATUS_TAG[props.value]] ?? "inf";
});

const text = computed(() => {
  if (props.label) return props.label;
  if (props.kind === "repair") return REPAIR_STATUS_LABELS[props.value] || props.value;
  return props.value;
});
</script>

<style scoped>
.chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 20px;
  padding: 0 7px;
  background: var(--surface);
  border: 1px solid var(--steel-200);
  border-radius: var(--r);
  font-size: var(--t-12);
  line-height: 1;
  white-space: nowrap;
  /* 文案统一钢灰（白底 9.7:1）：色相信息交给状态点与描边承载，正文保持可读性 */
  color: var(--steel-700);
}

.dot {
  flex: 0 0 auto;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--steel-300);
}

/* 语义色只出现在 6px 状态点与 1px 描边上（图形元素 3:1 门槛，全部达标） */
.is-ok {
  border-color: var(--ok);
}
.is-ok .dot {
  background: var(--ok);
}

.is-ink {
  border-color: var(--ink);
}
.is-ink .dot {
  background: var(--ink);
}

.is-warn {
  border-color: var(--warn);
}
.is-warn .dot {
  background: var(--warn);
}

.is-inf {
  border-color: var(--info);
}
.is-inf .dot {
  background: var(--info);
}

.is-bad {
  border-color: var(--danger);
}
.is-bad .dot {
  background: var(--danger);
}

/* 待确认：唯一一个填充底色的 chip。信号黄底 + 墨字 = 9.7:1，
   同时靠「唯一的实心块」在整列描边 chip 里一眼可辨。 */
.is-sig {
  background: var(--signal);
  border-color: var(--signal-d);
  color: var(--ink);
  font-weight: 600;
}
.is-sig .dot {
  background: var(--ink);
}
</style>
