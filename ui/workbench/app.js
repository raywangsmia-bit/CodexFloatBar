const pageVersion = window.__PAGE_VERSION__;
const workbenchToken = window.__WORKBENCH_TOKEN__;
const autoExport = window.__AUTO_EXPORT__ === true;
const surface = document.querySelector("#exportSurface");
const statisticsSurface = document.querySelector("#statisticsSurface");
const usageToastSurface = document.querySelector("#usageToastSurface");
const trayMenuPreview = document.querySelector("#trayMenuPreview");
const themeSelect = document.querySelector("#themeSelect");
const layoutSelect = document.querySelector("#layoutSelect");
const scenarioSelect = document.querySelector("#scenarioSelect");
const scaleSelect = document.querySelector("#scaleSelect");
const collapseToggle = document.querySelector("#collapseToggle");
const statisticsToggle = document.querySelector("#statisticsToggle");
const toastToggle = document.querySelector("#toastToggle");
const trayToggle = document.querySelector("#trayToggle");
const boundaryToggle = document.querySelector("#boundaryToggle");
const nativePreviewToggle = document.querySelector("#nativePreviewToggle");
const motionPreviewToggle = document.querySelector("#motionPreviewToggle");
const exportButton = document.querySelector("#exportButton");
const exportStatus = document.querySelector("#exportStatus");
const statisticsPeriod = document.querySelector('[data-bind="statistics.month"]');
const statisticsPreviousMonth = document.querySelector(
  '[data-action="statistics-previous-month"]',
);
const statisticsNextMonth = document.querySelector('[data-action="statistics-next-month"]');

let selectedStatisticsView = "month";
let selectedStatisticsMonth = new Date(2026, 7, 1);
let statisticsEarliestMonth = new Date(2025, 8, 1);
let statisticsCurrentMonth = new Date(2026, 7, 1);
let statisticsMonthOverride = null;
let currentStatisticsCellLevels = [];
let selectedStatisticsDay = 0;
let nativePreviewTimer = 0;
let nativePreviewGeneration = 0;
const nativePreviewURLs = new Map();
const previewAnimations = new Map();
const reducedMotionQuery = window.matchMedia("(prefers-reduced-motion: reduce)");
let statisticsMotionAnimations = [];

const exportSurfaces = Object.freeze(
  ["dark", "light"].flatMap((theme) => [
    {
      id: themedSurfaceID("main-horizontal", theme),
      element: surface,
      layout: "horizontal",
      theme,
      scenario: "normal",
    },
    {
      id: themedSurfaceID("main-vertical", theme),
      element: surface,
      layout: "vertical",
      theme,
      scenario: "normal",
    },
    {
      id: themedSurfaceID("statistics", theme),
      element: statisticsSurface,
      theme,
      scenario: "normal",
    },
    ...["good", "warning", "danger", "offline"].map((scenario) => ({
      id: themedSurfaceID(toastSurfaceID(scenario), theme),
      element: usageToastSurface,
      theme,
      scenario,
    })),
  ]),
);

const normalStatistics = Object.freeze({
  total: "18.4M",
  peak: "892K",
  duration: "4h 26m",
  currentStreak: "12 天",
  longestStreak: "31 天",
  month: "2026 年 8 月",
  detailInput: "18.8M",
  detailOutput: "1.8M",
  detailTotal: "20.6M",
  detailCached: "17.9M",
  detailReasoning: "684K",
  detailCacheHit: "95%",
  detailCost: "--",
  cells: [0, 0, 1, 2, 0, 1, 0, 2, 3, 1, 0, 4, 2, 0, 0, 1, 3, 2, 1, 0, 2, 1, 4, 3, 1, 0, 2, 1, 3, 2, 0, 1, 4, 2, 3, 0, 1, 2, 0, 3, 1, 0],
});

const scenarios = {
  normal: {
    tone: "good",
    model: "gpt-5.6-codex",
    effort: "高",
    speed: "快速",
    usage: 72,
    reset: "8/14 09:30 重置",
    toastTitle: "一周额度状态良好",
    toastMessage: "当前剩余 72%，暂无额度压力。",
    statistics: normalStatistics,
  },
  warning: {
    tone: "warn",
    model: "gpt-5.6-codex",
    effort: "高",
    speed: "普通",
    usage: 34,
    reset: "8/12 18:00 重置",
    toastTitle: "一周额度不高于 60%",
    toastMessage: "当前剩余 34%，用量进入提醒区间。",
    statistics: normalStatistics,
  },
  danger: {
    tone: "danger",
    model: "gpt-5.6-codex",
    effort: "超高",
    speed: "繁忙",
    usage: 8,
    reset: "8/10 09:30 重置",
    toastTitle: "一周额度快用完了",
    toastMessage: "当前剩余 8%，建议放慢使用或等待重置。",
    statistics: normalStatistics,
  },
  loading: {
    tone: "offline",
    model: "正在读取…",
    effort: "…",
    speed: "…",
    usage: 0,
    usageText: "…",
    reset: "正在读取用量记录",
    toastTitle: "正在读取额度",
    toastMessage: "请稍候，正在扫描最新的 Codex 状态。",
    statistics: {
      total: "…", peak: "…", duration: "…", currentStreak: "…", longestStreak: "…",
      month: "正在读取", cells: [],
    },
  },
  empty: {
    tone: "offline",
    model: "未读取到模型",
    effort: "—",
    speed: "—",
    usage: 0,
    usageText: "--",
    reset: "等待用量记录",
    toastTitle: "暂无额度记录",
    toastMessage: "Codex 产生用量记录后将在这里显示。",
    statistics: {
      total: "0", peak: "0", duration: "<1分", currentStreak: "0 天", longestStreak: "0 天",
      month: "暂无统计", cells: [],
    },
  },
  error: {
    tone: "danger",
    model: "读取失败",
    effort: "—",
    speed: "—",
    usage: 0,
    usageText: "!",
    reset: "请检查 Codex 文件",
    toastTitle: "状态读取失败",
    toastMessage: "保留上一帧画面；修复文件后会自动重试。",
    statistics: {
      total: "!", peak: "!", duration: "!", currentStreak: "!", longestStreak: "!",
      month: "读取失败", cells: [],
    },
  },
  offline: {
    tone: "offline",
    model: "Codex 未运行",
    effort: "—",
    speed: "—",
    usage: 0,
    reset: "等待 Codex 启动",
    toastTitle: "Codex 未运行",
    toastMessage: "启动 Codex 后将继续读取额度状态。",
    statistics: {
      total: "0", peak: "0", duration: "<1分", currentStreak: "0 天", longestStreak: "0 天",
      month: "Codex 未运行", cells: [],
    },
  },
  "long-text": {
    tone: "warn",
    model: "gpt-5.6-codex-with-a-deliberately-very-long-preview-name",
    effort: "超高推理强度（超长文本测试）",
    speed: "优先高速模式（文本溢出测试）",
    usage: 54,
    reset: "12/31 23:59 很长的重置说明文本",
    toastTitle: "这是一个用于验证省略与边界的超长额度提醒标题",
    toastMessage: "这段提醒内容故意很长，用来确认导出后的文字不会越过窗口边界或覆盖关闭按钮。",
    statistics: {
      ...normalStatistics,
      total: "1234567890.1万",
      month: "2026 年 8 月 · 超长月份标题",
    },
  },
};

themeSelect.addEventListener("change", () => {
  setTheme(themeSelect.value);
  queueNativePreview();
});
layoutSelect.addEventListener("change", () => {
  setLayout(layoutSelect.value);
  queueNativePreview();
});
scenarioSelect.addEventListener("change", () => {
  setScenario(scenarioSelect.value);
  queueNativePreview();
});
scaleSelect.addEventListener("change", () => setPreviewScale(scaleSelect.value));
collapseToggle.addEventListener("change", () => setCollapsePreview(collapseToggle.checked));
statisticsToggle.addEventListener("change", () => syncSurfaceVisibility());
toastToggle.addEventListener("change", () => syncSurfaceVisibility());
trayToggle.addEventListener("change", () => syncSurfaceVisibility());
boundaryToggle.addEventListener("change", () => {
  document.body.classList.toggle("show-slot-boundaries", boundaryToggle.checked);
});
nativePreviewToggle.addEventListener("change", () => {
  if (nativePreviewToggle.checked) {
    queueNativePreview();
  } else {
    clearNativePreview();
  }
});
motionPreviewToggle.addEventListener("change", () => {
  document.body.classList.toggle("motion-preview-enabled", motionPreviewToggle.checked);
  if (motionPreviewToggle.checked) {
    playAuxiliaryMotionPreview();
  } else {
    cancelPreviewMotion();
    syncSurfaceVisibility({ animate: false });
  }
});
reducedMotionQuery.addEventListener("change", () => {
  if (reducedMotionQuery.matches) {
    cancelPreviewMotion();
    syncSurfaceVisibility({ animate: false });
  }
});
for (const button of document.querySelectorAll("[data-statistics-view]")) {
  button.addEventListener("click", () => {
    setStatisticsView(button.dataset.statisticsView);
    queueNativePreview();
  });
}
for (const [index, cell] of Array.from(
  document.querySelectorAll('[data-cells-bind="statistics.monthCells"] i'),
).entries()) {
  cell.dataset.action = `statistics-select-day-${String(index).padStart(2, "0")}`;
  cell.addEventListener("click", () => selectStatisticsDay(index));
}
statisticsPreviousMonth.addEventListener("click", () => shiftStatisticsMonth(-1));
statisticsNextMonth.addEventListener("click", () => shiftStatisticsMonth(1));
exportButton.addEventListener("click", exportBundle);
setTheme(themeSelect.value);
setScenario(scenarioSelect.value);
setStatisticsView(selectedStatisticsView);
setPreviewScale(scaleSelect.value);
setCollapsePreview(collapseToggle.checked);
syncSurfaceVisibility();

setInterval(checkForStaticChanges, 800);
if (autoExport) {
  window.addEventListener("load", () => void runAutomaticExport(), { once: true });
}

function setLayout(layout) {
  surface.classList.toggle("horizontal", layout === "horizontal");
  surface.classList.toggle("vertical", layout === "vertical");
  syncTrayPreview();
}

function setTheme(theme) {
  const normalized = theme === "light" ? "light" : "dark";
  for (const element of [surface, statisticsSurface, usageToastSurface, trayMenuPreview]) {
    element.classList.remove("theme-dark", "theme-light");
    element.classList.add(`theme-${normalized}`);
  }
  syncTrayPreview();
}

function setPreviewScale(value) {
  const scale = Number.parseFloat(value);
  document.documentElement.style.setProperty(
    "--preview-scale",
    Number.isFinite(scale) ? String(scale) : "1",
  );
  syncTrayPreview();
}

function setCollapsePreview(collapsed) {
  document.querySelector(".main-surface-preview").classList.toggle("preview-collapsed", collapsed);
  syncTrayPreview();
}

function syncSurfaceVisibility({ animate = true } = {}) {
  setAuxiliaryPreviewVisibility(
    document.querySelector("#statisticsPreview"),
    statisticsToggle.checked,
    animate,
  );
  setAuxiliaryPreviewVisibility(
    document.querySelector("#toastPreview"),
    toastToggle.checked,
    animate,
  );
  document.querySelector("#trayPreview").classList.toggle(
    "preview-suppressed",
    !trayToggle.checked,
  );
}

function setAuxiliaryPreviewVisibility(element, visible, animate) {
  cancelElementPreviewAnimation(element);
  if (!animate || !shouldPreviewMotion()) {
    element.classList.toggle("preview-suppressed", !visible);
    return;
  }
  if (visible && !element.classList.contains("preview-suppressed")) {
    return;
  }
  if (!visible && element.classList.contains("preview-suppressed")) {
    return;
  }

  if (visible) {
    element.classList.remove("preview-suppressed");
  }
  const keyframes = visible
    ? [
        { opacity: 0, transform: "translateY(6px)" },
        { opacity: 1, transform: "translateY(0)" },
      ]
    : [
        { opacity: 1, transform: "translateY(0)" },
        { opacity: 0, transform: "translateY(0)" },
      ];
  const animation = element.animate(keyframes, {
    duration: visible ? 180 : 130,
    easing: visible ? "cubic-bezier(.2,.8,.2,1)" : "ease-out",
  });
  const record = { animation, visible };
  previewAnimations.set(element, record);
  animation.finished
    .then(() => {
      if (previewAnimations.get(element) !== record) {
        return;
      }
      previewAnimations.delete(element);
      element.classList.toggle("preview-suppressed", !visible);
    })
    .catch(() => {});
}

function shouldPreviewMotion() {
  return Boolean(
    motionPreviewToggle.checked &&
      !reducedMotionQuery.matches &&
      !document.documentElement.classList.contains("export-freeze") &&
      typeof Element.prototype.animate === "function",
  );
}

function playAuxiliaryMotionPreview() {
  if (!shouldPreviewMotion()) {
    return;
  }
  for (const [selector, visible] of [
    ["#statisticsPreview", statisticsToggle.checked],
    ["#toastPreview", toastToggle.checked],
  ]) {
    const element = document.querySelector(selector);
    if (!visible) {
      continue;
    }
    cancelElementPreviewAnimation(element);
    const animation = element.animate(
      [
        { opacity: 0, transform: "translateY(6px)" },
        { opacity: 1, transform: "translateY(0)" },
      ],
      { duration: 180, easing: "cubic-bezier(.2,.8,.2,1)" },
    );
    const record = { animation, visible: true };
    previewAnimations.set(element, record);
    animation.finished
      .then(() => {
        if (previewAnimations.get(element) === record) {
          previewAnimations.delete(element);
        }
      })
      .catch(() => {});
  }
}

function cancelElementPreviewAnimation(element) {
  const record = previewAnimations.get(element);
  if (!record) {
    return;
  }
  previewAnimations.delete(element);
  record.animation.cancel();
}

function cancelPreviewMotion() {
  for (const element of [...previewAnimations.keys()]) {
    cancelElementPreviewAnimation(element);
  }
  clearStatisticsCrossfade();
}

function syncTrayPreview() {
  const theme = themeSelect?.value === "light" ? "浅色" : "深色";
  const layout = layoutSelect?.value === "vertical" ? "竖版" : "横版";
  const scale = Math.round((Number.parseFloat(scaleSelect?.value) || 1) * 100);
  document.querySelector("#trayThemeValue").textContent = `${theme} ✓`;
  document.querySelector("#trayLayoutValue").textContent = `${layout} ✓`;
  document.querySelector("#trayScaleValue").textContent = `${scale}% ✓`;
  document.querySelector("#trayCollapseValue").textContent = collapseToggle?.checked ? "开启 ✓" : "关闭";
}

function setScenario(name) {
  const scenario = scenarios[name] ?? scenarios.normal;
  document.querySelector("#modelValue").textContent = scenario.model;
  document.querySelector("#effortValue").textContent = scenario.effort;
  document.querySelector("#speedValue").textContent = scenario.speed;
  document.querySelector("#usageValue").textContent = scenario.usageText ?? `${scenario.usage}%`;
  document.querySelector("#usageReset").textContent = scenario.reset;
  document.querySelector("#usageFill").style.width = `${scenario.usage}%`;
  document.querySelector("#toastTitle").textContent = scenario.toastTitle;
  document.querySelector("#toastMessage").textContent = scenario.toastMessage;

  const usagePanel = document.querySelector("#usagePanel");
  usagePanel.classList.remove("good", "warn", "danger", "offline");
  usagePanel.classList.add(scenario.tone);
  usageToastSurface.classList.remove("good", "warn", "danger", "offline");
  usageToastSurface.classList.add(scenario.tone);

  const statistics = scenario.statistics ?? normalStatistics;
  for (const binding of ["total", "peak", "duration", "currentStreak", "longestStreak", "month"]) {
    document.querySelector(`[data-bind="statistics.${binding}"]`).textContent = statistics[binding];
  }
  currentStatisticsCellLevels = [...(statistics.cells ?? [])];
  for (const binding of [
    "detailInput", "detailOutput", "detailTotal", "detailCached",
    "detailReasoning", "detailCacheHit", "detailCost",
  ]) {
    document.querySelector(`[data-bind="statistics.${binding}"]`).textContent =
      statistics[binding] ?? "--";
  }
  const parsedMonth = parseStatisticsMonth(statistics.month);
  if (parsedMonth) {
    selectedStatisticsMonth = parsedMonth;
    statisticsCurrentMonth = parsedMonth;
    statisticsEarliestMonth = new Date(parsedMonth.getFullYear(), parsedMonth.getMonth() - 11, 1);
    statisticsMonthOverride = null;
    selectedStatisticsDay = 0;
  } else {
    statisticsMonthOverride = statistics.month;
  }
  renderStatisticsMonthCells();
  setStatisticsView(selectedStatisticsView);
}

function renderStatisticsMonthCells() {
  const cells = Array.from(
    document.querySelectorAll('[data-cells-bind="statistics.monthCells"] i'),
  );
  const hasCalendarMonth =
    !statisticsMonthOverride && !Number.isNaN(selectedStatisticsMonth.getTime());
  let firstOffset = 0;
  let daysInMonth = 0;
  if (hasCalendarMonth) {
    const firstWeekday = new Date(
      selectedStatisticsMonth.getFullYear(),
      selectedStatisticsMonth.getMonth(),
      1,
    ).getDay();
    firstOffset = (firstWeekday + 6) % 7;
    daysInMonth = new Date(
      selectedStatisticsMonth.getFullYear(),
      selectedStatisticsMonth.getMonth() + 1,
      0,
    ).getDate();
  }

  cells.forEach((cell, index) => {
    const isCalendarDay =
      hasCalendarMonth && index >= firstOffset && index < firstOffset + daysInMonth;
    cell.classList.remove("l1", "l2", "l3", "l4", "selected");
    cell.classList.toggle("outside-month", !isCalendarDay);
    const level = isCalendarDay ? currentStatisticsCellLevels[index] ?? 0 : 0;
    if (level > 0) {
      cell.classList.add(`l${Math.min(4, level)}`);
    }
    const day = index - firstOffset + 1;
    cell.classList.toggle("selected", isCalendarDay && day === selectedStatisticsDay);
  });
}

function setStatisticsView(view) {
  const nextView = ["month", "week", "cumulative", "detail"].includes(view)
    ? view
    : "month";
  const crossfadeGhost =
    nextView !== selectedStatisticsView ? createStatisticsCrossfadeGhost() : null;
  selectedStatisticsView = nextView;
  for (const button of document.querySelectorAll("[data-statistics-view]")) {
    button.classList.toggle("active", button.dataset.statisticsView === selectedStatisticsView);
  }
  for (const panel of document.querySelectorAll("[data-statistics-view-panel]")) {
    const active = panel.dataset.statisticsViewPanel === selectedStatisticsView;
    panel.classList.toggle("active", active);
    panel.setAttribute("aria-hidden", active ? "false" : "true");
  }
  const isMonth = selectedStatisticsView === "month" || selectedStatisticsView === "detail";
  statisticsPreviousMonth.textContent =
    isMonth && !statisticsMonthOverride && canShiftStatisticsMonth(-1) ? "‹" : "";
  statisticsNextMonth.textContent =
    isMonth && !statisticsMonthOverride && canShiftStatisticsMonth(1) ? "›" : "";
  if (selectedStatisticsView === "detail" && selectedStatisticsDay > 0) {
    statisticsPeriod.textContent = `${selectedStatisticsMonth.getFullYear()}-${String(
      selectedStatisticsMonth.getMonth() + 1,
    ).padStart(2, "0")}-${String(selectedStatisticsDay).padStart(2, "0")}`;
  } else if (isMonth) {
    statisticsPeriod.textContent =
      statisticsMonthOverride ?? formatStatisticsMonth(selectedStatisticsMonth);
  } else if (selectedStatisticsView === "week") {
    statisticsPeriod.textContent = "最近 13 周";
  } else {
    statisticsPeriod.textContent = "近 12 月累计";
  }
  if (crossfadeGhost) {
    startStatisticsCrossfade(crossfadeGhost);
  }
}

function createStatisticsCrossfadeGhost() {
  if (
    !shouldPreviewMotion() ||
    document.querySelector("#statisticsPreview").classList.contains("preview-suppressed")
  ) {
    return null;
  }
  clearStatisticsCrossfade();
  const bounds = statisticsSurface.getBoundingClientRect();
  if (bounds.width <= 0 || bounds.height <= 0) {
    return null;
  }
  const ghost = statisticsSurface.cloneNode(true);
  ghost.removeAttribute("id");
  for (const node of ghost.querySelectorAll("[id]")) {
    node.removeAttribute("id");
  }
  ghost.classList.add("statistics-crossfade-ghost");
  ghost.setAttribute("aria-hidden", "true");
  ghost.style.left = `${bounds.left}px`;
  ghost.style.top = `${bounds.top}px`;
  ghost.style.width = `${statisticsSurface.offsetWidth}px`;
  ghost.style.height = `${statisticsSurface.offsetHeight}px`;
  ghost.style.transform = `scale(${bounds.width / statisticsSurface.offsetWidth})`;
  document.body.append(ghost);
  return ghost;
}

function startStatisticsCrossfade(ghost) {
  const outgoing = ghost.animate([{ opacity: 1 }, { opacity: 0 }], {
    duration: 120,
    easing: "ease-out",
  });
  const incoming = statisticsSurface.animate([{ opacity: 0 }, { opacity: 1 }], {
    duration: 120,
    easing: "ease-out",
  });
  statisticsMotionAnimations = [outgoing, incoming];
  Promise.allSettled([outgoing.finished, incoming.finished]).then(() => {
    if (statisticsMotionAnimations[0] !== outgoing) {
      return;
    }
    statisticsMotionAnimations = [];
    ghost.remove();
  });
}

function clearStatisticsCrossfade() {
  for (const animation of statisticsMotionAnimations) {
    animation.cancel();
  }
  statisticsMotionAnimations = [];
  for (const ghost of document.querySelectorAll(".statistics-crossfade-ghost")) {
    ghost.remove();
  }
}

function shiftStatisticsMonth(delta) {
  if (
    !["month", "detail"].includes(selectedStatisticsView) ||
    statisticsMonthOverride ||
    !canShiftStatisticsMonth(delta)
  ) {
    return;
  }
  selectedStatisticsMonth = new Date(
    selectedStatisticsMonth.getFullYear(),
    selectedStatisticsMonth.getMonth() + delta,
    1,
  );
  selectedStatisticsDay = 0;
  renderStatisticsMonthCells();
  setStatisticsView(selectedStatisticsView);
}

function selectStatisticsDay(index) {
  if (statisticsMonthOverride) {
    return;
  }
  const firstWeekday = new Date(
    selectedStatisticsMonth.getFullYear(),
    selectedStatisticsMonth.getMonth(),
    1,
  ).getDay();
  const firstOffset = (firstWeekday + 6) % 7;
  const day = index - firstOffset + 1;
  const daysInMonth = new Date(
    selectedStatisticsMonth.getFullYear(),
    selectedStatisticsMonth.getMonth() + 1,
    0,
  ).getDate();
  if (day < 1 || day > daysInMonth) {
    return;
  }
  selectedStatisticsDay = selectedStatisticsDay === day ? 0 : day;
  renderStatisticsMonthCells();
  setStatisticsView("detail");
  queueNativePreview();
}

function canShiftStatisticsMonth(delta) {
  const candidate = new Date(
    selectedStatisticsMonth.getFullYear(),
    selectedStatisticsMonth.getMonth() + delta,
    1,
  );
  return candidate >= statisticsEarliestMonth && candidate <= statisticsCurrentMonth;
}

function parseStatisticsMonth(value) {
  const match = String(value ?? "").match(/^(\d{4})\s*年\s*(\d{1,2})\s*月$/);
  if (!match) {
    return null;
  }
  return new Date(Number(match[1]), Number(match[2]) - 1, 1);
}

function formatStatisticsMonth(value) {
  return `${value.getFullYear()} 年 ${value.getMonth() + 1} 月`;
}

async function exportBundle() {
  exportButton.disabled = true;
  exportButton.setAttribute("aria-busy", "true");
  setStatus("正在检查现有导出资源…", "");

  const originalLayout = layoutSelect.value;
  const originalTheme = themeSelect.value;
  const originalScenario = scenarioSelect.value;
  const originalScale = scaleSelect.value;
  const originalCollapsed = collapseToggle.checked;
  const originalStatisticsVisible = statisticsToggle.checked;
  const originalToastVisible = toastToggle.checked;
  const originalTrayVisible = trayToggle.checked;
  const originalNativePreview = nativePreviewToggle.checked;
  const originalStatisticsView = selectedStatisticsView;
  const originalStatisticsMonth = new Date(selectedStatisticsMonth);
  const originalStatisticsDay = selectedStatisticsDay;
  const defaultSurface = themedSurfaceID(`main-${originalLayout}`, originalTheme);
  const scales = [1, 1.25, 1.5, 2];
  const surfaces = [];
  const files = [];
  let exportFrozen = false;

  try {
    const preflight = await requestExportPreflight(defaultSurface);
    if (preflight.upToDate) {
      setStatus(`导出已是最新：复用 ${preflight.reused} 个资源`, "success");
      return successfulExportOutcome(preflight);
    }
    const renderer = await requireEdgeWorkbenchExport();
    setStatus("资源有变化，正在用 Edge 渲染四档 DPI 图集…", "");
    setExportFreeze(true);
    exportFrozen = true;
    await document.fonts.ready;
    const stylesheets = await collectStylesheets();
    setPreviewScale("1");
    collapseToggle.checked = false;
    statisticsToggle.checked = true;
    toastToggle.checked = true;
    trayToggle.checked = true;
    nativePreviewToggle.checked = false;
    clearNativePreview();
    setCollapsePreview(false);
    syncSurfaceVisibility();
    setStatisticsView("month");

    for (const definition of exportSurfaces) {
      setTheme(definition.theme);
      setScenario(definition.scenario);
      if (definition.layout) {
        setLayout(definition.layout);
      }
      await nextFrame();

      const bounds = definition.element.getBoundingClientRect();
      const logicalWidth = Math.round(bounds.width);
      const logicalHeight = Math.round(bounds.height);
      const variants = [];
      const dynamic = collectDynamicSlots(definition.element, bounds);
      const hitRegions = Array.from(definition.element.querySelectorAll("[data-action]")).map(
        (element) => {
          const region = element.getBoundingClientRect();
          return {
            action: element.dataset.action,
            x: region.left - bounds.left,
            y: region.top - bounds.top,
            width: region.width,
            height: region.height,
          };
        },
      );

      for (const scale of scales) {
        const file = `${definition.id}@${Math.round(scale * 100)}.png`;
        const capture = await renderElement(
          definition.element,
          stylesheets,
          logicalWidth,
          logicalHeight,
          scale,
        );
        files.push({
          name: file,
          html: capture.html,
          width: capture.width,
          height: capture.height,
        });
        variants.push({
          scale,
          file,
          width: capture.width,
          height: capture.height,
        });
      }

      surfaces.push({
        id: definition.id,
        logicalWidth,
        logicalHeight,
        hitRegions,
        dynamic,
        variants,
      });
    }

    const response = await fetch("/api/export", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Codex-Workbench-Token": workbenchToken,
      },
      body: JSON.stringify({
        manifest: {
          schema: 2,
          project: "codexfloatingbar",
          defaultSurface,
          version: pageVersion,
          surfaces,
        },
        files,
        renderer,
      }),
    });
    if (!response.ok) {
      throw new Error(await response.text());
    }
    const result = await response.json();
    const atlasSummary = result.atlases > 0 ? `，Edge 图集 ${result.atlases} 张` : "";
    const fallbackSummary =
      result.fallback > 0 ? `，${result.fallback} 个资源已显式回退逐图渲染` : "";
    setStatus(
      `导出完成：重新渲染 ${result.rendered} 个，复用 ${result.reused} 个资源${atlasSummary}${fallbackSummary}`,
      "success",
    );
    return successfulExportOutcome(result);
  } catch (error) {
    const summary = error instanceof Error ? error.message : String(error);
    setStatus(
      `导出失败：${summary}；可再次点击重试`,
      "error",
    );
    return { ok: false, error: summary };
  } finally {
    setLayout(originalLayout);
    setTheme(originalTheme);
    setScenario(originalScenario);
    selectedStatisticsMonth = originalStatisticsMonth;
    selectedStatisticsDay = originalStatisticsDay;
    renderStatisticsMonthCells();
    setStatisticsView(originalStatisticsView);
    setPreviewScale(originalScale);
    collapseToggle.checked = originalCollapsed;
    statisticsToggle.checked = originalStatisticsVisible;
    toastToggle.checked = originalToastVisible;
    trayToggle.checked = originalTrayVisible;
    nativePreviewToggle.checked = originalNativePreview;
    setCollapsePreview(originalCollapsed);
    syncSurfaceVisibility({ animate: false });
    if (exportFrozen) {
      setExportFreeze(false);
    }
    exportButton.disabled = false;
    exportButton.removeAttribute("aria-busy");
    if (originalNativePreview) {
      queueNativePreview();
    }
  }
}

function successfulExportOutcome(result) {
  return {
    ok: true,
    error: "",
    upToDate: result.upToDate === true,
    files: Number(result.files) || 0,
    rendered: Number(result.rendered) || 0,
    reused: Number(result.reused) || 0,
    atlases: Number(result.atlases) || 0,
    fallback: Number(result.fallback) || 0,
  };
}

async function runAutomaticExport() {
  let outcome;
  try {
    outcome = await exportBundle();
  } catch (error) {
    outcome = {
      ok: false,
      error: error instanceof Error ? error.message : String(error),
    };
  }
  try {
    const response = await fetch("/api/export/result", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Codex-Workbench-Token": workbenchToken,
      },
      body: JSON.stringify(outcome),
    });
    if (!response.ok) {
      throw new Error((await response.text()).trim());
    }
  } catch (error) {
    setStatus(
      `自动导出结果回报失败：${error instanceof Error ? error.message : String(error)}`,
      "error",
    );
  }
}

async function requestExportPreflight(defaultSurface) {
  const response = await fetch("/api/export/preflight", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Codex-Workbench-Token": workbenchToken,
    },
    body: JSON.stringify({ defaultSurface, version: pageVersion }),
  });
  if (!response.ok) {
    throw new Error(`导出预检失败：${(await response.text()).trim()}`);
  }
  return response.json();
}

async function requireEdgeWorkbenchExport() {
  const match = navigator.userAgent.match(/\bEdg\/([0-9]+(?:\.[0-9]+){3})\b/);
  if (!match) {
    throw new Error(
      "资源需要重绘时请用 Microsoft Edge 打开工作台，以保证测量与最终栅格使用同一 Chromium/Edge 引擎",
    );
  }
  if (navigator.userAgentData?.getHighEntropyValues) {
    const values = await navigator.userAgentData.getHighEntropyValues(["fullVersionList"]);
    const edge = values.fullVersionList?.find(({ brand }) => brand === "Microsoft Edge");
    if (edge?.version) {
      return edge.version;
    }
  }
  if (!match[1].endsWith(".0.0.0")) {
    return match[1];
  }
  throw new Error(
    "无法读取 Microsoft Edge 完整版本，不能安全地与服务端导出引擎核对；请更新 Edge 后重试",
  );
}

function setExportFreeze(frozen) {
  document.documentElement.classList.toggle("export-freeze", frozen);
  if (frozen) {
    cancelPreviewMotion();
    syncSurfaceVisibility({ animate: false });
  }
}

function queueNativePreview() {
  if (!nativePreviewToggle.checked) {
    return;
  }
  window.clearTimeout(nativePreviewTimer);
  nativePreviewTimer = window.setTimeout(() => void refreshNativePreview(), 100);
}

async function refreshNativePreview() {
  if (!nativePreviewToggle.checked) {
    return;
  }
  const generation = ++nativePreviewGeneration;
  const theme = themeSelect.value === "light" ? "light" : "dark";
  const scenario = scenarios[scenarioSelect.value] ?? scenarios.normal;
  const definitions = [
    {
      element: surface,
      surfaceID: themedSurfaceID(`main-${layoutSelect.value}`, theme),
    },
    {
      element: statisticsSurface,
      surfaceID: themedSurfaceID("statistics", theme),
    },
    {
      element: usageToastSurface,
      surfaceID: themedSurfaceID(toastSurfaceID(scenarioSelect.value), theme),
    },
  ];
  const text = Object.fromEntries(
    Array.from(document.querySelectorAll("[data-bind]")).map((node) => [
      node.dataset.bind,
      node.textContent,
    ]),
  );
  const monthCells = Array.from(
    document.querySelectorAll('[data-cells-bind="statistics.monthCells"] i'),
  ).map((cell, index) =>
    cell.classList.contains("outside-month")
      ? -1
      : cell.classList.contains("selected")
        ? 5
        : currentStatisticsCellLevels[index] ?? 0,
  );
  const presentation = {
    text,
    progress: { "quota.progress": scenario.usage },
    cells: { "statistics.monthCells": monthCells },
    tone: scenario.tone,
    statisticsView: selectedStatisticsView,
    chartValues:
      selectedStatisticsView === "week"
        ? [32, 51, 44, 76, 63, 81, 55, 92, 86, 104, 97, 121, 116]
        : selectedStatisticsView === "cumulative"
          ? [18, 31, 48, 72, 96, 128, 164, 203, 249, 302, 361, 426]
          : [],
  };

  try {
    const images = await Promise.all(
      definitions.map(async (definition) => {
        const response = await fetch("/api/native-preview", {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "X-Codex-Workbench-Token": workbenchToken,
          },
          body: JSON.stringify({ surfaceId: definition.surfaceID, ...presentation }),
        });
        if (!response.ok) {
          throw new Error(await response.text());
        }
        return { definition, blob: await response.blob() };
      }),
    );
    if (generation !== nativePreviewGeneration || !nativePreviewToggle.checked) {
      return;
    }
    for (const { definition, blob } of images) {
      installNativePreviewImage(definition.element, blob);
    }
    setStatus("原生合成预览已更新（与程序同一渲染路径）", "success");
  } catch (error) {
    if (generation === nativePreviewGeneration) {
      clearNativePreview();
      nativePreviewToggle.checked = false;
      setStatus(
        `原生预览失败：${error instanceof Error ? error.message : String(error)}`,
        "error",
      );
    }
  }
}

function installNativePreviewImage(element, blob) {
  const wrapper = element.closest(".surface-preview");
  let image = wrapper.querySelector(":scope > .native-preview-image");
  if (!image) {
    image = document.createElement("img");
    image.className = "native-preview-image";
    image.alt = `${wrapper.getAttribute("aria-label") ?? "窗口"}原生合成结果`;
    wrapper.append(image);
  }
  const previousURL = nativePreviewURLs.get(image);
  if (previousURL) {
    URL.revokeObjectURL(previousURL);
  }
  const nextURL = URL.createObjectURL(blob);
  nativePreviewURLs.set(image, nextURL);
  image.src = nextURL;
  wrapper.classList.add("native-preview-active");
}

function clearNativePreview() {
  nativePreviewGeneration++;
  for (const [image, objectURL] of nativePreviewURLs) {
    URL.revokeObjectURL(objectURL);
    image.remove();
  }
  nativePreviewURLs.clear();
  for (const wrapper of document.querySelectorAll(".native-preview-active")) {
    wrapper.classList.remove("native-preview-active");
  }
}

function themedSurfaceID(base, theme) {
  return theme === "light" ? `${base}-light` : base;
}

function toastSurfaceID(scenario) {
  switch (scenario) {
    case "normal":
    case "good":
      return "usage-toast-good";
    case "danger":
      return "usage-toast-danger";
    case "offline":
      return "usage-toast-offline";
    default:
      return "usage-toast";
  }
}

async function collectStylesheets() {
  const stylesheets = Array.from(
    new Set([...document.styleSheets, ...(document.adoptedStyleSheets ?? [])]),
  ).filter((stylesheet) => !stylesheet.disabled);
  const contents = await Promise.all(
    stylesheets.map(async (stylesheet) => {
      try {
        return {
          media: stylesheet.media?.mediaText ?? "",
          rules: Array.from(stylesheet.cssRules),
        };
      } catch (error) {
        if (!stylesheet.href) {
          throw error;
        }
      }

      const response = await fetch(stylesheet.href, { cache: "no-store" });
      if (!response.ok) {
        throw new Error(`无法读取样式表 ${stylesheet.href}`);
      }
      return {
        contents: await response.text(),
        media: stylesheet.media?.mediaText ?? "",
      };
    }),
  );
  return contents;
}

function collectSurfaceStylesheet(stylesheets, element) {
  return stylesheets
    .map((stylesheet) => {
      const contents = stylesheet.rules
        ? serializeRelevantRules(stylesheet.rules, element)
        : stylesheet.contents;
      if (!contents) {
        return "";
      }
      return stylesheet.media ? `@media ${stylesheet.media}{${contents}}` : contents;
    })
    .filter(Boolean)
    .join("\n");
}

function serializeRelevantRules(rules, element) {
  return Array.from(rules, (rule) => serializeRelevantRule(rule, element))
    .filter(Boolean)
    .join("\n");
}

function serializeRelevantRule(rule, element) {
  if ("selectorText" in rule) {
    return selectorAffectsExportTree(rule.selectorText, element) ? rule.cssText : "";
  }
  if (!("cssRules" in rule)) {
    return rule.cssText;
  }

  const nested = serializeRelevantRules(rule.cssRules, element);
  if (!nested) {
    return "";
  }
  const openingBrace = rule.cssText.indexOf("{");
  const closingBrace = rule.cssText.lastIndexOf("}");
  if (openingBrace < 0 || closingBrace <= openingBrace) {
    return rule.cssText;
  }
  return `${rule.cssText.slice(0, openingBrace + 1)}${nested}${rule.cssText.slice(closingBrace)}`;
}

function selectorAffectsExportTree(selector, element) {
  const targetsDocumentRoot = /(^|,)\s*(?::root|html|body)(?=[\s,.:#\[>+~]|$)/.test(selector);
  if (targetsDocumentRoot) {
    return true;
  }
  try {
    return element.matches(selector) || element.querySelector(selector) !== null;
  } catch {
    return true;
  }
}

async function renderElement(element, stylesheets, width, height, scale) {
  const clone = element.cloneNode(true);
  clone.style.margin = "0";
  clone.style.transform = "none";
  for (const text of clone.querySelectorAll("[data-bind]")) {
    text.style.setProperty("color", "transparent", "important");
    text.style.setProperty("-webkit-text-fill-color", "transparent", "important");
    text.style.setProperty("text-shadow", "none", "important");
  }
  for (const control of clone.querySelectorAll("[data-statistics-tab], [data-statistics-navigation]")) {
    control.style.setProperty("background", "transparent", "important");
    control.style.setProperty("border-color", "transparent", "important");
  }
  for (const progress of clone.querySelectorAll("[data-progress-bind]")) {
    progress.style.setProperty("background", "transparent", "important");
  }
  for (const cell of clone.querySelectorAll("[data-cells-bind] i")) {
    cell.classList.remove("l1", "l2", "l3", "l4", "selected", "outside-month");
    cell.style.setProperty("background", "transparent", "important");
  }

  const stylesheet = collectSurfaceStylesheet(stylesheets, clone);
  const markup = new XMLSerializer().serializeToString(clone);
  const outputWidth = Math.round(width * scale);
  const outputHeight = Math.round(height * scale);
  const html = [
    "<!doctype html><html class=\"export-freeze\"><head><meta charset=\"utf-8\" /><style>",
    stylesheet,
    "</style><style>",
    `html,body{width:${width}px!important;height:${height}px!important;`,
    "min-width:0!important;min-height:0!important;margin:0!important;",
    "padding:0!important;overflow:hidden!important;background:transparent!important}",
    `body{zoom:${scale}}`,
    "</style></head><body>",
    markup,
    "</body></html>",
  ].join("");

  return { html, width: outputWidth, height: outputHeight };
}

function collectDynamicSlots(element, surfaceBounds) {
  const text = Array.from(element.querySelectorAll("[data-bind]")).map((node) => {
    const bounds = node.getBoundingClientRect();
    const box = node.dataset.bindBox === "parent" ? node.parentElement.getBoundingClientRect() : bounds;
    const style = getComputedStyle(node);
    const fontFamilies = parsedFontFamilies(style.fontFamily);
    const slot = {
      bind: node.dataset.bind,
      rect: relativeRect(
        node.dataset.bindBox === "parent" ? box.left : bounds.left,
        bounds.top,
        node.dataset.bindBox === "parent" ? box.width : bounds.width,
        bounds.height,
        surfaceBounds,
      ),
      fontFamily: fontFamilies[0],
      fontFamilies,
      fontSize: Number.parseFloat(style.fontSize),
      fontWeight: normalizedFontWeight(style.fontWeight),
      color: colorToHex(style.color),
      align: normalizedTextAlign(node.dataset.align ?? style.textAlign),
    };
    if (node.hasAttribute("data-tone-color")) {
      slot.toneColors = collectToneColors(node, "color");
    }
    if (node.hasAttribute("data-statistics-tab")) {
      slot.toneColors = collectStatisticsTabColors(node);
    }
    return slot;
  });

  const progress = Array.from(element.querySelectorAll("[data-progress-bind]")).map((node) => {
    const bounds = node.parentElement.getBoundingClientRect();
    const slot = {
      bind: node.dataset.progressBind,
      rect: relativeRect(bounds.left, bounds.top, bounds.width, bounds.height, surfaceBounds),
      color: colorToHex(getComputedStyle(node).backgroundColor),
    };
    if (node.hasAttribute("data-tone-color")) {
      slot.toneColors = collectToneColors(node, "backgroundColor");
    }
    return slot;
  });

  const cells = Array.from(element.querySelectorAll("[data-cells-bind]")).map((group) => {
    const backgroundHost = group.closest(".activity-card") ?? group;
    return {
      bind: group.dataset.cellsBind,
      rects: Array.from(group.querySelectorAll("i")).map((cell) => {
        const bounds = cell.getBoundingClientRect();
        return relativeRect(bounds.left, bounds.top, bounds.width, bounds.height, surfaceBounds);
      }),
      colors: collectCellColors(group),
      backgroundColor: colorToHex(getComputedStyle(backgroundHost).backgroundColor),
    };
  });
  return { text, progress, cells };
}

function relativeRect(left, top, width, height, surfaceBounds) {
  return {
    x: left - surfaceBounds.left,
    y: top - surfaceBounds.top,
    width,
    height,
  };
}

function collectToneColors(node, property) {
  const host = node.closest('[data-tone-bind="quota"]');
  if (!host) {
    return {};
  }
  const originalClass = host.className;
  const result = {};
  for (const tone of ["good", "warn", "danger", "offline"]) {
    host.classList.remove("good", "warn", "danger", "offline");
    host.classList.add(tone);
    result[tone] = colorToHex(getComputedStyle(node)[property]);
  }
  host.className = originalClass;
  return result;
}

function collectCellColors(group) {
  const cell = group.querySelector("i");
  if (!cell) {
    return [];
  }
  const originalClass = cell.className;
  const colors = [];
  for (const level of ["", "l1", "l2", "l3", "l4", "selected"]) {
    cell.className = level;
    colors.push(colorToHex(getComputedStyle(cell).backgroundColor));
  }
  cell.className = originalClass;
  return colors;
}

function collectStatisticsTabColors(node) {
  const wasActive = node.classList.contains("active");
  node.classList.remove("active");
  const inactiveStyle = getComputedStyle(node);
  const inactive = colorToHex(inactiveStyle.color);
  const inactiveBackground = colorToHex(inactiveStyle.backgroundColor);
  node.classList.add("active");
  const activeStyle = getComputedStyle(node);
  const active = colorToHex(activeStyle.color);
  const activeBackground = colorToHex(activeStyle.backgroundColor);
  node.classList.toggle("active", wasActive);
  return {
    good: active,
    warn: activeBackground,
    danger: inactiveBackground,
    offline: inactive,
  };
}

function parsedFontFamilies(value) {
  const families = [];
  let current = "";
  let quote = "";
  for (const character of value) {
    if ((character === '"' || character === "'") && (!quote || quote === character)) {
      quote = quote ? "" : character;
      continue;
    }
    if (character === "," && !quote) {
      appendFontFamily(families, current);
      current = "";
      continue;
    }
    current += character;
  }
  appendFontFamily(families, current);
  if (families.length === 0) {
    throw new Error(`无法解析字体 ${value}`);
  }
  return families.slice(0, 8);
}

function appendFontFamily(families, value) {
  const family = value.trim();
  if (family && !families.includes(family)) {
    families.push(family);
  }
}

function normalizedFontWeight(value) {
  const numeric = Number.parseInt(value, 10);
  if (Number.isFinite(numeric)) {
    return numeric;
  }
  return value === "bold" ? 700 : 400;
}

function normalizedTextAlign(value) {
  if (value === "center" || value === "right") {
    return value;
  }
  return "left";
}

function colorToHex(value) {
  const match = value.match(/rgba?\(([^)]+)\)/i);
  if (!match) {
    throw new Error(`无法解析颜色 ${value}`);
  }
  const parts = match[1].split(/[,\s/]+/).filter(Boolean).map(Number);
  const alpha = parts.length > 3 ? Math.round(parts[3] * 255) : 255;
  return `#${[parts[0], parts[1], parts[2], alpha]
    .map((part) => Math.max(0, Math.min(255, Math.round(part))).toString(16).padStart(2, "0"))
    .join("")}`;
}

async function checkForStaticChanges() {
  try {
    const response = await fetch("/api/meta", { cache: "no-store" });
    if (!response.ok) {
      return;
    }
    const current = await response.json();
    if (current.staticVersion !== pageVersion.staticVersion) {
      window.location.reload();
    }
  } catch {
    // The development server may be restarting; keep the current preview visible.
  }
}

function nextFrame() {
  return new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
}

function setStatus(message, tone) {
  exportStatus.textContent = message;
  exportStatus.className = tone;
}
