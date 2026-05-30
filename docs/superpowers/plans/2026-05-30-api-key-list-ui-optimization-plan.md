# API密钥列表UI/UX优化实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 全面优化API密钥列表的UI/UX，包括密钥密码遮蔽、复制功能、权重步进器、状态徽章、代理URL折叠、删除确认

**Architecture:** 在现有表格结构基础上进行深度优化，保留紧凑性同时全面提升可读性和交互体验。代理URL从主表移至可折叠展开区域。

**Tech Stack:** React, TypeScript, SCSS, Lucide Icons

---

## 文件结构

| 文件 | 改动内容 |
|------|----------|
| `Cli-Proxy-API-Management-Center/src/pages/AiProvidersPage.module.scss` | 更新表格布局、状态Badge样式、步进器样式 |
| `Cli-Proxy-API-Management-Center/src/pages/AiProvidersOpenAIEditPage.tsx` | 重构密钥行组件、添加交互逻辑 |

---

## Task 1: 添加图标组件

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/components/ui/icons.tsx`

- [ ] **Step 1: 添加 Copy 图标**

在 `icons.tsx` 末尾添加:

```tsx
export function IconCopy({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <rect width="14" height="14" x="8" y="8" rx="2" ry="2" />
      <path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2" />
    </svg>
  );
}

export function IconPlus({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M5 12h14" />
      <path d="M12 5v14" />
    </svg>
  );
}

export function IconMinus({ size = 20, ...props }: IconProps) {
  return (
    <svg {...baseSvgProps} width={size} height={size} {...props}>
      <path d="M5 12h14" />
    </svg>
  );
}
```

- [ ] **Step 2: 提交**

```bash
git add src/components/ui/icons.tsx && git commit -m "feat(icons): add Copy, Plus, Minus icons for key list"
```

---

## Task 2: 更新SCSS样式

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/pages/AiProvidersPage.module.scss:1550-1650`

- [ ] **Step 1: 更新表格列布局**

将 `.keyTableHeader` 和 `.keyTableRow` 的 grid-template-columns 从:
```scss
grid-template-columns: 46px minmax(320px, 1fr) 120px 120px;
```
改为 (移除代理列，状态前置):
```scss
grid-template-columns: 46px 120px minmax(280px, 1fr) 90px auto;
```

- [ ] **Step 2: 添加状态Badge样式**

在 `.keyTableShell` 后添加:

```scss
// Status Badge styles
.keyStatusBadge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 4px 10px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  white-space: nowrap;
  border: 1px solid transparent;
}

.keyStatusBadgeIdle {
  background: var(--bg-secondary);
  color: var(--text-tertiary);
  border-color: var(--border-secondary);
}

.keyStatusBadgeLoading {
  background: #3b82f6;
  color: white;
}

.keyStatusBadgeSuccess {
  background: var(--success-badge-bg, #d1fae5);
  color: var(--success-badge-text, #065f46);
  border-color: var(--success-badge-border, #6ee7b7);
}

.keyStatusBadgeError {
  background: var(--failure-badge-bg);
  color: var(--failure-badge-text);
  border-color: var(--failure-badge-border);
}

.keyStatusBadgeSuccess.loading {
  animation: pulse 1.5s infinite;
}

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.7; }
}
```

- [ ] **Step 3: 添加密钥输入框组合样式**

```scss
// Key input group (input + toggle + copy)
.keyInputGroup {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
}

.keyInputWrapper {
  position: relative;
  flex: 1;
  min-width: 0;
}

.keyInputToggle,
.keyInputCopy {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  padding: 0;
  border: 1px solid var(--border-primary);
  border-radius: 6px;
  background: var(--bg-secondary);
  color: var(--text-secondary);
  cursor: pointer;
  flex-shrink: 0;
  transition: all 0.15s ease;

  &:hover {
    background: var(--bg-tertiary);
    color: var(--text-primary);
    border-color: var(--primary-color);
  }

  &:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
}

.keyInputToggle {
  &:hover {
    background: var(--bg-tertiary);
  }
}

.keyInputCopy {
  &:hover {
    background: var(--bg-tertiary);
    color: var(--primary-color);
  }
}
```

- [ ] **Step 4: 添加权重步进器样式**

```scss
// Weight stepper
.weightStepper {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  border: 1px solid var(--border-primary);
  border-radius: 6px;
  background: var(--bg-secondary);
  overflow: hidden;
}

.weightStepperBtn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 32px;
  padding: 0;
  border: none;
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
  transition: all 0.15s ease;

  &:hover:not(:disabled) {
    background: var(--bg-tertiary);
    color: var(--text-primary);
  }

  &:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
}

.weightStepperValue {
  width: 36px;
  height: 32px;
  padding: 0 4px;
  border: none;
  background: transparent;
  text-align: center;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  -moz-appearance: textfield;

  &::-webkit-outer-spin-button,
  &::-webkit-inner-spin-button {
    -webkit-appearance: none;
    margin: 0;
  }

  &:focus {
    outline: none;
    background: var(--bg-tertiary);
  }
}
```

- [ ] **Step 5: 添加代理URL折叠区样式**

```scss
// Proxy URL expandable section
.keyProxySection {
  margin-top: $spacing-sm;
  padding: $spacing-sm;
  border: 1px dashed var(--border-secondary);
  border-radius: $radius-md;
  background: var(--bg-secondary);
}

.keyProxyToggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--text-secondary);
  font-size: 12px;
  cursor: pointer;
  transition: all 0.15s ease;

  &:hover {
    background: var(--bg-tertiary);
    color: var(--text-primary);
  }
}

.keyProxyExpanded {
  margin-top: $spacing-xs;
}

.keyProxyInput {
  width: 100%;
  padding: 8px 10px;
  font-size: 13px;
  min-height: 36px;
  text-align: left;
}
```

- [ ] **Step 6: 提交**

```bash
git add src/pages/AiProvidersPage.module.scss && git commit -m "feat(styles): add key list UI styles - badges, stepper, input groups"
```

---

## Task 3: 重构 AiProvidersOpenAIEditPage 组件

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/pages/AiProvidersOpenAIEditPage.tsx`

- [ ] **Step 1: 添加新的导入**

在文件顶部添加:

```tsx
import { IconEye, IconEyeOff, IconCopy, IconPlus, IconMinus, IconChevronDown, IconChevronUp } from '@/components/ui/icons';
import { useNotificationStore } from '@/stores';
```

- [ ] **Step 2: 在组件内添加状态管理**

在 `AiProvidersOpenAIEditPage` 组件内，添加以下状态 (在其他 useState 后):

```tsx
const [visibleKeyIndexes, setVisibleKeyIndexes] = useState<Set<number>>(new Set());
const [expandedProxyIndexes, setExpandedProxyIndexes] = useState<Set<number>>(new Set());
```

- [ ] **Step 3: 添加辅助函数**

在组件内添加:

```tsx
// Toggle key visibility
const toggleKeyVisibility = (index: number) => {
  setVisibleKeyIndexes((prev) => {
    const next = new Set(prev);
    if (next.has(index)) {
      next.delete(index);
    } else {
      next.add(index);
    }
    return next;
  });
};

// Copy key to clipboard
const copyKeyToClipboard = async (apiKey: string) => {
  try {
    await navigator.clipboard.writeText(apiKey);
    showNotification(t('notification.copied_to_clipboard'), 'success');
  } catch {
    showNotification(t('notification.copy_failed'), 'error');
  }
};

// Toggle proxy expanded
const toggleProxyExpanded = (index: number) => {
  setExpandedProxyIndexes((prev) => {
    const next = new Set(prev);
    if (next.has(index)) {
      next.delete(index);
    } else {
      next.add(index);
    }
    return next;
  });
};
```

- [ ] **Step 4: 添加删除确认函数**

在组件内添加:

```tsx
// Confirm and remove entry
const confirmRemoveEntry = (idx: number) => {
  showConfirmation({
    title: t('ai_providers.openai_keys_delete_confirm_title'),
    message: t('ai_providers.openai_keys_delete_confirm_message'),
    onConfirm: () => removeEntry(idx),
    variant: 'danger',
    confirmText: t('common.delete'),
    cancelText: t('common.cancel'),
  });
};
```

- [ ] **Step 5: 重构 renderKeyEntries 函数中的行渲染**

将原来的表格行替换为新的结构。主要改动:

**5.1 替换状态列:**

原来的状态图标:
```tsx
<div className={styles.keyTableColStatus}>
  <StatusIcon status={keyStatus} />
</div>
```

替换为状态Badge:
```tsx
<div className={styles.keyTableColStatus}>
  <StatusBadge status={keyStatus} message={keyTestStatuses[index]?.message} />
</div>
```

**5.2 替换密钥输入列为输入框组:**

原来的:
```tsx
<div className={styles.keyTableColKey}>
  <input type="text" value={entry.apiKey} ... />
</div>
```

替换为:
```tsx
<div className={styles.keyTableColKey}>
  <div className={styles.keyInputGroup}>
    <div className={styles.keyInputWrapper}>
      <input
        type={visibleKeyIndexes.has(index) ? 'text' : 'password'}
        value={entry.apiKey}
        onChange={(e) => updateEntry(index, { apiKey: e.target.value })}
        disabled={saving || disableControls || isTestingKeys}
        className={`input ${styles.keyTableInput}`}
        placeholder={t('ai_providers.openai_key_placeholder')}
      />
    </div>
    <button
      type="button"
      className={styles.keyInputToggle}
      onClick={() => toggleKeyVisibility(index)}
      title={visibleKeyIndexes.has(index) ? t('common.hide') : t('common.show')}
      disabled={saving || disableControls || isTestingKeys}
    >
      {visibleKeyIndexes.has(index) ? <IconEyeOff size={14} /> : <IconEye size={14} />}
    </button>
    <button
      type="button"
      className={styles.keyInputCopy}
      onClick={() => copyKeyToClipboard(entry.apiKey)}
      title={t('common.copy')}
      disabled={saving || disableControls || isTestingKeys || !entry.apiKey?.trim()}
    >
      <IconCopy size={14} />
    </button>
  </div>
  {/* Proxy URL expandable section */}
  <div className={styles.keyProxySection}>
    <button
      type="button"
      className={styles.keyProxyToggle}
      onClick={() => toggleProxyExpanded(index)}
    >
      {expandedProxyIndexes.has(index) ? <IconChevronUp size={12} /> : <IconChevronDown size={12} />}
      {expandedProxyIndexes.has(index) ? t('ai_providers.openai_proxy_collapse') : t('ai_providers.openai_proxy_expand')}
    </button>
    {expandedProxyIndexes.has(index) && (
      <div className={styles.keyProxyExpanded}>
        <input
          type="text"
          value={entry.proxyUrl ?? ''}
          onChange={(e) => updateEntry(index, { proxyUrl: e.target.value })}
          disabled={saving || disableControls || isTestingKeys}
          className={`input ${styles.keyProxyInput}`}
          placeholder={t('ai_providers.openai_proxy_placeholder')}
        />
      </div>
    )}
  </div>
</div>
```

**5.3 替换权重列为步进器:**

原来的:
```tsx
<div className={styles.keyTableColWeight}>
  <input type="number" min="1" step="1" value={entry.weight ?? 1} ... style={{ width: '60px' }} />
</div>
```

替换为:
```tsx
<div className={styles.keyTableColWeight}>
  <WeightStepper
    value={entry.weight ?? 1}
    onChange={(val) => updateEntry(index, { weight: val })}
    min={1}
    disabled={saving || disableControls || isTestingKeys}
  />
</div>
```

**5.4 替换删除按钮:**

原来的:
```tsx
<Button variant="ghost" size="sm" onClick={() => removeEntry(index)} ...>
  {t('common.delete')}
</Button>
```

替换为:
```tsx
<Button variant="danger" size="sm" onClick={() => confirmRemoveEntry(index)} ...>
  {t('common.delete')}
</Button>
```

- [ ] **Step 6: 添加 StatusBadge 和 WeightStepper 组件**

在组件文件顶部 (StatusIcon 定义之后) 添加:

```tsx
// Status Badge Component
function StatusBadge({ status, message }: { status: KeyTestStatus['status']; message?: string }) {
  const { t } = useTranslation();

  const getBadgeClass = () => {
    switch (status) {
      case 'loading':
        return styles.keyStatusBadgeLoading;
      case 'success':
        return styles.keyStatusBadgeSuccess;
      case 'error':
        return styles.keyStatusBadgeError;
      default:
        return styles.keyStatusBadgeIdle;
    }
  };

  const getLabel = () => {
    switch (status) {
      case 'loading':
        return t('ai_providers.openai_test_status_loading');
      case 'success':
        return t('ai_providers.openai_test_status_success');
      case 'error':
        return t('ai_providers.openai_test_status_error');
      default:
        return t('ai_providers.openai_test_status_idle');
    }
  };

  return (
    <span className={`${styles.keyStatusBadge} ${getBadgeClass()}`} title={message || ''}>
      {status === 'loading' && <LoadingSpinner />}
      {getLabel()}
    </span>
  );
}

// Weight Stepper Component
function WeightStepper({
  value,
  onChange,
  min = 1,
  disabled = false,
}: {
  value: number;
  onChange: (val: number) => void;
  min?: number;
  disabled?: boolean;
}) {
  const handleDecrement = () => {
    if (value > min) {
      onChange(value - 1);
    }
  };

  const handleIncrement = () => {
    onChange(value + 1);
  };

  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const parsed = parseInt(e.target.value, 10);
    if (!isNaN(parsed) && parsed >= min) {
      onChange(parsed);
    }
  };

  return (
    <div className={styles.weightStepper}>
      <button
        type="button"
        className={styles.weightStepperBtn}
        onClick={handleDecrement}
        disabled={disabled || value <= min}
        aria-label="Decrement"
      >
        <IconMinus size={12} />
      </button>
      <input
        type="number"
        className={styles.weightStepperValue}
        value={value}
        onChange={handleInputChange}
        disabled={disabled}
        min={min}
      />
      <button
        type="button"
        className={styles.weightStepperBtn}
        onClick={handleIncrement}
        disabled={disabled}
        aria-label="Increment"
      >
        <IconPlus size={12} />
      </button>
    </div>
  );
}
```

- [ ] **Step 7: 提交**

```bash
git add src/pages/AiProvidersOpenAIEditPage.tsx && git commit -m "feat: implement key list UI optimization - masked input, stepper, badges"
```

---

## Task 4: 添加国际化文案

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/i18n/locales/zh-CN.json`
- Modify: `Cli-Proxy-API-Management-Center/src/i18n/locales/en.json`

- [ ] **Step 1: 在 zh-CN.json 中添加文案**

在 `ai_providers` 部分添加:

```json
"openai_test_status_idle": "未测试",
"openai_test_status_loading": "测试中",
"openai_test_status_success": "可用",
"openai_test_status_error": "异常",
"openai_proxy_expand": "展开代理设置",
"openai_proxy_collapse": "收起代理设置",
"openai_keys_delete_confirm_title": "删除密钥",
"openai_keys_delete_confirm_message": "确定要删除此密钥吗？此操作不可撤销。",
"copied_to_clipboard": "已复制到剪贴板",
"copy_failed": "复制失败",
"common": {
  "show": "显示",
  "hide": "隐藏",
  "copy": "复制"
}
```

- [ ] **Step 2: 在 en.json 中添加对应文案**

```json
"openai_test_status_idle": "Not tested",
"openai_test_status_loading": "Testing",
"openai_test_status_success": "Available",
"openai_test_status_error": "Error",
"openai_proxy_expand": "Expand proxy settings",
"openai_proxy_collapse": "Collapse proxy settings",
"openai_keys_delete_confirm_title": "Delete Key",
"openai_keys_delete_confirm_message": "Are you sure you want to delete this key? This action cannot be undone.",
"copied_to_clipboard": "Copied to clipboard",
"copy_failed": "Copy failed",
"common": {
  "show": "Show",
  "hide": "Hide",
  "copy": "Copy"
}
```

- [ ] **Step 3: 提交**

```bash
git add src/i18n/locales/zh-CN.json src/i18n/locales/en.json && git commit -m "i18n: add key list UI strings for new components"
```

---

## Task 5: 验证和测试

**Files:**
- Modify: `Cli-Proxy-API-Management-Center/src/pages/AiProvidersOpenAIEditPage.tsx`

- [ ] **Step 1: 构建检查**

```bash
cd Cli-Proxy-API-Management-Center && npm run build 2>&1 | head -50
```

预期: 无编译错误

- [ ] **Step 2: 提交**

```bash
git add -A && git commit -m "chore: verify build passes after key list UI changes"
```

---

## 自检清单

- [ ] Spec coverage: 所有设计文档中的功能都有对应实现
- [ ] Placeholder scan: 无 TBD/TODO 占位符
- [ ] Type consistency: 类型一致性检查通过
- [ ] 密钥默认密码遮蔽
- [ ] 眼睛图标切换可见性
- [ ] 复制图标功能
- [ ] 权重步进器增减正常
- [ ] 代理URL可折叠展开
- [ ] 状态Badge显示正确
- [ ] 删除有二次确认
- [ ] 暗色模式适配
- [ ] 移动端响应式

---

**Plan complete.** 文件: `docs/superpowers/plans/2026-05-30-api-key-list-ui-optimization-plan.md`

**Two execution options:**

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
