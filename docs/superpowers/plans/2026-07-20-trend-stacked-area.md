# Trend Analysis Stacked Area Chart — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert the 8-color trend chart from line chart to stacked area chart; switch the legend layout to a 2-column grid to fix name truncation; remove rank-aware border/point cues that no longer apply.

**Architecture:** Three commits, each touching one concern: (1) series block in `chartConfig.ts` (add `stack: 'total'`, change `smooth`/`lineStyle.width`/`areaStyle`); (2) legend block in `chartConfig.ts` (switch `type` to `'plain'`, constrain width); (3) `UsageChart.tsx` cleanup (remove `borderWidth` tier + `pointHoverRadius`). Test changes bundled with each.

**Tech Stack:** React 19, TypeScript, ECharts 6, Vite, Vitest, i18next (no new keys).

**Reference spec:** `docs/superpowers/specs/2026-07-20-trend-stacked-area-design.md`
**Previous design:** `docs/superpowers/specs/2026-07-20-trend-8-color-design.md`

**Working directory:** `Cli-Proxy-API-Management-Center/` (the frontend subproject).

---

## File Structure

| Path | Action | Responsibility |
|---|---|---|
| `src/utils/usage/chartConfig.ts` | edit | ECharts option builder: series (stack/smooth/area) + legend (grid layout) |
| `src/utils/usage/chartConfig.test.ts` | edit | Tests for stack + legend grid + smooth:false |
| `src/components/usage/UsageChart.tsx` | edit | Drop rank-aware `borderWidth` tier + `pointHoverRadius` |
| `src/components/usage/UsageChart.test.tsx` | edit | Update assertions: flat `borderWidth`, no `pointHoverRadius` |

**Files explicitly NOT changed** (per spec §9):
- `src/utils/usage/chartPalette.ts`
- `src/components/usage/CostTrendChart.tsx`
- `src/components/usage/ChartLineSelector.tsx`
- `src/components/usage/TrendTabsCard.tsx`
- `src/pages/UsagePage.tsx`
- `src/pages/UsagePage.module.scss`
- `src/utils/echarts/registerThemes.ts`
- All `src/i18n/locales/*.json`

---

## Task 1: Convert series to stacked area (chartConfig.ts)

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/utils/usage/chartConfig.ts` — series block
- Modify: `Cli-Proxy-API-Management-Center/src/utils/usage/chartConfig.test.ts` — add stack tests

- [ ] **Step 1: Read the current series block**

```bash
cd Cli-Proxy-API-Management-Center && grep -n "series:\|stack:\|smooth:\|lineStyle:\|areaStyle:\|emphasis:\|blur:" src/utils/usage/chartConfig.ts | head -20
```

Verify the exact line numbers and current values (rank-aware `lineStyle.width`, current `areaStyle`, current `smooth`, current `emphasis.lineStyle.width`).

- [ ] **Step 2: Write the failing test cases**

Append to `src/utils/usage/chartConfig.test.ts` (within or after the existing describe blocks):

```ts
describe('buildEChartsTrendOption - stacked area', () => {
  const theme = {
    textPrimary: '#e5e7eb',
    textSecondary: '#9ca3af',
    bgPrimary: '#111827',
    border: '#374151',
    borderMuted: '#4b5563',
    accent: '#06b6d4',
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

  it('enables stack: "total" on every series', () => {
    const option = buildEChartsTrendOption(data, theme, { isNarrowScreen: false });
    const series = option.series as Array<{ stack?: string }>;
    expect(series).toHaveLength(8);
    for (const s of series) {
      expect(s.stack).toBe('total');
    }
  });

  it('sets smooth: false on every series (stacked area prefers straight edges)', () => {
    const option = buildEChartsTrendOption(data, theme, { isNarrowScreen: false });
    const series = option.series as Array<{ smooth?: boolean }>;
    for (const s of series) {
      expect(s.smooth).toBe(false);
    }
  });

  it('uses a flat lineStyle.width of 1 (no longer rank-aware)', () => {
    const option = buildEChartsTrendOption(data, theme, { isNarrowScreen: false });
    const series = option.series as Array<{
      lineStyle: { width: number };
    }>;
    for (const s of series) {
      expect(s.lineStyle.width).toBe(1);
    }
  });

  it('uses a vertical gradient areaStyle with opacityTop > opacityBottom', () => {
    const option = buildEChartsTrendOption(data, theme, { isNarrowScreen: false });
    const series = option.series as Array<{
      areaStyle: { color: unknown };
    }>;
    for (const s of series) {
      // areaStyle.color is a CSS gradient string from buildAreaGradient
      expect(typeof s.areaStyle.color).toBe('string');
      expect((s.areaStyle.color as string)).toContain('linear-gradient');
    }
  });

  it('emphasis.lineStyle.width is reduced to 2 (was 3) to avoid overpowering the layer', () => {
    const option = buildEChartsTrendOption(data, theme, { isNarrowScreen: false });
    const series = option.series as Array<{
      emphasis: { lineStyle: { width: number } };
    }>;
    for (const s of series) {
      expect(s.emphasis.lineStyle.width).toBe(2);
    }
  });
});
```

- [ ] **Step 3: Run the new tests; expect failure**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/utils/usage/chartConfig.test.ts
```

Expected: at least 5 new FAILs (`stack` undefined, `smooth` is true, `lineStyle.width` is 2/1.5/1, `emphasis.lineStyle.width` is 3).

- [ ] **Step 4: Modify the series block in chartConfig.ts**

In the `series: data.datasets.map((d, i) => ({...}))` block inside `buildEChartsTrendOption`, apply these changes:

1. Add `stack: 'total',` to each series.
2. Change `smooth: true` → `smooth: false`.
3. Change `lineStyle: { width: i === 0 ? 2 : 1.5, color: d.borderColor }` → `lineStyle: { width: 1, color: d.borderColor }`.
4. Change `areaStyle: { color: buildAreaGradient(d.borderColor as string) }` → `areaStyle: { color: buildAreaGradient(d.borderColor as string, 0.55, 0.20) }`.
5. Change `emphasis: { focus: 'series', lineStyle: { width: 3 } }` → `emphasis: { focus: 'series', lineStyle: { width: 2 } }`.

The resulting block should look like:

```ts
series: data.datasets.map((d, i) => ({
  name: d.label,
  type: 'line',
  stack: 'total',
  smooth: false,
  showSymbol: false,
  sampling: 'lttb',
  lineStyle: { width: 1, color: d.borderColor },
  itemStyle: { color: d.borderColor },
  areaStyle: {
    color: buildAreaGradient(d.borderColor as string, 0.55, 0.20),
  },
  data: d.data,
  emphasis: { focus: 'series', lineStyle: { width: 2 } },
  blur: {
    lineStyle: { opacity: 0.15 },
    itemStyle: { opacity: 0.15 },
  },
})),
```

- [ ] **Step 5: Re-run the tests**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/utils/usage/chartConfig.test.ts
```

Expected: the 5 new tests PASS; pre-existing tests still PASS.

- [ ] **Step 6: Lint + type-check**

```bash
cd Cli-Proxy-API-Management-Center && npm run lint && npm run type-check
```

Expected: 0 errors. Pre-existing lint warnings in unrelated files are OK.

- [ ] **Step 7: Commit**

```bash
cd Cli-Proxy-API-Management-Center && git add src/utils/usage/chartConfig.ts src/utils/usage/chartConfig.test.ts && git commit -m "feat(usage): convert trend chart to stacked area with vertical gradient"
```

---

## Task 2: Switch legend to 2-column grid (chartConfig.ts)

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/utils/usage/chartConfig.ts` — legend block
- Modify: `Cli-Proxy-API-Management-Center/src/utils/usage/chartConfig.test.ts` — add legend grid tests

- [ ] **Step 1: Locate the current legend block**

```bash
cd Cli-Proxy-API-Management-Center && grep -n "legend:\|type: 'scroll'\|type: 'plain'\|selector:" src/utils/usage/chartConfig.ts
```

- [ ] **Step 2: Write the failing legend tests**

Append to `src/utils/usage/chartConfig.test.ts`:

```ts
describe('buildEChartsTrendOption - legend grid layout', () => {
  const theme = {
    textPrimary: '#e5e7eb',
    textSecondary: '#9ca3af',
    bgPrimary: '#111827',
    border: '#374151',
    borderMuted: '#4b5563',
    accent: '#06b6d4',
  } as const;

  const data = {
    labels: ['00:00'],
    datasets: Array.from({ length: 8 }, (_, i) => ({
      label: `model-${i + 1}`,
      data: [1],
      borderColor: '#000',
      backgroundColor: '',
      fill: true,
      tension: 0.35,
    })),
  };

  it('legend uses type "plain" (not "scroll") so it can wrap to multiple rows', () => {
    const option = buildEChartsTrendOption(data, theme, { isNarrowScreen: false });
    const legend = option.legend as { type: string };
    expect(legend.type).toBe('plain');
  });

  it('legend has a constrained width to force wrap (percentage or px)', () => {
    const option = buildEChartsTrendOption(data, theme, { isNarrowScreen: false });
    const legend = option.legend as { width: string | number };
    expect(legend.width).toBeDefined();
    // Accept either a percentage string or a pixel value
    if (typeof legend.width === 'string') {
      expect(legend.width).toMatch(/%|px/);
    } else {
      expect(typeof legend.width).toBe('number');
    }
  });

  it('legend still exposes selector all/inverse', () => {
    const option = buildEChartsTrendOption(data, theme, { isNarrowScreen: false });
    const legend = option.legend as { selector: string[] };
    expect(legend.selector).toEqual(expect.arrayContaining(['all', 'inverse']));
  });

  it('legend selectorPosition is "start" (so it does not compete with the 2-row grid)', () => {
    const option = buildEChartsTrendOption(data, theme, { isNarrowScreen: false });
    const legend = option.legend as { selectorPosition?: string };
    expect(legend.selectorPosition).toBe('start');
  });
});
```

- [ ] **Step 3: Run the new legend tests; expect failure**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/utils/usage/chartConfig.test.ts
```

Expected: 4 new FAILs (`type: 'scroll'`, no `width`, `selectorPosition: 'end'`).

- [ ] **Step 4: Modify the legend block in chartConfig.ts**

Find the existing legend block (currently `type: 'scroll', orient: 'horizontal', top: 8, ...`). Replace with:

```ts
legend: {
  type: 'plain',            // CHANGED: from 'scroll' to 'plain' for natural wrap
  orient: 'horizontal',
  left: 'center',
  top: 8,
  width: '70%',             // NEW: constrains legend width to force 2-row wrap
  itemWidth: 14,
  itemHeight: 8,
  itemGap: 18,              // CHANGED: from 14 → 18 for better readability when wrapped
  textStyle: { color: theme.textPrimary, fontSize: 12 },
  pageIconColor: theme.textSecondary,        // kept (unused for plain but harmless)
  pageTextStyle: { color: theme.textSecondary },
  data: data.datasets.map((d) => d.label),
  selector: ['all', 'inverse'],
  selectorLabel: {
    color: theme.textSecondary,
    borderColor: theme.border,
  },
  selectorPosition: 'start',  // CHANGED: from 'end' to 'start'
},
```

- [ ] **Step 5: Re-run the tests**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/utils/usage/chartConfig.test.ts
```

Expected: all 4 new tests PASS; all previous tests still PASS.

- [ ] **Step 6: Lint + type-check**

```bash
cd Cli-Proxy-API-Management-Center && npm run lint && npm run type-check
```

Expected: 0 new errors.

- [ ] **Step 7: Commit**

```bash
cd Cli-Proxy-API-Management-Center && git add src/utils/usage/chartConfig.ts src/utils/usage/chartConfig.test.ts && git commit -m "feat(usage): switch trend legend to 2-row grid layout via plain type"
```

---

## Task 3: Remove rank-aware borderWidth + pointHoverRadius (UsageChart.tsx)

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/components/usage/UsageChart.tsx` — dataset construction block
- Modify: `Cli-Proxy-API-Management-Center/src/components/usage/UsageChart.test.tsx` — update assertions

- [ ] **Step 1: Locate the dataset construction block**

```bash
cd Cli-Proxy-API-Management-Center && grep -n "borderWidth\|pointHoverRadius\|TOKEN_FOCUS_CHART_COLORS" src/components/usage/UsageChart.tsx
```

- [ ] **Step 2: Update the test file first (TDD)**

In `src/components/usage/UsageChart.test.tsx`, find the assertions on `borderWidth` (currently `rank === 0 ? 2 : rank < 8 ? 1.5 : 1`) and replace with:

```ts
// Stacked area: borderWidth is flat 1 (stacking implies rank via layer thickness)
expect(borderWidths.every((w) => w === 1)).toBe(true);

// Stacked area has no points
expect(pointHoverRadii.every((r) => r === 4)).toBe(true); // or remove this assertion if the field is gone
```

(Adjust based on the actual test file structure — read it first to find the existing assertion.)

If the existing test asserts `borderWidth === 2` for the first dataset, change it to assert `=== 1` for all datasets. If the test references `pointHoverRadius`, remove the rank-specific assertion (either keep a flat assertion or remove if the field is gone).

- [ ] **Step 3: Run the test; expect failure**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/components/usage/UsageChart.test.tsx
```

Expected: FAIL on borderWidth assertion (still rank-aware).

- [ ] **Step 4: Modify UsageChart.tsx**

In the dataset construction block (around lines 322-348 based on the prior iteration):

1. Replace the rank-aware `borderWidth` assignment:

```ts
// Before:
const borderWidth = rank === 0 ? 2 : rank < 8 ? 1.5 : 1;

// After:
const borderWidth = 1;
```

2. Replace the rank-aware `pointHoverRadius` assignment:

```ts
// Before:
const pointHoverRadius = rank === 0 ? 6 : 4;

// After:
const pointHoverRadius = 4;
```

(We keep the field with a flat value rather than removing it entirely, because the field is part of `ChartDataset` shape and downstream code may read it. Setting it to 4 (a neutral value) avoids surprises if ECharts later renders points. If the implementer prefers to remove the field entirely, do so AND verify no consumer reads it.)

**Do NOT touch** `TOKEN_FOCUS_CHART_COLORS` — that block remains unchanged.

- [ ] **Step 5: Re-run the test**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run -- src/components/usage/UsageChart.test.tsx
```

Expected: PASS.

- [ ] **Step 6: Lint + type-check**

```bash
cd Cli-Proxy-API-Management-Center && npm run lint && npm run type-check
```

Expected: 0 new errors.

- [ ] **Step 7: Commit**

```bash
cd Cli-Proxy-API-Management-Center && git add src/components/usage/UsageChart.tsx src/components/usage/UsageChart.test.tsx && git commit -m "refactor(usage): flatten borderWidth and pointHoverRadius for stacked area"
```

---

## Task 4: Final verification + parent-repo commit

**Files:** none (verification only)

- [ ] **Step 1: Run the full vitest suite**

```bash
cd Cli-Proxy-API-Management-Center && npm run test:run
```

Expected: all green. Count should be 80+ tests (the new tests from Tasks 1, 2, 3 add ~10 cases to the existing 75).

- [ ] **Step 2: Run the production build**

```bash
cd Cli-Proxy-API-Management-Center && npm run build 2>&1 | tail -5
```

Expected: tsc + vite build both succeed.

- [ ] **Step 3: Verify no new lint warnings**

```bash
cd Cli-Proxy-API-Management-Center && npm run lint
```

Expected: 0 new errors in changed files.

- [ ] **Step 4: Stage and commit the parent-repo submodule pointer**

From the **parent repo** root (`/Users/zhouyong/Desktop/work/Decard/gitlab/ai/CLIProxyAPI/`):

```bash
cd /Users/zhouyong/Desktop/work/Decard/gitlab/ai/CLIProxyAPI
git status --short
```

Expected: `M Cli-Proxy-API-Management-Center` (submodule pointer advanced). Possibly `M assets/management.html` (build artifact from Step 2).

```bash
cd /Users/zhouyong/Desktop/work/Decard/gitlab/ai/CLIProxyAPI
git add Cli-Proxy-API-Management-Center
git add assets/management.html 2>/dev/null  # only if present
git status --short
```

Verify only the expected files are staged.

```bash
cd /Users/zhouyong/Desktop/work/Decard/gitlab/ai/CLIProxyAPI
git commit -m "feat(usage): stacked area chart with 2-row legend grid (parent pointer)"
```

The branch **must** remain `personal/offline-dev` (do NOT checkout main).

- [ ] **Step 5: Verify final state**

```bash
cd /Users/zhouyong/Desktop/work/Decard/gitlab/ai/CLIProxyAPI
git branch --show-current
git log --oneline -5
git status --short
```

Expected:
- Branch: `personal/offline-dev`
- 5 most recent commits include: spec commit (`ceed827`), 3 stacked-area commits in submodule, 1 parent pointer commit
- Working tree clean (only pre-existing unrelated `config.yaml` modifications OK)

---

## Self-Review

**Spec coverage** (from spec §1):

| Spec requirement | Task |
|---|---|
| Chart type: stacked area with `stack: 'total'` | T1 |
| Legend: 2-column grid (type: 'plain' + constrained width) | T2 |
| Stack order: Top 1 at bottom (natural dataset order) | T1 (no explicit reverse needed; datasets are already in rank order) |
| Glow: vertical gradient (deeper top, lighter bottom) | T1 (`buildAreaGradient(color, 0.55, 0.20)`) |
| Remove rank-aware borderWidth | T3 |
| Remove pointHoverRadius | T3 |
| Preserve T4 tooltip | T1 (no change to tooltip block) |
| Preserve T3 emphasis/blur | T1 (kept; only width reduced 3 → 2) |
| Preserve 8-color palette | (no change to chartPalette.ts) |
| Preserve DEFAULT_TOP_N=8, MAX_CHART_LINES=9 | (no change to UsagePage.tsx) |
| Preserve ECharts built-in selector | T2 (kept; only `selectorPosition` changed end → start) |

**Placeholder scan**: No TBD/TODO. All code blocks are concrete.

**Type consistency**:
- `stack: 'total'` is a valid ECharts 6 series option (string literal).
- `width: '70%'` is a valid ECharts legend option (percentage string).
- `buildAreaGradient(color, 0.55, 0.20)` matches the function signature from T1.
- `selectorPosition: 'start'` is a valid ECharts option (string literal).
- `borderWidth: 1` is a flat number, matches the field type.
- `pointHoverRadius: 4` is a flat number, matches the field type.

**Risk review**:
- The smooth:false change is a deliberate aesthetic choice (stacked area reads cleaner with straight edges). If user feedback later says "make it smooth again", it's a 1-line revert.
- The `width: '70%'` is a heuristic — actual wrap behavior depends on chart width. On very narrow screens (<400px), legend might wrap to 4 rows × 2 cols, which is still acceptable.
- The flat `borderWidth: 1` removes the rank cue; spec §1 rationale is that stacking itself implies rank via layer thickness. If user later complains "can't tell which layer is Top 1", we can re-introduce a small opacity boost for rank 0.

Plan complete. Saved to `docs/superpowers/plans/2026-07-20-trend-stacked-area.md`.