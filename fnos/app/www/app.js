// Mihomo 面板前端逻辑。
// 只依赖原生 fetch，不引入任何框架 —— 在 fnOS 桌面的 iframe 里跑得更稳。

async function api(path, opts) {
  const res = await fetch(path, Object.assign({headers: {"Content-Type": "application/json"}}, opts || {}));
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch (e) { data = {error: text}; }
  if (res.status === 401) {
    // 会话没了（没登录、口令改了、面板重启了）：切回登录页。
    const msg = (data && data.error) || "需要登录";
    showLogin();
    const err = new Error(msg);
    err.auth = true;
    throw err;
  }
  if (!res.ok || (data && data.error)) {
    throw new Error((data && data.error) || ("HTTP " + res.status));
  }
  return data;
}

// loginRequired 表示当前停在登录页。
// 这期间的报错不该再弹提示 —— 用户正盯着登录框，弹窗只会添乱。
let loginRequired = false;

function toast(msg, kind) {
  if (loginRequired) return;
  const el = document.getElementById("toast");
  el.textContent = msg;
  el.className = "show " + (kind || "");
  clearTimeout(el._t);
  el._t = setTimeout(() => { el.className = ""; }, 4000);
}

// esc 用于把文本安全地插入 HTML，避免节点名里的特殊字符破坏页面结构。
function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"]/g, function(c) {
    return {"&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;"}[c];
  });
}

// ---- 自绘滚动条 ----
// 浏览器原生滚动条在暗色页面上是一条突兀的白杆，而宿主（fnOS 桌面的 iframe）
// 又管不到滚动条样式，所以这里自己画一根：轨道透明，把手细圆角。
// 支持拖动、点轨道空白翻页，内容高度变化时自动重算长度。
function attachScrollbar(el) {
  if (!el) return null;
  const host = el.parentElement;
  if (!host) return null;
  const track = document.createElement("div");
  track.className = "cs-track";
  const thumb = document.createElement("div");
  thumb.className = "cs-thumb";
  track.appendChild(thumb);
  host.appendChild(track);

  let raf = 0;
  function sync() {
    raf = 0;
    const view = track.clientHeight;
    const total = el.scrollHeight - el.clientHeight;
    // 内容没超出可视区就不画把手，避免留一条没用的空轨道。
    if (!view || total <= 1) { track.classList.remove("show"); return; }
    track.classList.add("show");
    const h = Math.max(30, Math.round(view * (el.clientHeight / el.scrollHeight)));
    const maxTop = Math.max(0, view - h);
    thumb.style.height = h + "px";
    thumb.style.top = (maxTop * (el.scrollTop / total)) + "px";
  }
  function schedule() { if (!raf) raf = requestAnimationFrame(sync); }

  el.addEventListener("scroll", schedule, {passive: true});
  window.addEventListener("resize", schedule);
  if (window.ResizeObserver) {
    const ro = new ResizeObserver(schedule);
    ro.observe(el);
    if (el.firstElementChild) ro.observe(el.firstElementChild);
  }
  if (window.MutationObserver) {
    new MutationObserver(schedule).observe(el, {childList: true, subtree: true, characterData: true});
  }

  // 拖动把手：把手高度按内容比例算，位移要按同样的比例换算成 scrollTop。
  let dragging = false, startY = 0, startTop = 0;
  thumb.addEventListener("pointerdown", (e) => {
    dragging = true;
    startY = e.clientY;
    startTop = el.scrollTop;
    track.classList.add("drag");
    thumb.setPointerCapture(e.pointerId);
    e.preventDefault();
    e.stopPropagation();
  });
  thumb.addEventListener("pointermove", (e) => {
    if (!dragging) return;
    const total = el.scrollHeight - el.clientHeight;
    const maxTop = track.clientHeight - thumb.offsetHeight;
    if (maxTop <= 0) return;
    el.scrollTop = startTop + (e.clientY - startY) * (total / maxTop);
  });
  function endDrag(e) {
    if (!dragging) return;
    dragging = false;
    track.classList.remove("drag");
    try { thumb.releasePointerCapture(e.pointerId); } catch (err) { /* 指针已释放 */ }
  }
  thumb.addEventListener("pointerup", endDrag);
  thumb.addEventListener("pointercancel", endDrag);

  // 点轨道空白处翻一屏，和原生滚动条的行为一致。
  track.addEventListener("pointerdown", (e) => {
    if (e.target === thumb) return;
    const r = thumb.getBoundingClientRect();
    el.scrollBy({top: (e.clientY < r.top ? -1 : 1) * el.clientHeight * 0.9, behavior: "smooth"});
  });

  sync();
  return {sync: schedule};
}

// ---- 登录 ----
// 面板没设访问密码时后端不拦请求，直接进主界面；
// 设了口令就要先过登录页，登录态由后端的会话 Cookie 承载。
let authState = {enabled: false, authenticated: true, user: ""};
let statusTimer = null;

// showLogin 切到登录页。msg 非空时把提示写进登录框。
function showLogin(msg) {
  loginRequired = true;
  document.body.classList.add("locked");
  // 轮询停掉：停在登录页还一直打接口，只会次次 401。
  if (statusTimer) {
    clearInterval(statusTimer);
    statusTimer = null;
  }
  const box = document.getElementById("login-msg");
  if (box && msg) box.textContent = msg;
  const pass = document.getElementById("login-pass");
  // 只聚焦，不清空：轮询撞上 401 时清掉用户正在输的字就没法用了。
  if (pass) pass.focus();
}

function hideLogin() {
  loginRequired = false;
  document.body.classList.remove("locked");
}

// doLogin 提交用户名与口令。
async function doLogin() {
  const user = (document.getElementById("login-user").value || "").trim() || "admin";
  const pass = document.getElementById("login-pass").value || "";
  const box = document.getElementById("login-msg");
  if (box) box.textContent = "";
  try {
    await api("/api/login", {method: "POST", body: JSON.stringify({user: user, password: pass})});
    hideLogin();
    await refreshAuth();
    startApp();
  } catch (e) {
    if (box) box.textContent = e.message;
  }
}

// doLogout 退出登录，回到登录页。
async function doLogout() {
  try {
    await api("/api/logout", {method: "POST"});
  } catch (e) { /* 就算接口失败，界面也得切回去 */ }
  showLogin("已退出登录");
}

// refreshAuth 拉一次鉴权状态并同步界面上的相关提示。
async function refreshAuth() {
  let a = authState;
  try {
    a = await api("/api/auth");
  } catch (e) {
    return authState;
  }
  authState = a;
  syncAuthUI();
  if (a.enabled && !a.authenticated) {
    showLogin("请重新登录");
  }
  return a;
}

// syncAuthUI 把鉴权状态反映到基础设置页。
function syncAuthUI() {
  const el = document.getElementById("set-auth-state");
  if (el) {
    el.textContent = authState.enabled
      ? ("已启用访问密码（用户名 " + (authState.user || "admin") + "）")
      : "未启用访问密码：同网段任何设备都能直接打开面板。";
  }
  const out = document.getElementById("btn-logout");
  if (out) out.hidden = !authState.enabled;
  const clr = document.getElementById("btn-clear-pass");
  if (clr) clr.hidden = !authState.enabled;
}

// clearPanelPassword 清除已设置的访问密码，回到免登录状态。
async function clearPanelPassword() {
  if (!confirm("确定清除面板访问密码？清除后同网段任何设备都能直接打开面板。")) return;
  try {
    const s = await api("/api/settings");
    s.clearPanelPassword = true;
    await api("/api/settings", {method: "POST", body: JSON.stringify(s)});
    toast("已清除访问密码", "ok");
    document.getElementById("set-passwd").value = "";
    await refreshAuth();
  } catch (e) { toast("清除失败: " + e.message, "err"); }
}

// ---- 导航与状态栏 ----
let lastStatus = null;
let dashName = "";
let gwEnabled = false;
// 日志：解析后的条目、拉回来的总行数、按级别筛选后显示的行数。
let logCache = [];
let logTotal = 0;
let logShown = 0;

function currentPanel() {
  const on = document.querySelector(".nav-item.on");
  return on ? on.dataset.p : "ov";
}

function setPill(id, label, value, kind) {
  const el = document.getElementById(id);
  if (!el) return;
  el.className = "pill" + (kind ? " " + kind : "");
  el.innerHTML = esc(label) + " <b>" + esc(value) + "</b>";
}

// 底部状态栏跟着当前页面走，像原生应用那样在列表下方显示条目数。
function updateStatusBar(key) {
  const left = document.getElementById("sb-left");
  const right = document.getElementById("sb-right");
  const s = lastStatus || {};
  const enabled = subsCache.filter(x => x.enabled).length;
  const texts = {
    ov: "共 6 项状态",
    sub: "共 " + subsCache.length + " 个订阅（启用 " + enabled + "）",
    dash: dashName ? "当前面板：" + dashName : "第三方面板",
    gw: gwEnabled ? "本机出站流量：已接管" : "本机出站流量：未启用",
    set: "端口与模式修改后需保存生效",
    log: logTotal
      ? ("共 " + logTotal + " 行日志" + (logShown !== logTotal ? "（显示 " + logShown + " 行）" : ""))
      : "暂无日志",
  };
  if (left) left.textContent = texts[key] || "";
  if (right) {
    right.textContent = s.running
      ? "内核 " + (s.version || "") + " 运行中 · 代理端口 " + s.mixedPort
      : "内核已停止";
  }
}

document.querySelectorAll(".nav-item").forEach(item => {
  item.addEventListener("click", () => {
    document.querySelectorAll(".nav-item").forEach(i => i.classList.remove("on"));
    document.querySelectorAll(".panel").forEach(p => p.classList.remove("on"));
    item.classList.add("on");
    const panel = document.getElementById("p-" + item.dataset.p);
    if (panel) panel.classList.add("on");
    if (item.dataset.p === "dash") loadDashboard();
    if (item.dataset.p === "log") loadLogs();
    updateStatusBar(item.dataset.p);
    if (sbMain) sbMain.sync();
  });
});

async function loadStatus() {
  try {
    const s = await api("/api/status");
    const cards = [
      ["内核状态", s.running ? "运行中" : "已停止", s.running ? "ok" : "bad"],
      ["运行时长", s.uptime || "-", ""],
      ["内核版本", s.version || "-", ""],
      ["代理端口", s.mixedPort, ""],
      ["订阅", s.enabledCount + "/" + s.subscriptionCount + " 启用", ""],
      ["本机走代理", s.gatewayActive ? "已接管" : (s.gatewayEnabled ? "已开启(未生效)" : "未启用"), s.gatewayActive ? "ok" : (s.gatewayEnabled ? "warn" : "")],
    ];
    document.getElementById("cards").innerHTML = cards.map(c =>
      `<div class="stat"><div class="k">${c[0]}</div><div class="v ${c[2]}">${c[1]}</div></div>`).join("");

    setPill("tb-kernel", "内核", s.running ? "运行中" : "已停止", s.running ? "ok" : "bad");
    setPill("tb-port", "端口", s.mixedPort);
    setPill("tb-sub", "订阅", s.enabledCount + "/" + s.subscriptionCount);

    const sk = document.getElementById("side-kernel");
    sk.textContent = s.running ? "运行中" : "已停止";
    sk.className = s.running ? "ok" : "bad";
    document.getElementById("side-port").textContent = s.mixedPort;
    document.getElementById("side-sub").textContent = s.enabledCount + "/" + s.subscriptionCount;

    lastStatus = s;
    updateStatusBar(currentPanel());
    if (sbMain) sbMain.sync();
  } catch (e) {
    toast("读取状态失败: " + e.message, "err");
  }
}

// ---- 订阅 ----
let subsCache = [];

async function loadSubs() {
  try {
    subsCache = await api("/api/subscriptions");
    renderSubs();
  } catch (e) { toast("读取订阅失败: " + e.message, "err"); }
}

// 搜索框按名称与链接过滤，匹配不上时列表和状态栏同步更新。
function filterSubs() {
  renderSubs();
}

function filteredSubs() {
  const q = (document.getElementById("sub-search").value || "").trim().toLowerCase();
  if (!q) return subsCache;
  return subsCache.filter(s =>
    String(s.name || "").toLowerCase().indexOf(q) >= 0 ||
    String(s.url || "").toLowerCase().indexOf(q) >= 0);
}

function renderSubs() {
  const list = filteredSubs();
  const tb = document.querySelector("#sub-table tbody");
  const gd = document.getElementById("sub-grid");
  if (!list.length) {
    tb.innerHTML = `<tr><td colspan="5"><div class="empty">${subsCache.length ? "没有匹配的订阅" : "还没有订阅，先在上面添加一个链接"}</div></td></tr>`;
    gd.innerHTML = "";
  } else {
    tb.innerHTML = list.map(subRow).join("");
    gd.innerHTML = list.map(subCard).join("");
  }
  updateStatusBar(currentPanel());
  if (sbSub) sbSub.sync();
  if (sbMain) sbMain.sync();
}

function subOps(s) {
  return `<button class="btn sm" onclick="toggleSub('${s.id}',${!s.enabled})">${s.enabled ? "停用" : "启用"}</button>` +
    `<button class="btn sm" onclick="refreshSub('${s.id}')">更新</button>` +
    `<button class="btn sm danger" onclick="delSub('${s.id}')">删除</button>`;
}

function subRow(s) {
  const st = s.lastError
    ? `<span class="tag err">出错</span>`
    : (s.enabled ? `<span class="tag on">启用</span>` : `<span class="tag off">停用</span>`);
  // 错误信息单独占一整行，而不是塞在「状态」格里：
  // 那格只有几十像素宽，长 URL 会被逐字断行，窄窗口里更是一片竖排字符。
  // 单独一行才有整行的宽度可读。
  const err = s.lastError
    ? `<tr class="err-row"><td colspan="5"><div class="err-line" title="${esc(s.lastError)}">${esc(s.lastError)}</div></td></tr>`
    : "";
  // 名字与链接都可能被截断（CSS 里是单行省略），
  // 完整内容放进 title，鼠标悬停还能看到。
  // data-label 是给窄窗口用的：表格改成卡片式堆叠后，
  // 每一格左侧会显示这个标签，不然窄屏里就分不清哪格是什么了。
  return `<tr>` +
    `<td><div class="row-main"><span class="row-ic"><svg class="ic"><use href="#ic-sub"/></svg></span>` +
    `<span class="row-txt"><span class="nm" title="${esc(s.name)}">${esc(s.name)}</span>` +
    `<span class="sub" title="${esc(s.url)}">${esc(s.url)}</span></span></div></td>` +
    `<td data-label="节点数">${s.nodeCount || 0}</td>` +
    `<td data-label="状态">${st}</td>` +
    `<td data-label="更新时间" style="color:var(--muted);white-space:nowrap">${s.updatedAt ? s.updatedAt.replace("T", " ").slice(0, 19) : "-"}</td>` +
    `<td data-label="操作"><div class="ops">${subOps(s)}</div></td></tr>` + err;
}

function subCard(s) {
  const st = s.lastError
    ? `<span class="tag err">出错</span>`
    : (s.enabled ? `<span class="tag on">启用</span>` : `<span class="tag off">停用</span>`);
  return `<div class="gcard">` +
    `<div class="row-main"><span class="row-ic"><svg class="ic"><use href="#ic-folder"/></svg></span>` +
    `<span class="row-txt"><span class="nm" title="${esc(s.name)}">${esc(s.name)}</span>` +
    `<span class="sub">${s.nodeCount || 0} 个节点</span></span></div>` +
    `<div class="long" title="${esc(s.url)}">${esc(s.url)}</div>` +
    `<div style="margin-top:8px">${st}</div>` +
    `<div class="ops">${subOps(s)}</div></div>`;
}

// 列表 / 卡片两种视图，和原生文件管理器一样可以随时切换。
function setSubView(v) {
  document.getElementById("p-sub").classList.toggle("view-grid", v === "grid");
  document.getElementById("v-list").classList.toggle("on", v === "list");
  document.getElementById("v-grid").classList.toggle("on", v === "grid");
  if (sbSub) sbSub.sync();
  if (sbMain) sbMain.sync();
}

async function saveSub() {
  const name = document.getElementById("sub-name").value.trim();
  const url = document.getElementById("sub-url").value.trim();
  if (!url) { toast("请填写订阅链接", "err"); return; }
  try {
    const r = await api("/api/subscriptions/update", {method: "POST", body: JSON.stringify({name: name, url: url})});
    toast("已添加，共 " + (r.nodeCount || 0) + " 个节点", "ok");
    document.getElementById("sub-name").value = "";
    document.getElementById("sub-url").value = "";
    await loadSubs(); await loadStatus();
  } catch (e) { toast("添加失败: " + e.message, "err"); }
}

async function refreshSub(id) {
  try {
    const r = await api("/api/subscriptions/update", {method: "POST", body: JSON.stringify({id: id})});
    toast("已更新，共 " + (r.nodeCount || 0) + " 个节点", "ok");
    await loadSubs(); await loadStatus();
  } catch (e) { toast("更新失败: " + e.message, "err"); await loadSubs(); }
}

async function toggleSub(id, enabled) {
  try {
    await api("/api/subscriptions/update", {method: "POST", body: JSON.stringify({id: id, enabled: enabled})});
    await loadSubs(); await loadStatus();
  } catch (e) { toast("操作失败: " + e.message, "err"); }
}

async function delSub(id) {
  if (!confirm("确定删除这个订阅？")) return;
  try {
    await api("/api/subscriptions/delete", {method: "POST", body: JSON.stringify({id: id})});
    toast("已删除", "ok");
    await loadSubs(); await loadStatus();
  } catch (e) { toast("删除失败: " + e.message, "err"); }
}

// ---- 控制面板 ----
// 这一页只做入口，不再内嵌预览。
//
// 第三方面板本身是个完整的单页应用，嵌进来会占掉大半屏；
// 而这个面板要跑在 fnOS 桌面的小窗口里，内嵌预览既看不清也没法交互。
// 改成「下拉选择 + 打开按钮 + 信息卡」，需要时新开标签页用。
//
// 面板文件由本应用从 GitHub 下载到本机，
// 内核通过 external-ui 把它托管在控制端口的 /ui/ 下，
// 与内核同源，不存在跨域和混合内容问题。
let dashURL = "";
// dashIPv6 是本机的全球 IPv6 入口，由后端按网卡地址算出来。
let dashIPv6 = [];

async function loadDashboard() {
  const box = document.getElementById("dash-hint");
  const sel = document.getElementById("dash-select");
  try {
    const d = await api("/api/dashboard");
    // 下拉框只在首次加载时填一次，避免每次切回来都重置用户选择。
    if (!sel.dataset.ready) {
      sel.innerHTML = (d.options || []).map(o =>
        `<option value="${esc(o.id)}">${esc(o.name)}</option>`).join("") +
        `<option value="">不使用（仅本面板）</option>`;
      sel.value = d.current || "";
      sel.dataset.ready = "1";
    }
    dashURL = d.url || "";
    dashName = d.name || "";
    dashIPv6 = Array.isArray(d.ipv6) ? d.ipv6 : [];
    renderDashInfo(d);
    syncIPv6Button();
    updateStatusBar(currentPanel());
    if (!dashURL) {
      box.textContent = "当前未启用第三方面板，节点与策略请用订阅管理和基础设置里的内核操作。";
      return;
    }
    if (d.status === "error") {
      box.textContent = "第三方面板下载失败：" + (d.error || "未知错误") +
        "。面板由本应用从 GitHub 直连下载，网络不通时稍后重试，或换一个面板试试。";
      return;
    }
    if (d.status === "downloading") {
      box.textContent = "正在下载第三方面板，下载完成后点「打开控制面板」即可使用……";
      return;
    }
    box.textContent = "面板由内核托管在本机 " + (d.controllerPort || "") +
      " 端口的 /ui/ 下，地址已带上当前控制端口与密钥，无需手工填写。";
  } catch (e) {
    box.textContent = "读取面板信息失败: " + e.message;
  }
}

// renderDashInfo 把面板状态整理成一张信息卡：名称、状态、控制端口、访问地址。
// 地址带链接，点一下就能在新标签页打开。
function renderDashInfo(d) {
  const info = document.getElementById("dash-info");
  if (!info) return;
  const statusText = {
    ok: "已就绪",
    downloading: "下载中",
    error: "出错",
  }[d.status] || (d.status || "未启用");
  const url = safeURL(d.url);
  const v6 = Array.isArray(d.ipv6) ? d.ipv6 : [];
  const rows = [
    ["面板名称", d.name || "未选择"],
    ["状态", statusText],
    ["控制端口", d.controllerPort || "-"],
    ["访问地址", url ? `<a href="${esc(url)}" target="_blank" rel="noreferrer">${esc(url)}</a>` : "-"],
  ];
  // IPv6 入口：本机的全球 IPv6 列出来，点一下就用 IPv6 打开面板。
  if (v6.length) {
    rows.push(["IPv6 地址", v6.map(x => {
      const u = safeURL(x.url);
      if (!u) return "";
      return `<div><a href="${esc(u)}" target="_blank" rel="noreferrer">${esc(x.addr)}</a></div>`;
    }).join("")]);
  }
  if (d.error) rows.push(["错误信息", `<span class="bad">${esc(d.error)}</span>`]);
  info.innerHTML = rows.map(r =>
    `<div class="k">${esc(r[0])}</div><div class="v">${r[1]}</div>`).join("");
}

// safeURL 只放行 http/https，别的东西不进 href。
function safeURL(u) {
  const s = String(u || "");
  return /^https?:\/\//i.test(s) ? s : "";
}

async function saveDashboard() {
  const sel = document.getElementById("dash-select");
  const id = sel.value;
  try {
    const s = await api("/api/settings");
    s.dashboard = id;
    await api("/api/settings", {method: "POST", body: JSON.stringify(s)});
    toast("面板已切换，正在准备面板文件…", "ok");
    dashURL = "";
    await waitDashboard(id);
  } catch (e) { toast("切换失败: " + e.message, "err"); }
}

// waitDashboard 轮询面板下载状态，下载完成后刷新信息卡。
async function waitDashboard(id) {
  for (let i = 0; i < 150; i++) {
    await new Promise(r => setTimeout(r, 1000));
    let d = null;
    try { d = await api("/api/dashboard"); } catch (e) { continue; }
    // 选了「不使用面板」，没有下载要等。
    if (!id) { await loadDashboard(); return; }
    if (d.status === "error") {
      await loadDashboard();
      toast("面板下载失败: " + (d.error || "未知错误"), "err");
      return;
    }
    if (d.status === "ok") { await loadDashboard(); return; }
  }
  await loadDashboard();
  toast("面板还在下载，请稍后刷新页面", "ok");
}

function openDashExternal() {
  if (!dashURL) { toast("当前没有启用第三方面板", "err"); return; }
  window.open(dashURL, "_blank", "noopener");
}

// syncIPv6Button 按本机有没有全球 IPv6 决定「用 IPv6 打开」按钮的显隐。
function syncIPv6Button() {
  const btn = document.getElementById("dash-ipv6-btn");
  if (btn) btn.hidden = dashIPv6.length === 0;
}

// openDashIPv6 用本机的 IPv6 地址打开第三方面板。
function openDashIPv6() {
  if (!dashIPv6.length) { toast("本机没有可用的全球 IPv6 地址", "err"); return; }
  const u = safeURL(dashIPv6[0].url);
  if (!u) { toast("IPv6 地址无效", "err"); return; }
  window.open(u, "_blank", "noopener");
}

async function coreAction(action) {
  try {
    await api("/api/core/" + action, {method: "POST"});
    toast("操作完成", "ok");
    await loadStatus();
  } catch (e) { toast("操作失败: " + e.message, "err"); }
}

// ---- 本机走代理 ----
async function loadGateway() {
  try {
    const s = await api("/api/settings");
    gwEnabled = !!(s.gateway && s.gateway.enabled);
    syncGatewaySwitch();
    updateStatusBar(currentPanel());
  } catch (e) { toast("读取设置失败: " + e.message, "err"); }
}

// 两个页面各有一个开关，共用同一个状态，改哪个都同步到另一个。
function syncGatewaySwitch() {
  const a = document.getElementById("gw-enabled");
  const b = document.getElementById("gw-enabled2");
  if (a) a.checked = gwEnabled;
  if (b) b.checked = gwEnabled;
  const la = document.getElementById("gw-lbl");
  const lb = document.getElementById("gw-lbl2");
  if (la) la.textContent = gwEnabled ? "已开启" : "已关闭";
  if (lb) lb.textContent = gwEnabled ? "已开启" : "已关闭";
}

function gwToggle(on) {
  gwEnabled = !!on;
  syncGatewaySwitch();
  saveGateway();
}

async function saveGateway() {
  try {
    const s = await api("/api/settings");
    s.gateway = s.gateway || {};
    s.gateway.enabled = gwEnabled;
    await api("/api/settings", {method: "POST", body: JSON.stringify(s)});
    await api("/api/gateway/toggle", {method: "POST", body: JSON.stringify({enabled: gwEnabled})});
    toast(gwEnabled ? "已启用：NAS 本机出站流量走代理" : "已关闭：接管规则已撤销", "ok");
    syncGatewaySwitch();
    updateStatusBar(currentPanel());
    await loadStatus();
  } catch (e) { toast("应用失败: " + e.message, "err"); }
}

// ---- 基础设置 ----
async function loadSettings() {
  try {
    const s = await api("/api/settings");
    document.getElementById("set-mixed").value = s.mixedPort;
    document.getElementById("set-controller").value = s.controllerPort;
    document.getElementById("set-secret").value = s.controllerSecret || "";
    document.getElementById("set-mode").value = s.mode;
    document.getElementById("set-rules").value = s.rulesMode;
    document.getElementById("set-log").value = s.logLevel;
    document.getElementById("set-lan").checked = !!s.allowLan;
    // 没这个字段的老配置按开启算，与服务端保持一致。
    document.getElementById("set-ipv6").checked = s.ipv6 !== false;
    // 口令不回传，输入框一律留空：填了才会改成新口令。
    document.getElementById("set-user").value = s.panelUser || "admin";
    document.getElementById("set-passwd").value = "";
  } catch (e) { toast("读取设置失败: " + e.message, "err"); }
}

async function saveSettings() {
  try {
    const s = await api("/api/settings");
    s.mixedPort = parseInt(document.getElementById("set-mixed").value, 10) || 7890;
    s.controllerPort = parseInt(document.getElementById("set-controller").value, 10) || 9090;
    s.controllerSecret = document.getElementById("set-secret").value;
    s.mode = document.getElementById("set-mode").value;
    s.rulesMode = document.getElementById("set-rules").value;
    s.logLevel = document.getElementById("set-log").value;
    s.allowLan = document.getElementById("set-lan").checked;
    s.ipv6 = document.getElementById("set-ipv6").checked;
    s.panelUser = document.getElementById("set-user").value.trim() || "admin";
    // 口令只在真的填了新的才带上去：留空表示不改。
    const pw = document.getElementById("set-passwd").value;
    if (pw) s.panelPassword = pw;
    await api("/api/settings", {method: "POST", body: JSON.stringify(s)});
    document.getElementById("set-passwd").value = "";
    toast("设置已保存", "ok");
    // 改过口令的话旧会话就失效了，这里顺手把界面切回登录页。
    await refreshAuth();
    await loadStatus();
  } catch (e) { toast("保存失败: " + e.message, "err"); }
}

// ---- 日志 ----
// 内核日志形如：
//   time="2026-11-25T12:00:00.123456789+08:00" level=info msg="..."
// 这里拆成「时间 / 级别 / 内容」三段，表格里分列显示并按级别染色。
// 级别归一：内核用的是 warning，界面上的筛选和配色都按 warn 走。
function normLevel(lv) {
  const v = String(lv || "").toLowerCase();
  if (v === "error" || v === "fatal" || v === "panic") return "error";
  if (v === "warning" || v === "warn") return "warn";
  if (v === "info") return "info";
  if (v === "debug" || v === "trace") return "debug";
  return "other";
}

const LV_TEXT = {error: "错误", warn: "警告", info: "信息", debug: "调试", other: "其它"};

// parseLogLine 把一行内核日志拆成时间、级别、内容三段。
// 不是内核格式的行（比如启动时的杂音）整条当作内容，级别记为「其它」。
function parseLogLine(raw) {
  const text = String(raw == null ? "" : raw);
  let rest = text;
  let time = "";
  let level = "other";
  const tm = /\btime="([^"]*)"/.exec(rest);
  if (tm) { time = tm[1]; rest = rest.replace(tm[0], " "); }
  const lm = /\blevel=([A-Za-z]+)/.exec(rest);
  if (lm) { level = normLevel(lm[1]); rest = rest.replace(lm[0], " "); }
  rest = rest.trim();
  let msg = rest;
  const mm = /^msg="([\s\S]*)"$/.exec(rest);
  if (mm) msg = mm[1];
  // Go 的 %q 会把引号转义成 \"，还原出来读着才正常。
  msg = msg.replace(/\\"/g, "\"");
  return {time: time, level: level, msg: msg};
}

// fmtTime 把完整时间戳压成 时:分:秒.毫秒，完整值放在 title 里。
function fmtTime(t) {
  const s = String(t || "");
  const m = /T(\d{2}:\d{2}:\d{2})(?:\.(\d+))?/.exec(s);
  if (!m) return s;
  return m[1] + (m[2] ? "." + m[2].slice(0, 3) : "");
}

function logRow(e) {
  const lv = e.level || "other";
  return `<tr class="lv-${lv}-row">` +
    `<td class="lv-col"><span class="lv lv-${lv}">${esc(LV_TEXT[lv] || lv)}</span></td>` +
    `<td class="ts-col"><span class="ts" title="${esc(e.time)}">${esc(fmtTime(e.time))}</span></td>` +
    `<td class="msg-cell"><span class="msg" title="${esc(e.msg)}">${esc(e.msg)}</span></td></tr>`;
}

// renderLogs 按当前级别筛选渲染日志表格。
// 筛选只是前端过滤，不用重新向内核要日志。
function renderLogs() {
  const body = document.getElementById("log-body");
  if (!body) return;
  const box = document.getElementById("log-scroll");
  // 换级别只是换一批行，滚动位置留着，
  // 不然每切一次级别视口都跳回顶部，一眼就找不到刚才在看的那行。
  const keep = box ? box.scrollTop : 0;
  const sel = document.getElementById("log-level");
  const want = sel ? sel.value : "";
  const list = want ? logCache.filter(x => x.level === want) : logCache;
  logShown = list.length;
  if (!list.length) {
    body.innerHTML = `<tr><td colspan="3"><div class="empty">${logTotal ? "当前级别没有日志" : "（暂无日志）"}</div></td></tr>`;
  } else {
    body.innerHTML = list.map(logRow).join("");
  }
  if (box) box.scrollTop = keep;
  updateStatusBar(currentPanel());
  if (sbLog) sbLog.sync();
}

async function loadLogs() {
  const box = document.getElementById("log-scroll");
  const body = document.getElementById("log-body");
  const linesSel = document.getElementById("log-lines");
  const want = parseInt(linesSel ? linesSel.value : "300", 10) || 300;
  try {
    const lines = await api("/api/logs?lines=" + want);
    const arr = Array.isArray(lines) ? lines : [];
    logTotal = arr.length;
    logCache = arr.map(parseLogLine);
    renderLogs();
    // 新日志在末尾，滚到底部看最新的。
    if (box) box.scrollTop = box.scrollHeight;
    if (sbLog) sbLog.sync();
  } catch (e) {
    logTotal = 0;
    logCache = [];
    if (body) {
      body.innerHTML = `<tr><td colspan="3"><div class="empty bad">读取日志失败: ${esc(e.message)}</div></td></tr>`;
    }
  }
}

// ---- 自绘滚动条 ----
// 挂在滚动容器上，轨道画在它的父层里，这样把手不会跟着内容一起滚走。
// 订阅列表与日志各有自己的滚动区，轨道分别画在各自的 .sbox 里。
const sbMain = attachScrollbar(document.getElementById("main-scroll"));
const sbSub = attachScrollbar(document.getElementById("sub-scroll"));
const sbLog = attachScrollbar(document.getElementById("log-scroll"));

// startApp 进主界面后要跑的加载与轮询。
// 轮询只在真正进了主界面时才开始 —— 没登录就去轮询，只会被后端一次次挡回来。
function startApp() {
  loadStatus();
  loadSubs();
  loadSettings();
  loadGateway();
  loadDashboard();
  if (!statusTimer) statusTimer = setInterval(loadStatus, 5000);
}

// boot 先问一句鉴权状态，再决定是进主界面还是停在登录页。
async function boot() {
  let a = null;
  try {
    a = await api("/api/auth");
  } catch (e) {
    showLogin("连不上面板服务：" + e.message);
    return;
  }
  authState = a;
  syncAuthUI();
  if (a.enabled && !a.authenticated) {
    showLogin();
    return;
  }
  hideLogin();
  startApp();
}

// 首次加载
boot();
