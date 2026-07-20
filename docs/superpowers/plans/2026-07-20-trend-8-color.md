# Trend Analysis 8-Color Palette & Interactive Enhancements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the Usage Statistics Trend Analysis chart from a 2-color palette (top-4 models, others gray) to an 8-color qualitative palette (top-8 models, rank-9+ gray) with an interactive ECharts legend, value-sorted tooltip, and hover-focus dimming.

**Architecture:** Introduce a new pure-function module `src/utils/usage/chartPalette.ts` that owns the 8-color palette, the `getRankColor` rank→color mapping, and `buildAreaGradient`. Extend `chartConfig.ts` (the ECharts option builder) with a legend block, a custom tooltip formatter, and per-series emphasis/blur. Update `UsageChart.tsx` to consume `getRankColor`. Bump `DEFAULT_TOP_N` from 4 to 8 in `UsagePage.tsx`.

**Tech Stack:** React 19, TypeScript, ECharts 6 (`echarts`), Vite, Vitest, i18next (no new keys), SCSS modules (no SCSS changes).

**Reference spec:** `docs/superpowers/specs/2026-07-20-trend-8-color-design.md`

**Working directory for commands:** `Cli-Proxy-API-Management-Center/` (the frontend subproject).

---

## File Structure

| Path | Action | Responsibility |
|---|---|---|
| `src/utils/usage/chartPalette.ts` | **create** | 8-color palette, `getRankColor`, `buildAreaGradient` |
| `src/utils/usage/chartPalette.test.ts` | **create** | unit tests for `getRankColor` and `buildAreaGradient` |
| `src/components/usage/UsageChart.tsx` | edit | drop local `CHART_COLORS`; use `getRankColor(i)` |
| `src/components/usage/UsageChart.test.tsx` | **create** | assert dataset borderColor at rank 0 / rank 8 |
| `src/utils/usage/chartConfig.ts` | edit | add legend block, tooltip formatter, emphasis/blur |
| `src/utils/usage/chartConfig.test.ts` | edit | extend cases for 8-series legend, tooltip sort, selector, emphasis |
| `src/pages/UsagePage.tsx` | edit | introduce `DEFAULT_TOP_N = 8`; replace `.slice(0, 4)` × 2 |

**Files explicitly NOT changed** (per spec §9):
- `src/components/usage/CostTrendChart.tsx`
- `src/components/usage/ChartLineSelector.tsx`
- `src/components/usage/TrendTabsCard.tsx`
- `src/pages/UsagePage.module.scss`
- `src/utils/echarts/registerThemes.ts`
- All `src/i18n/locales/*.json`

---

## Task 1: Create `chartPalette.ts` with palette + `getRankColor` + `buildAreaGradient`

**Files:**
- Create: `Cli-Proxy-API-Management-Center/src/utils/usage/chartPalette.ts`
- Test: `Cli-Proxy-API-Management-Center/src/utils/usage/chartPalette.test.ts`

- [ ] **Step 1: Write the failing test**

Create `src/utils/usage/chartPalette.test.ts` with the following content:

```ts
import { describe, it, expect } from 'vitest';
import {
  getRankColor,
  buildAreaGradient,
  MODEL_TREND_PALETTE,
  TAIL_LINE_COLOR,
} from './chartPalette';

describe('getRankColor', () => {
  it('returns the palette color for rank 0..7', () => {
    for (let i = 0; i < MODEL_TREND_PALETTE.length; i++) {
      expect(getRankColor(i)).toBe(MODEL_TREND_PALETTE[i]);
    }
  });

  it('returns TAIL_LINE_COLOR for rank >= 8', () => {
    expect(getRankColor(8)).toBe(TAIL_LINE_COLOR);
    expect(getRankColor(99)).toBe(TAIL_LINE_COLOR);
  });

  it('returns TAIL_LINE_COLOR for invalid inputs', () => {
    expect(getRankColor(-1)).toBe(TAIL_LINE_COLOR);
    expect(getRankColor(1.5)).toBe(TAIL_LINE_COLOR);
    expect(getRankColor(Number.NaN)).toBe(TAIL_LINE_COLOR);
    expect(getRankColor(Number.POSITIVE_INFINITY)).toBe(TAIL_LINE_COLOR);
  });
});

describe('buildAreaGradient', () => {
  it('returns a CSS linear-gradient string', () => {
    const result = buildAreaGradient('#3B82F6');
    expect(result).toContain('linear-gradient');
    expect(result).toContain('#3B82F6');
  });

  it('respects opacityTop and opacityBottom overrides', () => {
    const result = buildAreaGradient('#10B981', 0.5, 0.1);
    expect(result).toContain('0.5');
    expect(result).toContain('0.1');
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/utils/usage/chartPalette.test.ts
```

Expected: FAIL with `Cannot find module './chartPalette'` (the module does not exist yet).

- [ ] **Step 3: Implement `chartPalette.ts`**

Create `src/utils/usage/chartPalette.ts`:

```ts
/**
 * 8-color qualitative palette for the trend analysis chart.
 * Index = rank (0 = top model by total spend, 7 = 8th model).
 * Indices >= 8 use TAIL_LINE_COLOR (slate, semi-transparent).
 *
 * Colors are picked for high mutual distinguishability in both light and
 * dark themes. Top-1 uses a thicker line + larger hover point as a
 * secondary cue for color-blind accessibility.
 */
export const MODEL_TREND_PALETTE: readonly string[] = [
  '#3B82F6', // Top 1 — 科技蓝
  '#10B981', // Top 2 — 翡翠绿
  '#8B5CF6', // Top 3 — 皇家紫
  '#F59E0B', // Top 4 — 琥珀橙
  '#EC4899', // Top 5 — 魅惑粉
  '#06B6D4', // Top 6 — 青天蓝
  '#F43F5E', // Top 7 — 玫瑰红
  '#64748B', // Top 8 — 石板灰
] as const;

/** Color used for any series whose rank is >= MODEL_TREND_PALETTE.length. */
export const TAIL_LINE_COLOR = 'rgba(148, 163, 184, 0.55)';
export const TAIL_LINE_AREA_COLOR = 'rgba(148, 163, 184, 0.06)';

/**
 * Pure: rank (0-indexed) → color string.
 * Returns TAIL_LINE_COLOR for invalid input (negative, non-integer, NaN, ±Infinity)
 * and for rank >= MODEL_TREND_PALETTE.length.
 */
export function getRankColor(rank: number): string {
  if (!Number.isFinite(rank) || !Number.isInteger(rank) || rank < 0) {
    return TAIL_LINE_COLOR;
  }
  if (rank < MODEL_TREND_PALETTE.length) {
    return MODEL_TREND_PALETTE[rank];
  }
  return TAIL_LINE_COLOR;
}

/**
 * Build a CSS linear-gradient suitable for ECharts areaStyle.color.
 * Migrated from utils/usage.ts so it lives next to the palette it derives from.
 */
export function buildAreaGradient(
  hex: string,
  opacityTop: number = 0.33,
  opacityBottom: number = 0,
): string {
  const normalized = hex.startsWith('#') ? hex.slice(1) : hex;
  const expanded =
    normalized.length === 3
      ? normalized
          .split('')
          .map((c) => c + c)
          .join('')
      : normalized;
  const r = parseInt(expanded.slice(0, 2), 16);
  const g = parseInt(expanded.slice(2, 4), 16);
  const b = parseInt(expanded.slice(4, 6), 16);
  const top = `rgba(${r}, ${g}, ${b}, ${opacityTop})`;
  const bottom = `rgba(${r}, ${g}, ${b}, ${opacityBottom})`;
  return `linear-gradient(180deg, ${top} 0%, ${bottom} 100%)`;
}
```

- [ ] **Step 4: Re-export `buildAreaGradient` from `utils/usage.ts` for back-compat**

Open `src/utils/usage.ts`. Find the existing `buildAreaGradient` function (it is the original implementation that the migrated version replaces). Replace its body with a re-export of the new implementation:

```ts
// Re-export from the consolidated chartPalette module.
// The original buildAreaGradient lived here; it now lives in
// utils/usage/chartPalette.ts alongside MODEL_TREND_PALETTE.
export { buildAreaGradient } from './usage/chartPalette';
```

Leave any other export of the same name (if a default export alias existed) untouched — only replace the implementation declaration.

- [ ] **Step 5: Run the test to verify it passes**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/utils/usage/chartPalette.test.ts
```

Expected: PASS — all 5 tests green.

- [ ] **Step 6: Run lint + type-check**

```bash
cd Cli-Proxy-API-Management-Center && npm run lint && npm run type-check
```

Expected: 0 errors. If `buildAreaGradient` is reported as duplicate-export, verify only one `export` statement remains in `utils/usage.ts` (the re-export).

- [ ] **Step 7: Commit**

```bash
cd Cli-Proxy-API-Management-Center && git add src/utils/usage/chartPalette.ts src/utils/usage/chartPalette.test.ts src/utils/usage.ts && git commit -m "feat(usage): add 8-color trend palette with getRankColor helper"
```

---

## Task 2: Update `UsageChart.tsx` to consume `getRankColor`

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/components/usage/UsageChart.tsx:50-57, 322-351` (approximate lines; verify with grep before editing)
- Test: `Cli-Proxy-API-Management-Center/src/components/usage/UsageChart.test.tsx` (new)

- [ ] **Step 1: Locate the current color assignment in `UsageChart.tsx`**

```bash
cd Cli-Proxy-API-Management-Center && grep -n "CHART_COLORS\|TAIL_LINE_COLOR\|isPrimary\|isSecondary" src/components/usage/UsageChart.tsx
```

Expected output: a block at L50-57 defining `CHART_COLORS` and a block at L322-351 (approximate) reading from those constants.

- [ ] **Step 2: Write the failing test**

Create `src/components/usage/UsageChart.test.tsx`:

```tsx
import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/react';
import { UsageChart } from './UsageChart';
import { TAIL_LINE_COLOR, MODEL_TREND_PALETTE } from '@/utils/usage/chartPalette';
import { buildTrendChartData } from '@/utils/usage';
import type { UsageDetailsResponse } from '@/types/usage';

const baseUsage: UsageDetailsResponse = {
  fetchedAt: '2026-07-20T00:00:00Z',
  details: Array.from({ length: 9 }, (_, i) => ({
    timestamp: `2026-07-20T0${i}:00:00Z`,
    model: `model-${i + 1}`,
    requests: (i + 1) * 10,
    inputTokens: 0,
    outputTokens: 0,
    reasoningTokens: 0,
    cachedTokens: 0,
    totalTokens: 0,
    cost: 0,
    failed: false,
  })),
};

describe('UsageChart dataset colors', () => {
  it('assigns the first 8 ranks to MODEL_TREND_PALETTE and rank 8+ to TAIL_LINE_COLOR', () => {
    const models = Array.from({ length: 9 }, (_, i) => `model-${i + 1}`);
    const data = buildTrendChartData(baseUsage, 'hour', 'requests', models);
    expect(data.datasets).toHaveLength(9);
    expect(data.datasets[0].borderColor).toBe(MODEL_TREND_PALETTE[0]);
    expect(data.datasets[7].borderColor).toBe(MODEL_TREND_PALETTE[7]);
    expect(data.datasets[8].borderColor).toBe(TAIL_LINE_COLOR);
  });
});
```

If `buildTrendChartData` is not exported from `@/utils/usage`, adjust the import path after `grep`-ing for it:

```bash
cd Cli-Proxy-API-Management-Center && grep -rn "export.*buildTrendChartData" src/
```

If `UsageDetailsResponse` lives elsewhere, replace the import with the actual type path discovered via the grep above.

- [ ] **Step 3: Run the test to verify it fails**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/components/usage/UsageChart.test.tsx
```

Expected: FAIL — current implementation only sets `borderColor` to one of 2 palette entries or `TAIL_LINE_COLOR`; rank 7 will be gray, not `MODEL_TREND_PALETTE[7]`.

- [ ] **Step 4: Edit `UsageChart.tsx`**

Replace the local palette constants and their usage. Concretely:

1. Remove the existing `CHART_COLORS = ['#00E5FF', '#7C4DFF']` and the local `TAIL_LINE_COLOR` constants at L50-51.

2. Add an import at the top of the file:

```ts
import {
  getRankColor,
  TAIL_LINE_COLOR,
  buildAreaGradient,
} from '@/utils/usage/chartPalette';
```

3. In the function that builds each dataset (the block at L322-351), replace the rank→color logic. The exact code depends on the existing local variables; below is the target behavior:

```ts
// Pseudocode — adapt variable names to the existing local names.
const rankInLines = lines.indexOf(modelId);
const color = getRankColor(rankInLines); // 0..7 → palette; >=8 → gray
const isTop = rankInLines === 0;

const dataset: ChartDataset = {
  label: modelId,
  data: series,
  borderColor: color,
  backgroundColor: buildAreaGradient(color),
  fill: true,
  tension: 0.35,
  borderWidth: isTop ? 2 : 1.5,
  pointHoverRadius: isTop ? 6 : 4,
  // preserve any other existing fields
};
```

4. Leave `TOKEN_FOCUS_CHART_COLORS` (the focused single-model view's 4-color set) untouched.

- [ ] **Step 5: Re-run the test**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/components/usage/UsageChart.test.tsx
```

Expected: PASS — `datasets[0].borderColor === '#3B82F6'`, `datasets[7] === '#64748B'`, `datasets[8] === TAIL_LINE_COLOR`.

- [ ] **Step 6: Lint + type-check**

```bash
cd Cli-Proxy-API-Management-Center && npm run lint && npm run type-check
```

Expected: 0 errors. If the unused `CHART_COLORS` import warning surfaces, verify you removed all references.

- [ ] **Step 7: Commit**

```bash
cd Cli-Proxy-API-Management-Center && git add src/components/usage/UsageChart.tsx src/components/usage/UsageChart.test.tsx && git commit -m "feat(usage): color trend chart by rank via getRankColor"
```

---

## Task 3: Add ECharts legend block + per-series emphasis/blur in `chartConfig.ts`

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/utils/usage/chartConfig.ts:27-86`

- [ ] **Step 1: Locate the series loop and confirm `TooltipParam` is the existing type alias**

```bash
cd Cli-Proxy-API-Management-Center && grep -n "TooltipParam\|series: data\.datasets" src/utils/usage/chartConfig.ts
```

- [ ] **Step 2: Extend the chart-config test (write first)**

Open `src/utils/usage/chartConfig.test.ts` and append the following cases to the existing `describe` block. If the file uses a different describe name, add a new describe block:

```ts
import { describe, it, expect } from 'vitest';
import { buildEChartsTrendOption } from './chartConfig';

describe('buildEChartsTrendOption - 8 series with palette', () => {
  const theme = {
    text: '#e5e7eb',
    textMuted: '#9ca3af',
    tooltipBg: '#111827',
    tooltipBorder: '#374151',
  } as const;

  const data = {
    labels: ['00:00', '01:00', '02:00'],
    datasets: Array.from({ length: 8 }, (_, i) => ({
      label: `model-${i + 1}`,
      data: [i + 1, (i + 1) * 2, (i + 1) * 3],
      borderColor: ['#3B82F6', '#10B981', '#8B5CF6', '#F59E0B',
                    '#EC4899', '#06B6D4', '#F43F5E', '#64748B'][i],
      backgroundColor: 'transparent',
      fill: true,
      tension: 0.35,
    })),
  };

  it('returns a legend with 8 entries and built-in all/inverse selector', () => {
    const option = buildEChartsTrendOption(data, theme);
    const legend = option.legend as { data: string[]; selector: string[] };
    expect(legend.data).toHaveLength(8);
    expect(legend.selector).toEqual(expect.arrayContaining(['all', 'inverse']));
  });

  it('configures per-series emphasis.focus and blur.opacity', () => {
    const option = buildEChartsTrendOption(data, theme);
    const series = option.series as Array<{
      emphasis: { focus: string };
      blur: { lineStyle: { opacity: number } };
    }>;
    expect(series).toHaveLength(8);
    for (const s of series) {
      expect(s.emphasis.focus).toBe('series');
      expect(s.blur.lineStyle.opacity).toBe(0.15);
    }
  });
});
```

- [ ] **Step 3: Run the new cases; expect failure**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/utils/usage/chartConfig.test.ts
```

Expected: at least one new FAIL — `legend.selector` undefined, `emphasis.focus` undefined, or `blur.lineStyle.opacity` undefined.

- [ ] **Step 4: Edit `chartConfig.ts`**

Make three changes inside `buildEChartsTrendOption(data, themeColors)`:

**Change A — add the legend block** in the returned option object, alongside the existing `tooltip` block:

```ts
legend: {
  type: 'scroll',         // safety: handles > 8 entries if rank cap is later raised
  orient: 'horizontal',
  top: 8,
  left: 'center',
  itemWidth: 14,
  itemHeight: 8,
  itemGap: 14,
  textStyle: { color: themeColors.text, fontSize: 12 },
  pageIconColor: themeColors.textMuted,
  pageTextStyle: { color: themeColors.textMuted },
  data: data.datasets.map((d) => d.label),
  selector: ['all', 'inverse'], // ECharts built-in 全选 / 反选
  selectorLabel: {
    color: themeColors.textMuted,
    borderColor: themeColors.tooltipBorder,
  },
  selectorPosition: 'end',
},
```

**Change B — add `emphasis` and `blur` to each series entry.** In the existing `data.datasets.map((d, i) => ({...}))` block, extend each series with:

```ts
emphasis: { focus: 'series', lineStyle: { width: 3 } },
blur: {
  lineStyle: { opacity: 0.15 },
  itemStyle: { opacity: 0.15 },
},
```

Also, replace the static `lineStyle: { width: 2 }` with a rank-aware width:

```ts
lineStyle: { width: i === 0 ? 2 : 1.5, color: d.borderColor },
```

**Change C — do NOT touch the existing tooltip block yet.** It is replaced in Task 4. Leaving it intact keeps this commit small and the failing test from Task 3 narrower.

- [ ] **Step 5: Re-run the test**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/utils/usage/chartConfig.test.ts
```

Expected: the two new cases from Step 2 PASS; pre-existing tests still PASS.

- [ ] **Step 6: Lint + type-check**

```bash
cd Cli-Proxy-API-Management-Center && npm run lint && npm run type-check
```

Expected: 0 errors. If ECharts complains about `legend.type` being incompatible with `selector`, see ECharts 6 docs: `selector` is a top-level legend option since 5.x and is compatible with `type: 'scroll'`.

- [ ] **Step 7: Commit**

```bash
cd Cli-Proxy-API-Management-Center && git add src/utils/usage/chartConfig.ts src/utils/usage/chartConfig.test.ts && git commit -m "feat(usage): add ECharts legend with all/inverse selector and hover focus blur"
```

---

## Task 4: Replace tooltip with value-sorted formatter in `chartConfig.ts`

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/utils/usage/chartConfig.ts` (existing `tooltip` block)
- Modify: `Cli-Proxy-API-Management-Center/src/utils/usage/chartConfig.test.ts`

- [ ] **Step 1: Inspect existing tooltip block and confirm helpers**

```bash
cd Cli-Proxy-API-Management-Center && grep -n "tooltip\|escapeHtml\|formatNum\|formatUsd" src/utils/usage/chartConfig.ts | head -30
```

Confirm whether `escapeHtml` and `formatNum` already exist as module-local helpers. If they do, skip to Step 3.

- [ ] **Step 2: Add missing helpers (only if Step 1 shows they don't exist)**

Add at the top of `chartConfig.ts`, after imports:

```ts
function escapeHtml(s: string): string {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

const compactNumberFormatter = new Intl.NumberFormat('en-US', {
  notation: 'compact',
  maximumFractionDigits: 1,
});

function formatNum(n: number): string {
  if (!Number.isFinite(n)) return '0';
  return compactNumberFormatter.format(n);
}
```

Adjust `formatNum` if a richer formatter (e.g. unit-aware) already lives in the project; the test below only checks that the output is a string.

- [ ] **Step 3: Write the failing tooltip test**

Append to `src/utils/usage/chartConfig.test.ts`:

```ts
describe('buildEChartsTrendOption - tooltip formatter', () => {
  const theme = {
    text: '#e5e7eb',
    textMuted: '#9ca3af',
    tooltipBg: '#111827',
    tooltipBorder: '#374151',
  } as const;

  const data = {
    labels: ['00:00'],
    datasets: [
      { label: 'low',  data: [1],   borderColor: '#3B82F6', backgroundColor: '', fill: true, tension: 0.35 },
      { label: 'high', data: [100], borderColor: '#10B981', backgroundColor: '', fill: true, tension: 0.35 },
      { label: 'mid',  data: [50],  borderColor: '#8B5CF6', backgroundColor: '', fill: true, tension: 0.35 },
    ],
  };

  it('sorts rows by descending value and includes color + percentage', () => {
    const option = buildEChartsTrendOption(data, theme);
    const tooltip = option.tooltip as {
      formatter: (params: unknown) => string;
    };
    const params = [
      { seriesName: 'low',  value: 1,   color: '#3B82F6', axisValueLabel: '00:00' },
      { seriesName: 'high', value: 100, color: '#10B981', axisValueLabel: '00:00' },
      { seriesName: 'mid',  value: 50,  color: '#8B5CF6', axisValueLabel: '00:00' },
    ];
    const html = tooltip.formatter(params as never);
    // Order: high (100), mid (50), low (1)
    const highIdx = html.indexOf('high');
    const midIdx  = html.indexOf('mid');
    const lowIdx  = html.indexOf('low');
    expect(highIdx).toBeGreaterThan(-1);
    expect(highIdx).toBeLessThan(midIdx);
    expect(midIdx).toBeLessThan(lowIdx);
    // Color swatch + percent markup present
    expect(html).toContain('#10B981');
    expect(html).toContain('66.7%'); // 100 / 151 ≈ 66.2; allow loose check
  });

  it('returns empty string for invalid params', () => {
    const option = buildEChartsTrendOption(data, theme);
    const tooltip = option.tooltip as { formatter: (p: unknown) => string };
    expect(tooltip.formatter([])).toBe('');
    expect(tooltip.formatter(null as never)).toBe('');
  });
});
```

- [ ] **Step 4: Run the test; expect failure**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/utils/usage/chartConfig.test.ts
```

Expected: FAIL — current tooltip has no `formatter` (or formatter does not sort / emit percentages).

- [ ] **Step 5: Replace the tooltip block in `chartConfig.ts`**

Find the existing tooltip configuration (lines 40-45 from spec §2). Replace its contents with:

```ts
tooltip: {
  trigger: 'axis',
  backgroundColor: themeColors.tooltipBg,
  borderColor: themeColors.tooltipBorder,
  textStyle: { color: themeColors.text },
  axisPointer: { type: 'line', lineStyle: { color: themeColors.tooltipBorder } },
  formatter: (params: TooltipParam[]) => {
    if (!Array.isArray(params) || params.length === 0) return '';
    const time = params[0].axisValueLabel ?? '';
    const total = params.reduce(
      (s, p) => s + (Number(p.value) || 0),
      0,
    );
    const sorted = [...params].sort(
      (a, b) => (Number(b.value) || 0) - (Number(a.value) || 0),
    );
    const rows = sorted
      .map((p) => {
        const v = Number(p.value) || 0;
        const pct = total > 0 ? ((v / total) * 100).toFixed(1) : '0.0';
        return `<div style="display:flex;align-items:center;gap:6px;margin:2px 0;">
          <span style="display:inline-block;width:10px;height:10px;border-radius:2px;background:${p.color};"></span>
          <span style="flex:1;">${escapeHtml(p.seriesName)}</span>
          <span style="font-variant-numeric:tabular-nums;">${formatNum(v)}</span>
          <span style="opacity:.7;min-width:42px;text-align:right;">${pct}%</span>
        </div>`;
      })
      .join('');
    return `<div style="font-size:12px;">
      <div style="margin-bottom:4px;font-weight:600;">${escapeHtml(time)}</div>
      ${rows}
      <div style="margin-top:4px;border-top:1px solid ${themeColors.tooltipBorder};padding-top:4px;display:flex;justify-content:space-between;">
        <span>Total</span><span style="font-variant-numeric:tabular-nums;">${formatNum(total)}</span>
      </div>
    </div>`;
  },
},
```

If `TooltipParam` is not exported, declare a local interface at the top of `chartConfig.ts`:

```ts
interface TooltipParam {
  seriesName: string;
  value: number | string;
  color: string;
  axisValueLabel?: string;
}
```

- [ ] **Step 6: Re-run tests**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/utils/usage/chartConfig.test.ts
```

Expected: PASS for both new cases. Pre-existing tests still PASS.

- [ ] **Step 7: Lint + type-check**

```bash
cd Cli-Proxy-API-Management-Center && npm run lint && npm run type-check
```

Expected: 0 errors.

- [ ] **Step 8: Commit**

```bash
cd Cli-Proxy-API-Management-Center && git add src/utils/usage/chartConfig.ts src/utils/usage/chartConfig.test.ts && git commit -m "feat(usage): value-sorted tooltip with color swatch and percentage"
```

---

## Task 5: Bump `DEFAULT_TOP_N` from 4 to 8 in `UsagePage.tsx`

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/pages/UsagePage.tsx:39, 681, 686`

- [ ] **Step 1: Locate the existing constants and the two `slice(0, 4)` call sites**

```bash
cd Cli-Proxy-API-Management-Center && grep -n "MAX_CHART_LINES\|slice(0, 4)" src/pages/UsagePage.tsx
```

Expected: `MAX_CHART_LINES = 9` on one line (around L39), two `slice(0, 4)` matches around L681 (credentials) and L686 (models).

- [ ] **Step 2: Add `DEFAULT_TOP_N` constant**

Right after the existing `MAX_CHART_LINES` declaration, add:

```ts
/** Number of models/credentials to auto-pick when the user has not customized chart lines. */
const DEFAULT_TOP_N = 8;
```

Do not delete `MAX_CHART_LINES = 9`. The two constants serve different purposes:
- `MAX_CHART_LINES` (9) — UI ceiling enforced by `ChartLineSelector`.
- `DEFAULT_TOP_N` (8) — number of series the page auto-selects by default.

- [ ] **Step 3: Replace both `slice(0, 4)` call sites**

Replace L681 (credentials):

```ts
// Before
credentialRows.filter((row) => row.requests > 0).slice(0, 4)

// After
credentialRows.filter((row) => row.requests > 0).slice(0, DEFAULT_TOP_N)
```

Replace L686 (models):

```ts
// Before
modelStats.slice(0, 4).map((stat) => stat.model)

// After
modelStats.slice(0, DEFAULT_TOP_N).map((stat) => stat.model)
```

- [ ] **Step 4: Lint + type-check**

```bash
cd Cli-Proxy-API-Management-Center && npm run lint && npm run type-check
```

Expected: 0 errors. No test changes here — the behavior is exercised by the existing dashboard render path; verification is via the manual checklist in Task 7.

- [ ] **Step 5: Commit**

```bash
cd Cli-Proxy-API-Management-Center && git add src/pages/UsagePage.tsx && git commit -m "feat(usage): default top-N models/credentials bumped from 4 to 8"
```

---

## Task 6: Full test suite + build

**Files:** none (verification only)

- [ ] **Step 1: Run the entire vitest suite**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run
```

Expected: all green. If any pre-existing test breaks, investigate the failure before proceeding — likely culprits: `UsageChart.test.tsx` (was nonexistent before; new test) or `chartConfig.test.ts` (extended).

- [ ] **Step 2: Run the production build**

```bash
cd Cli-Proxy-API-Management-Center && npm run build
```

Expected: tsc + vite build both succeed; `dist/index.html` is regenerated. Do NOT run `npm run deploy` — that copies `dist/index.html` to `../assets/management.html`, which is the manual deployment step (out of plan scope).

- [ ] **Step 3: Verify no new lint warnings**

```bash
cd Cli-Proxy-API-Management-Center && npm run lint
```

Expected: 0 errors and no new warnings compared to the pre-plan baseline.

- [ ] **Step 4: Commit (only if Step 1-3 surfaced any stray formatting/lock-file updates)**

```bash
cd Cli-Proxy-API-Management-Center && git status
```

If only `package-lock.json` or similar is dirty, commit with:

```bash
cd Cli-Proxy-API-Management-Center && git add -u && git commit -m "chore: post-build lockfile refresh"
```

If nothing changed, skip the commit.

---

## Task 7: Manual verification on the running dev server

**Files:** none

- [ ] **Step 1: Start the dev server**

```bash
cd Cli-Proxy-API-Management-Center && npm run dev
```

Expected: Vite reports a local URL (typically `http://localhost:5173`).

- [ ] **Step 2: Walk the manual checklist from spec §7**

Open the Usage Statistics page in a browser and verify, in order:

- [ ] Default page load shows **8 distinct colored series** in the trend chart. Top-1 is `#3B82F6` (blue), Top-2 `#10B981` (green), … Top-8 `#64748B` (slate).
- [ ] Hover any series → that line thickens; the other 7 fade to ~15% opacity.
- [ ] Click a legend item → that series hides/shows; chart re-renders.
- [ ] Click the legend's **全选** button → all hidden series reappear.
- [ ] Click the legend's **反选** button → visible/hidden states swap.
- [ ] Hover any time point → tooltip appears sorted by value desc, with color swatch + percentage on each row + a Total row at the bottom.
- [ ] Reload page with old `localStorage` data (4 ids) → still works, no migration error.
- [ ] Switch theme (light/dark) → legend text and tooltip background follow the theme colors.
- [ ] Switch to the **花费统计 / Cost Overview** tab → still shows the single cyan line, unchanged.

If any item fails, file a follow-up commit addressing the gap (no automated test exists for these visual checks yet).

- [ ] **Step 3: Stop the dev server**

Press Ctrl+C in the terminal running `npm run dev`.

---

## Self-Review

**Spec coverage** (from §1 Goal):

| Spec requirement | Task |
|---|---|
| 8-color palette, top-8 distinct hues | T1, T2 |
| `DEFAULT_TOP_N` 4 → 8 | T5 |
| Custom ECharts legend | T3 |
| Smart value-sorted tooltip | T4 |
| Hover-focus dimming | T3 |
| `maxLines` stays 9 | T5 (deliberately not changed) |
| Unit tests | T1, T2, T3, T4 |
| No CostTrendChart changes | T1 explicitly excludes; T2/T3/T4 only edit trend-config & trend-chart |
| No new i18n keys | T1-T5 do not edit i18n files |

**Placeholder scan:** No "TBD"/"TODO"/"implement later"/"fill in details" markers in any task. Step content is concrete (file paths, code blocks, exact commands with expected output).

**Type consistency check:**
- `getRankColor(rank: number): string` — defined in T1, consumed in T2.
- `TAIL_LINE_COLOR: string` — defined in T1, consumed in T2.
- `buildAreaGradient(hex, opacityTop?, opacityBottom?): string` — defined in T1, re-exported from `utils/usage.ts` (T1 Step 4), consumed in T2.
- `DEFAULT_TOP_N = 8` — defined in T5 Step 2, consumed in T5 Step 3 (×2).
- `TooltipParam` — declared in T4 Step 5 if missing; consumed in T4 formatter.
- `legend.selector === ['all', 'inverse']` — added in T3, asserted in T3's test.
- `emphasis.focus === 'series'`, `blur.lineStyle.opacity === 0.15` — added in T3, asserted in T3's test.
- `MODEL_TREND_PALETTE[i]` — defined in T1, asserted in T2's test at indices 0 and 7.

No inconsistencies found.

**Plan complete.** Saved to `docs/superpowers/plans/2026-07-20-trend-8-color.md`.