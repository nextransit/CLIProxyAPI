# Trend Analysis 8-Color Palette & Interactive Enhancements — Design

**Status**: Approved (brainstorming complete)
**Date**: 2026-07-20
**Branch**: personal/offline-dev
**Author**: Brainstorming session

---

## 1. Goal

Increase the visual fidelity and readability of the **Trend Analysis** panel
(趋势分析) in the Usage Statistics page when more than 4 models are active.
Specifically:

- Extend the per-series color palette from **2 colors** (cyan + purple) to a
  **ranked 8-color qualitative palette**, so the top 8 models each render with
  a distinct hue rather than collapsing to gray.
- Raise the **default top-N model auto-selection** from 4 → 8 to match the
  expanded palette.
- Add **interactive affordances** for the now-denser line chart: a 2×4 grid
  legend, a smart tooltip sorted by value with per-row color swatches and
  percentages, and ECharts built-in **hover focus** that dims other lines.
- Keep **`maxLines` at 9** so the existing UI ceiling and i18n hint remain
  unchanged.
- Add **unit tests** for the new palette helper and the extended chart config.

### Out of Scope

- `CostTrendChart` (single-line cost overview) — keeps its dedicated cyan color.
- `UsagePage.module.scss` drawer styles — unchanged.
- Playwright visual regression — not added (per user decision).
- New i18n keys (ECharts built-in `selector: ['all', 'inverse']` covers the
  select-all/invert shortcut per user decision).

---

## 2. Current State (fact-based)

| Concern | Current behavior | Source |
|---|---|---|
| Top-N auto-pick | Hard-coded `.slice(0, 4)` × 2 places | `src/pages/UsagePage.tsx:681, 686` |
| Per-series color | `CHART_COLORS = ['#00E5FF', '#7C4DFF']` then `TAIL_LINE_COLOR` for rank ≥ 2 | `src/components/usage/UsageChart.tsx:50-51` |
| Custom legend | None — ECharts default disabled | `src/utils/usage/chartConfig.ts:27-86` |
| Tooltip formatter | ECharts default (no sort, no %) | `src/utils/usage/chartConfig.ts:40-45` |
| Hover focus | None | — |
| `maxLines` ceiling | `MAX_CHART_LINES = 9` (default 9) | `src/pages/UsagePage.tsx:39`, `src/components/usage/ChartLineSelector.tsx:35` |
| Persistence | `CHART_LINES_STORAGE_KEY` in `localStorage` | `src/pages/UsagePage.tsx:600-615` |
| Data sampling | `MAX_CHART_DETAILS = 5000` + `sampleDetails` + ECharts LTTB | `src/components/usage/UsageChart.tsx:49, 60-71`, `src/utils/usage/chartConfig.ts:68` |

---

## 3. Architecture

```
┌──────────────────────────────────────────────────────────────┐
│ UsagePage                                                    │
│  - DEFAULT_TOP_N = 8                                         │
│  - resolvedChartLines: top-N from modelStats.slice(0, N)    │
│  - chartLines (state, localStorage 持久化)                    │
└──────┬─────────────────────────────────────┬─────────────────┘
       │                                     │
       ▼                                     ▼
┌──────────────────┐              ┌────────────────────────┐
│ TrendTabsCard    │              │ ChartLineSelector      │
│ - header (徽章)  │              │ - 抽屉 (mode/list)     │
│ - tabs           │              │ (no changes — α path)  │
└──────┬───────────┘              └────────────────────────┘
       │
       ├─ UsageChart (Requests/Tokens tab)
       │    └─ buildTrendChartData → ChartData
       │       └─ each dataset.borderColor = getRankColor(rank)
       │
       └─ CostTrendChart (Cost tab) — keeps single #06b6d4

       ▼
┌──────────────────────────────────────────────┐
│ buildEChartsTrendOption (chartConfig.ts)     │
│  - legend: { type:'scroll', selector }       │
│  - tooltip: { formatter: sortByValueDesc }   │
│  - series: { emphasis.focus, blur.opacity }  │
└──────────────────────────────────────────────┘
```

### Component Responsibilities

| File | Responsibility | Touches |
|---|---|---|
| `src/utils/usage/chartPalette.ts` (NEW) | 8-color palette constants + `getRankColor(rank)` pure function + `buildAreaGradient(hex)` | new file |
| `src/utils/usage/chartConfig.ts` | ECharts option construction: legend, tooltip formatter, series emphasis/blur | edit |
| `src/components/usage/UsageChart.tsx` | Drop local `CHART_COLORS`; map `lines` index → `getRankColor(i)`; adjust line width by rank | edit |
| `src/pages/UsagePage.tsx` | Introduce `DEFAULT_TOP_N = 8`; replace `.slice(0, 4)` × 2 | edit |
| `src/utils/usage/chartPalette.test.ts` (NEW) | `getRankColor` boundary cases | new file |
| `src/utils/usage/chartConfig.test.ts` | Add cases for 8-series legend, tooltip sort, selector presence | edit |

### Why a New `chartPalette.ts`

Putting palette + helper in `utils/usage/chartPalette.ts`:

- Single source of truth (avoids the current split where `CHART_COLORS` lives
  in `UsageChart.tsx` and an unused 9-color array lives at
  `src/utils/usage.ts:1615-1625`).
- Pure-function export → trivial to unit-test.
- Co-located with `buildAreaGradient` (migrated from `utils/usage.ts`) so
  gradient logic lives next to the colors it consumes.

---

## 4. Data Flow

1. `UsagePage` aggregates `modelStats` and `credentialRows` from usage data.
2. `resolvedChartLines` memo (with `DEFAULT_TOP_N = 8`) picks the top-N lines.
3. The list flows into `<TrendTabsCard>` → `<UsageChart>`.
4. `UsageChart` calls `buildTrendChartData(usageData, period, metric, lines)`,
   producing a `ChartData` whose datasets correspond one-to-one with `lines`.
5. For each dataset `i`, `borderColor = getRankColor(i)` and `areaStyle` is
   `buildAreaGradient(getRankColor(i))`.
6. `buildEChartsTrendOption(data, themeColors)` returns the ECharts option,
   including the new legend block, tooltip formatter, and emphasis/blur.
7. ECharts renders. On legend click, the chart's visibility toggles. On hover,
   `emphasis.focus = 'series'` highlights the line while `blur.opacity = 0.15`
   dims all others.

### Stale-data and Rank Stability

`lines` is sorted by total spend in `resolvedChartLines` (existing logic).
As long as the rank order matches the dataset order, the visual color ranking
stays consistent across the requests / tokens / cost tabs.

---

## 5. API Design

### 5.1 `src/utils/usage/chartPalette.ts`

```ts
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

export const TAIL_LINE_COLOR = 'rgba(148, 163, 184, 0.55)';
export const TAIL_LINE_AREA_COLOR = 'rgba(148, 163, 184, 0.06)';

/** Pure: rank (0-indexed) → color. rank ≥ 8 or invalid → TAIL_LINE_COLOR. */
export function getRankColor(rank: number): string;

/** Migrated from utils/usage.ts. Linear gradient based on hex color. */
export function buildAreaGradient(
  hex: string,
  opacityTop?: number,   // default 0.33
  opacityBottom?: number, // default 0
): string;
```

### 5.2 `src/utils/usage/chartConfig.ts` additions

#### Legend block (added to `buildEChartsTrendOption` return)

```ts
legend: {
  type: 'scroll',            // safety for > 8 entries
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

#### Tooltip formatter (replaces default)

```ts
tooltip: {
  trigger: 'axis',
  backgroundColor: themeColors.tooltipBg,
  borderColor: themeColors.tooltipBorder,
  textStyle: { color: themeColors.text },
  axisPointer: { type: 'line', lineStyle: { color: themeColors.tooltipBorder } },
  formatter: (params: TooltipParam[]) => {
    if (!Array.isArray(params) || params.length === 0) return '';
    const time = params[0].axisValueLabel;
    const total = params.reduce((s, p) => s + (Number(p.value) || 0), 0);
    const sorted = [...params].sort(
      (a, b) => (Number(b.value) || 0) - (Number(a.value) || 0),
    );
    const rows = sorted.map((p) => {
      const v = Number(p.value) || 0;
      const pct = total > 0 ? ((v / total) * 100).toFixed(1) : '0.0';
      return `<div style="display:flex;align-items:center;gap:6px;margin:2px 0;">
        <span style="display:inline-block;width:10px;height:10px;border-radius:2px;background:${p.color};"></span>
        <span style="flex:1;">${escapeHtml(p.seriesName)}</span>
        <span style="font-variant-numeric:tabular-nums;">${formatNum(v)}</span>
        <span style="opacity:.7;min-width:42px;text-align:right;">${pct}%</span>
      </div>`;
    }).join('');
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

#### Series emphasis + blur (added per-series)

```ts
series: data.datasets.map((d, i) => ({
  name: d.label,
  type: 'line',
  smooth: true,
  showSymbol: false,
  sampling: 'lttb',
  lineStyle: { width: i === 0 ? 2 : 1.5, color: d.borderColor },
  itemStyle: { color: d.borderColor },
  areaStyle: {
    origin: 'start',
    color: buildAreaGradient(d.borderColor as string),
  },
  data: d.data,
  emphasis: { focus: 'series', lineStyle: { width: 3 } },
  blur: {
    lineStyle: { opacity: 0.15 },
    itemStyle: { opacity: 0.15 },
  },
})),
```

`formatNum`, `escapeHtml`, `formatUsd` are existing helpers reused from
`chartConfig.ts`; if `formatNum` is not yet defined, add a minimal
`Intl.NumberFormat`-based implementation in the same file.

### 5.3 `src/components/usage/UsageChart.tsx` changes

- Remove `CHART_COLORS` and the local `TAIL_LINE_COLOR`.
- Import `getRankColor`, `TAIL_LINE_COLOR`, `buildAreaGradient` from
  `@/utils/usage/chartPalette`.
- Replace the `isPrimary`/`isSecondary` ternary with `color = getRankColor(i)`,
  where `i = lines.indexOf(modelId)`.
- Leave `TOKEN_FOCUS_CHART_COLORS` untouched (focused single-model view).
- `pointHoverRadius`: rank 0 → 6, others → 4.

### 5.4 `src/pages/UsagePage.tsx` changes

```ts
const DEFAULT_TOP_N = 8;

// L681 (credentials)
credentialRows.filter((r) => r.requests > 0).slice(0, DEFAULT_TOP_N)

// L686 (models)
modelStats.slice(0, DEFAULT_TOP_N).map((stat) => stat.model)
```

`MAX_CHART_LINES = 9` and the i18n hint `chart_line_hint` are unchanged.

---

## 6. Error Handling & Edge Cases

| Case | Behavior |
|---|---|
| `lines` length > 8 (e.g. user manually selects 9) | First 8 use palette; index ≥ 8 → `TAIL_LINE_COLOR`. Legend `type:'scroll'` keeps layout sane. |
| `lines` length 0 | Existing `TrendTabsPlaceholder` empty state — unchanged. |
| `lines` from legacy `localStorage` (4 ids) | Works unchanged; `resolvedChartLines` honors user selection first. |
| Invalid rank (`NaN`, negative, non-integer) | `getRankColor` returns `TAIL_LINE_COLOR`. |
| Tooltip `params` not an array (defensive) | Returns `''` (current behavior on bad input). |
| Tooltip with single series | Still renders total + single row. |
| Light / dark theme | `themeColors` already provided to `buildEChartsTrendOption`; new blocks use only `themeColors.*` keys (no hardcoded hex). |

---

## 7. Testing Strategy

### Unit tests (vitest)

1. **`src/utils/usage/chartPalette.test.ts` (NEW)**
   - `getRankColor(0..7)` returns exact palette hex.
   - `getRankColor(8)`, `getRankColor(99)` → `TAIL_LINE_COLOR`.
   - `getRankColor(-1)`, `getRankColor(1.5)`, `getRankColor(NaN)` →
     `TAIL_LINE_COLOR`.
   - `buildAreaGradient` returns a string containing `linear-gradient`.

2. **`src/components/usage/UsageChart.test.tsx` (NEW)** — light assertion
   that with 9 fake models passed in as `lines`, the resulting
   `ChartData.datasets[0].borderColor === '#3B82F6'` and
   `chartData.datasets[8].borderColor === TAIL_LINE_COLOR`.

3. **`src/utils/usage/chartConfig.test.ts` (EXTEND)**
   - With 8 datasets, returned `legend.data.length === 8`.
   - `legend.selector` contains `'all'` and `'inverse'`.
   - Tooltip `formatter` invoked with mock params sorts by descending value
     and emits color swatch + percentage markup.
   - Series entries include `emphasis.focus === 'series'` and
     `blur.lineStyle.opacity === 0.15`.

### Manual verification checklist (developer)

- [ ] Default page load: 8 distinct colored series in the trend chart.
- [ ] Hover a series → that line thickens; the other 7 fade to ~15% opacity.
- [ ] Click a legend item → that series hides/shows; chart re-renders.
- [ ] Click legend selector `全选` / `反选` → chart visibility toggles accordingly.
- [ ] Reload page with old localStorage (4 ids) → still works, no migration.
- [ ] Switch theme (light/dark) → legend text/tooltip bg follow theme.
- [ ] Cost tab unchanged (single cyan line).

---

## 8. Risks & Mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| 8 colors are still hard to distinguish for color-blind users | Medium | Top1 uses thicker line + larger hover point as a secondary cue. Future work: add line dash patterns per rank (out of scope here). |
| Tooltip template string leaks unescaped series names | Low | Reuse existing `escapeHtml` helper; series names are already bounded to known model/credential IDs. |
| ECharts `legend.selector` styling clashes with theme | Low | `selectorLabel` keys map onto `themeColors.textMuted` / `tooltipBorder`. |
| 8-series chart exceeds browser canvas perf on low-end devices | Low | `MAX_CHART_DETAILS = 5000` + per-series `sampling: 'lttb'` is the existing safety net. |
| Rank instability when totals are very close | Low | Ranking is by total spend in the current filter window; ties already break deterministically by insertion order (existing behavior). |

---

## 9. Files Changed (summary)

| Path | Action |
|---|---|
| `src/utils/usage/chartPalette.ts` | **create** |
| `src/utils/usage/chartPalette.test.ts` | **create** |
| `src/utils/usage/chartConfig.ts` | edit (legend + tooltip + emphasis) |
| `src/utils/usage/chartConfig.test.ts` | edit (extend cases) |
| `src/components/usage/UsageChart.tsx` | edit (use `getRankColor`) |
| `src/components/usage/UsageChart.test.tsx` | **create** (light dataset-color snapshot) |
| `src/pages/UsagePage.tsx` | edit (`DEFAULT_TOP_N = 8`) |
| `src/utils/usage.ts` | re-export `buildAreaGradient` for back-compat (no breaking change) |

### Files Explicitly NOT Changed

- `src/components/usage/CostTrendChart.tsx`
- `src/components/usage/ChartLineSelector.tsx` (α path)
- `src/components/usage/TrendTabsCard.tsx`
- `src/pages/UsagePage.module.scss`
- `src/utils/echarts/registerThemes.ts`
- All `src/i18n/locales/*.json` (no new keys; existing `chart_line_label_1..9`
  cover ranks 1–8 and `chart_line_hint` still reads accurately)

---

## 10. Implementation Order (for the writing-plans skill)

1. Create `chartPalette.ts` and its test file.
2. Migrate `buildAreaGradient` re-export from `utils/usage.ts`.
3. Update `UsageChart.tsx` to consume `getRankColor`.
4. Extend `chartConfig.ts` with legend / tooltip / emphasis.
5. Bump `DEFAULT_TOP_N` in `UsagePage.tsx`.
6. Add `UsageChart.test.tsx` snapshot.
7. Extend `chartConfig.test.ts`.
8. Run `npm run lint && npm run type-check && npm run test:run`.
9. Run `npm run build` (deploy step is manual, not automated).
10. Manual verification checklist (Section 7).