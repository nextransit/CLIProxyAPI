# Trend Analysis Stacked Area Chart — Design

**Status:** Draft (brainstorming)
**Date:** 2026-07-20
**Branch:** personal/offline-dev
**Author:** Brainstorming session (iteration 2)
**Depends on:** `2026-07-20-trend-8-color-design.md` (already implemented)

---

## 1. Goal

Iterate on the just-shipped 8-color line chart redesign. Convert the trend chart
from a **multi-line chart** to a **stacked area chart** to eliminate the
"tangled yarn ball" visual that emerges when 8 series are plotted on top of
each other. Concurrently fix the legend truncation bug (long model names like
`MiniMax-M2.7-highspeed` get clipped at 8 entries) by switching the legend
layout from a single horizontal scrollable row to a 2-column grid above the
chart.

### What stays the same (preserved from previous iteration)

- 8-color palette (`MODEL_TREND_PALETTE`) and `getRankColor` rank→color mapping
- `DEFAULT_TOP_N = 8`, `MAX_CHART_LINES = 9`
- Smart value-sorted tooltip with color swatch + percentage + Total row (T4)
- ECharts built-in `selector: ['all', 'inverse']` for legend toggle
- Hover focus via `emphasis.focus: 'series'` + `blur.lineStyle.opacity: 0.15`
- `CostTrendChart` (single-line cyan, untouched)
- i18n keys (no new keys)
- `ChartLineSelector`, `TrendTabsCard`, `UsagePage.module.scss` (untouched)

### What changes

1. **Chart type**: line → **stacked area** with `stack: 'total'`
2. **Legend layout**: horizontal scroll → **2-column grid above chart**
3. **Stack order**: Top 1 at the bottom, Top 8 at the top (natural dataset order)
4. **Glow effect**: each layer uses a vertical gradient (deeper at bottom, lighter
   at top) instead of a flat fill — gives subtle depth on dark background without
   becoming neon-rainbow
5. **Removed**: rank-based `borderWidth` differences (no longer needed when
   area thickness carries the rank visually) and `pointHoverRadius` (stacked
   area has no points)

---

## 2. Motivation (recap of user feedback)

User observed on a deployed snapshot:
- **"8 / 9" badge but only 6 visible legend entries, last one clipped** — current
  `type: 'scroll'` + `top: 8` pushes long names off-screen and the scroll
  affordance is easy to miss.
- **"Tangled yarn ball"** on the right side of the chart (post 2026-05-06) —
  8 lines on a shared Y axis with overlapping peaks; users can't tell which
  spike belongs to which model.
- **"Low color distinguishability"** — pale blue / cyan / white lines blend
  together on the dark theme background.

Stacked area addresses all three: the area itself encodes the value, the
stacking order makes the rank hierarchy visible at a glance, and the 2-column
legend ensures no clipping.

---

## 3. Architecture

```
                          ┌────────────────────────────┐
                          │  TrendTabsCard             │
                          │  ┌────────────────────┐    │
                          │  │ Legend (2-col grid)│    │
                          │  │ ● Top 1  ● Top 2   │    │
                          │  │ ● Top 3  ● Top 4   │    │
                          │  │ ● Top 5  ● Top 6   │    │
                          │  │ ● Top 7  ● Top 8   │    │
                          │  └────────────────────┘    │
                          │  ┌────────────────────┐    │
                          │  │  Stacked Area      │    │
                          │  │  (stack: 'total')  │    │
                          │  │  Top 1 ▓▓▓         │    │
                          │  │  Top 2 ░░░         │    │
                          │  │   ... stacked ...  │    │
                          │  │  Top 8 ▒▒▒         │    │
                          │  └────────────────────┘    │
                          │  [全选] [反选] (ECharts)    │
                          └────────────────────────────┘
```

Component responsibilities are unchanged from the previous iteration. Only
`buildEChartsTrendOption` is updated:

| File | Action | Why |
|---|---|---|
| `src/utils/usage/chartConfig.ts` | edit | Add `stack: 'total'` to each series; change `areaStyle` to vertical gradient; switch `legend.type` from `'scroll'` to `'plain'` with grid layout |
| `src/utils/usage/chartConfig.test.ts` | edit | Add cases for stack + legend grid |
| `src/components/usage/UsageChart.tsx` | edit | Remove `borderWidth` tiered differences; remove `pointHoverRadius` (stacked area has no points) |

**Files explicitly NOT changed**:
- `src/components/usage/CostTrendChart.tsx` (single-line, not affected)
- `src/components/usage/ChartLineSelector.tsx` (legend toggle via ECharts)
- `src/components/usage/TrendTabsCard.tsx`
- `src/utils/usage/chartPalette.ts` (palette is the same)
- `src/utils/echarts/registerThemes.ts`
- `src/pages/UsagePage.tsx` (DEFAULT_TOP_N already correct)
- `src/pages/UsagePage.module.scss`
- All `src/i18n/locales/*.json`

---

## 4. Data Flow

1. `UsagePage` produces top-8 models → `UsageChart`.
2. `UsageChart` calls `buildTrendChartData(...)` → `ChartData` with 8 datasets
   (already in rank order: `datasets[0]` = Top 1, `datasets[7]` = Top 8).
3. `buildEChartsTrendOption(data, themeColors)` returns ECharts option with:
   - `legend` block: `type: 'plain'`, `orient: 'horizontal'`, 2 columns via
     grid positioning (ECharts 6 supports custom grid via `legend.backgroundColor`
     + CSS flex container wrapping the chart, OR via `legend.textStyle` /
     `legend.itemWidth` / `legend.itemGap` tuned for wrapping).
     **Note**: ECharts native legend doesn't support explicit "2 columns"
     natively — see §5.1 for the chosen approach.
   - `series[*].stack: 'total'` so ECharts stacks them.
   - `series[*].areaStyle.color` set to a vertical gradient built by
     `buildAreaGradient(color, 0.65, 0.15)` — deep at top (matches the layer's
     top edge), light at bottom (matches the next layer's transition).
     *Wait — stacked area convention is: the layer is bounded ABOVE by the
     upper cumulative line and BELOW by the lower cumulative line. So the
     gradient should run from the layer's top (lighter) to the layer's bottom
     (slightly darker)? Or vice versa? See §5.2.*

---

## 5. API Design

### 5.1 Legend layout — 2-column grid

ECharts native legend has these layout options (ECharts 6):
- `type: 'scroll'` (single horizontal scrollable row)
- `type: 'plain'` (single block, multiple lines allowed by ECharts when
  items overflow — but it doesn't produce a clean grid)
- Custom layout via HTML/CSS overlay (more work, breaks ECharts theme sync)

**Chosen approach**: `type: 'plain'` with `orient: 'horizontal'` and tuned
`itemWidth`, `itemGap`, and a constrained width via `width` + `left` so the
legend wraps naturally to a 2-row × 4-col grid when there are 8 entries.

ECharts 6 legend with `type: 'plain'` and `orient: 'horizontal'` will wrap
to multiple rows when `width` is constrained. To force a 2-row layout for 8
entries:
```ts
legend: {
  type: 'plain',
  orient: 'horizontal',
  left: 'center',
  top: 8,
  width: '70%',          // constrain to ~70% of chart width → forces wrap
  itemWidth: 14,
  itemHeight: 8,
  itemGap: 18,
  textStyle: { color: theme.textPrimary, fontSize: 12 },
  data: data.datasets.map((d) => d.label),
  selector: ['all', 'inverse'],
  selectorLabel: {
    color: theme.textSecondary,
    borderColor: theme.border,
  },
  selectorPosition: 'start',  // moved to left so it doesn't compete with 2-col grid
}
```

Trade-off: this gives us a clean 2-row wrap (4 items per row) for 8 entries,
but on narrower screens the wrap might shift to 4 rows × 2 cols, which is also
acceptable. The grid is **fluid** based on chart width.

### 5.2 Area gradient direction

For stacked area layers, the visual convention is:
- **Bottom edge of a layer = transition to the next layer below** → should be
  slightly opaque (so the layer is "anchored" to what's underneath)
- **Top edge of a layer = the cumulative total line** → can be lighter
  (transitions toward the chart background / next layer above)

So `areaStyle.color` runs **light at bottom → darker at top** for each layer:
```ts
areaStyle: {
  color: buildAreaGradient(color, /* opacityTop */ 0.55, /* opacityBottom */ 0.20),
}
```

This gives a "rising" feel — each layer "fills in" toward the total line.

### 5.3 Series modifications

```ts
series: data.datasets.map((d, i) => ({
  name: d.label,
  type: 'line',
  stack: 'total',         // NEW: enable stacking
  smooth: false,          // CHANGED: stacked area looks cleaner without smooth
  showSymbol: false,
  sampling: 'lttb',
  lineStyle: {
    width: 1,             // CHANGED: was rank-aware (2/1.5/1) → flat 1 (stacking
                          // already implies rank hierarchy via layer thickness)
    color: d.borderColor,
  },
  itemStyle: { color: d.borderColor },
  areaStyle: {
    color: buildAreaGradient(d.borderColor as string, 0.55, 0.20),
  },
  data: d.data,
  emphasis: { focus: 'series', lineStyle: { width: 2 } },  // CHANGED width 3 → 2
  blur: {
    lineStyle: { opacity: 0.15 },
    itemStyle: { opacity: 0.15 },
  },
})),
```

**Removed** (from `UsageChart.tsx`):
- `borderWidth: i === 0 ? 2 : ... ` (replaced with flat `1`)
- `pointHoverRadius: rank === 0 ? 6 : 4` (no points in stacked area)

### 5.4 Tooltip — unchanged

The T4 value-sorted tooltip formatter remains as-is. With stacking, sorting
by descending value at the hover time-point gives users the "which layer
contributed the most at this instant" view, which is the same mental model
they had with line chart.

---

## 6. Edge Cases

| Case | Behavior |
|---|---|
| `lines.length` < 8 (e.g. only 3 models in dataset) | `stack: 'total'` still stacks 3 layers; legend grid wraps to 1 row × 3 cols (or whatever fits) |
| User clicks legend → hides Top 1 | ECharts restacks remaining 7 layers; auto-scale Y axis; the cumulative top line now reflects only the remaining 7 |
| All layers hidden via `selector: 'inverse'` | Empty plot — acceptable, matches current behavior |
| Hover over a layer → `emphasis.focus: 'series'` dims other 6 layers to 15% opacity | Same behavior as line chart, works transparently with stack |
| Light theme | `textStyle.color`, `selectorLabel.color` etc. follow `ThemeColors` (already wired) |
| Chart width < 600px (mobile) | Legend wraps to 4 rows × 2 cols (still readable, no clipping) |

---

## 7. Testing Strategy

### Unit tests (extend `chartConfig.test.ts`)

1. **Stack enabled**: `buildEChartsTrendOption(data, theme, ...)` returns
   `series[0].stack === 'total'`, ..., `series[7].stack === 'total'`.
2. **Smooth disabled**: every series has `smooth: false`.
3. **Area gradient direction**: spy on `buildAreaGradient` and confirm it's
   called with `opacityTop > opacityBottom` for each series.
4. **Legend type is `plain`**: `option.legend.type === 'plain'`.
5. **Legend constrained width**: `option.legend.width` is a percentage string
   (e.g. `'70%'`).
6. **Legend selector still present**: `legend.selector` contains
   `['all', 'inverse']`.
7. **Emphasis/blur preserved**: every series has
   `emphasis.focus === 'series'` and `blur.lineStyle.opacity === 0.15`.

### Unit tests (extend `UsageChart.test.tsx`)

1. **`borderWidth` is flat 1** for all ranks (no longer rank-aware).
2. **No `pointHoverRadius` field** (or it's `0`/`undefined`) in the dataset
   schema (we don't write it anymore).

### Manual verification checklist (developer)

- [ ] Default page load: 8 stacked layers visible; Total trend (top contour)
      visible.
- [ ] Legend shows all 8 model names in a 2-row × 4-col grid; no truncation.
- [ ] Hover any time point → tooltip shows 8 rows sorted desc, each with
      color swatch + percentage + Total row.
- [ ] Click any legend item → that layer disappears, remaining 7 restack.
- [ ] Click legend's 全选 / 反选 → layer visibility toggles accordingly.
- [ ] Switch theme (light/dark) → legend text + area fills follow theme.
- [ ] Resize browser narrow → legend wraps gracefully (4 rows × 2 cols).
- [ ] Cost tab unchanged (single cyan line, no stacking).

---

## 8. Risks & Mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| Stacked area hides individual model trends | Medium | This is the deliberate trade-off; legend toggle lets users hide other layers to focus on one |
| Area gradient direction (light/dark) feels backwards on light theme | Low | Test both themes in dev server; flip the gradient params if needed |
| ECharts legend wrap behavior inconsistent across versions | Low | Pin ECharts version (already at `^6.1.0`); if wrap misbehaves, fall back to explicit 2-row positioning via `legend.top` + CSS |
| Stacking a 0-valued layer breaks the visual | Low | LTTB sampling smooths this; if problematic, add `connectNulls: true` per series |
| Performance: 8 stacked layers + LTTB on a 30-day window | Low | Existing `MAX_CHART_DETAILS = 5000` + LTTB caps data; stacking is a GPU-friendly op |

---

## 9. Files Changed (summary)

| Path | Action |
|---|---|
| `src/utils/usage/chartConfig.ts` | edit (series: add stack, smooth:false, area gradient; legend: switch type to plain + constrained width) |
| `src/utils/usage/chartConfig.test.ts` | edit (extend cases for stack + legend grid) |
| `src/components/usage/UsageChart.tsx` | edit (remove borderWidth tier + pointHoverRadius) |
| `src/components/usage/UsageChart.test.tsx` | edit (verify flat borderWidth, no pointHoverRadius) |

### Files NOT changed (preserved)

- `src/utils/usage/chartPalette.ts` (palette unchanged)
- `src/components/usage/CostTrendChart.tsx`
- `src/components/usage/ChartLineSelector.tsx`
- `src/components/usage/TrendTabsCard.tsx`
- `src/pages/UsagePage.tsx`
- `src/pages/UsagePage.module.scss`
- `src/utils/echarts/registerThemes.ts`
- All `src/i18n/locales/*.json`

---

## 10. Implementation Order (for writing-plans)

1. Edit `chartConfig.ts`: series changes (stack, smooth, areaStyle, lineStyle.width)
2. Edit `chartConfig.test.ts`: extend tests for stack + legend grid
3. Run vitest → all green
4. Edit `UsageChart.tsx`: remove borderWidth tier + pointHoverRadius
5. Edit `UsageChart.test.tsx`: update tests for flat borderWidth
6. Edit `chartConfig.ts`: legend block (type: 'plain', width: '70%', etc.)
7. Run vitest → all green
8. Run build + lint → all clean
9. Manual verification (Section 7 checklist)

Total estimated: 2-3 commits on personal/offline-dev.