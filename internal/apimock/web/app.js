const root = document.querySelector('#app');
let csrf = '';
let accounts = [];
let records = [];
let strategy = 'fill_first';
let view = 'accounts';
let busy = false;
let toastTimer = 0;
let toastHideTimer = 0;

async function api(path, init = {}) {
  const headers = new Headers(init.headers || {});
  if (init.body) headers.set('Content-Type', 'application/json');
  if (init.method && init.method !== 'GET') headers.set('X-CSRF-Token', csrf);
  const response = await fetch(path, { ...init, headers, credentials: 'same-origin' });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw Error(body.error?.message || '请求失败');
  return body;
}

const esc = (value) => String(value ?? '').replace(/[&<>"']/g, (ch) => ({
  '&': '&amp;',
  '<': '&lt;',
  '>': '&gt;',
  '"': '&quot;',
  "'": '&#39;',
}[ch]));

function isZeroTime(value) {
  if (!value) return true;
  const t = Date.parse(value);
  return Number.isNaN(t) || t <= 0;
}

function formatTime(value) {
  if (isZeroTime(value)) return '未使用';
  return new Date(value).toLocaleString();
}

function ensureToastHost() {
  let host = document.querySelector('#toast-host');
  if (host) return host;
  host = document.createElement('div');
  host.id = 'toast-host';
  host.className = 'toastHost';
  host.setAttribute('aria-live', 'polite');
  host.setAttribute('aria-atomic', 'true');
  document.body.append(host);
  return host;
}

function hideToast() {
  const host = document.querySelector('#toast-host');
  if (!host) return;
  host.querySelectorAll('.toast').forEach((node) => node.classList.add('toast-out'));
  window.clearTimeout(toastHideTimer);
  toastHideTimer = window.setTimeout(() => {
    host.replaceChildren();
  }, 180);
}

function showToast(message, type = 'info') {
  const text = String(message || '').trim();
  if (!text) return;
  const host = ensureToastHost();
  window.clearTimeout(toastTimer);
  window.clearTimeout(toastHideTimer);
  host.replaceChildren();
  const node = document.createElement('div');
  node.className = `toast toast-${type === 'error' ? 'error' : 'info'}`;
  node.textContent = text;
  host.append(node);
  // force layout so the enter transition runs
  void node.offsetWidth;
  node.classList.add('toast-in');
  toastTimer = window.setTimeout(hideToast, 3000);
}

async function load() {
  const [accountPayload, recordPayload] = await Promise.all([
    api('/api/accounts'),
    api('/api/records'),
  ]);
  accounts = Array.isArray(accountPayload.data) ? accountPayload.data : [];
  records = Array.isArray(recordPayload.data) ? recordPayload.data : [];
  strategy = accountPayload.strategy === 'round_robin' ? 'round_robin' : 'fill_first';
}

function login() {
  root.innerHTML = `
    <section class="login">
      <div class="loginBox">
        <div class="mark">B</div>
        <h1>Buddy-API-mock</h1>
        <p>WorkBuddy 兼容中转控制台</p>
        <form class="form" id="login">
          <label>管理员密码<input type="password" name="password" required autofocus></label>
          <button class="primary" type="submit">进入控制台</button>
        </form>
      </div>
    </section>`;
  document.querySelector('#login').onsubmit = async (event) => {
    event.preventDefault();
    try {
      const payload = await api('/api/login', {
        method: 'POST',
        body: JSON.stringify(Object.fromEntries(new FormData(event.target))),
      });
      csrf = payload.csrf;
      await load();
      render();
    } catch (error) {
      showToast(error.message, 'error');
    }
  };
}

function accountPage() {
  const rows = accounts.map((account) => `
    <tr>
      <td>
        <strong>${esc(account.label)}</strong>
        <small>${esc(account.endpoint)}</small>
      </td>
      <td>${esc(account.model)}</td>
      <td><span class="state">${account.enabled ? '可用' : '已停用'}</span></td>
      <td>${formatTime(account.lastUsedAt)}</td>
      <td><button class="icon" type="button" title="移除账户" data-delete="${esc(account.id)}">×</button></td>
    </tr>`).join('') || `<tr><td colspan="5" class="empty">尚未添加账户</td></tr>`;

  return `
    <section class="panel">
      <div class="toolbar">
        <div class="strategyWrap">
          <div class="strategyMeta">
            <strong>请求方式</strong>
          </div>
          <div class="strategy" role="group" aria-label="请求方式">
            <button type="button" data-strategy="fill_first" class="${strategy === 'fill_first' ? 'active' : ''}">填充优先</button>
            <button type="button" data-strategy="round_robin" class="${strategy === 'round_robin' ? 'active' : ''}">轮询优先</button>
          </div>
        </div>
        <button class="primary add" id="add" type="button">添加账户</button>
      </div>
      <div class="stats">
        <span>账户总数<b>${accounts.length}</b></span>
        <span>可用账户<b>${accounts.filter((item) => item.enabled).length}</b></span>
        <span>当前策略<b>${strategy === 'round_robin' ? '轮询优先' : '填充优先'}</b></span>
      </div>
      <div class="table">
        <table>
          <thead>
            <tr><th>账户</th><th>模型</th><th>状态</th><th>最近使用</th><th></th></tr>
          </thead>
          <tbody>${rows}</tbody>
        </table>
      </div>
    </section>`;
}

function recordPage() {
  const rows = records.map((record) => `
    <tr>
      <td>${formatTime(record.At)}</td>
      <td>${esc(record.AccountLabel)}</td>
      <td>${esc(record.Model)}</td>
      <td><span class="state ${record.Status === 'succeeded' ? '' : 'failed'}">${esc(record.HTTPStatus || '网络错误')} · ${esc(record.Status)}</span></td>
      <td>${record.Stream ? '流式' : 'JSON'} · ${esc(record.DurationMS)} ms</td>
    </tr>`).join('') || `<tr><td colspan="5" class="empty">尚无请求记录</td></tr>`;

  return `
    <section class="panel table">
      <table>
        <thead>
          <tr><th>时间</th><th>账户</th><th>模型</th><th>结果</th><th>传输</th></tr>
        </thead>
        <tbody>${rows}</tbody>
      </table>
    </section>`;
}

function bindShell() {
  document.querySelectorAll('[data-view]').forEach((button) => {
    button.onclick = async () => {
      if (busy) return;
      const next = button.dataset.view;
      if (next === view && next !== 'records') return;
      busy = true;
      try {
        view = next;
        await load();
        render();
      } catch (error) {
        showToast(error.message, 'error');
        render();
      } finally {
        busy = false;
      }
    };
  });

  document.querySelector('#diagnostics').onclick = diagnosticsModal;
  document.querySelector('#logout').onclick = async () => {
    if (busy) return;
    busy = true;
    try {
      await api('/api/logout', { method: 'POST' });
      csrf = '';
      accounts = [];
      records = [];
      login();
    } catch (error) {
      showToast(error.message, 'error');
    } finally {
      busy = false;
    }
  };

  if (view !== 'accounts') return;

  const addButton = document.querySelector('#add');
  if (addButton) addButton.onclick = modal;

  document.querySelectorAll('[data-strategy]').forEach((button) => {
    button.onclick = async () => {
      if (busy) return;
      const next = button.dataset.strategy;
      if (!next || next === strategy) return;
      busy = true;
      try {
        await api('/api/strategy', {
          method: 'POST',
          body: JSON.stringify({ strategy: next }),
        });
        await load();
        showToast(next === 'round_robin' ? '已切换为轮询优先' : '已切换为填充优先');
        render();
      } catch (error) {
        showToast(error.message, 'error');
        render();
      } finally {
        busy = false;
      }
    };
  });

  document.querySelectorAll('[data-delete]').forEach((button) => {
    button.onclick = async () => {
      if (busy) return;
      if (!confirm('移除此账户？')) return;
      busy = true;
      try {
        await api('/api/accounts/' + encodeURIComponent(button.dataset.delete), { method: 'DELETE' });
        await load();
        showToast('账户已移除');
        render();
      } catch (error) {
        showToast(error.message, 'error');
        render();
      } finally {
        busy = false;
      }
    };
  });
}

function render() {
  root.innerHTML = `
    <div class="shell">
      <aside class="side">
        <div class="brand"><i>B</i><span>Buddy-API-mock</span></div>
        <button class="nav ${view === 'accounts' ? 'active' : ''}" type="button" data-view="accounts">◫ <span>账户池</span></button>
        <button class="nav ${view === 'records' ? 'active' : ''}" type="button" data-view="records">☷ <span>请求记录</span></button>
        <div class="sideActions">
          <button class="sideAction" id="diagnostics" type="button">⌁ <span>诊断</span></button>
          <button class="sideAction" id="logout" type="button">↪ <span>退出</span></button>
        </div>
      </aside>
      <main class="main">
        <header class="top">
          <div>
            <h1>${view === 'accounts' ? '账户池' : '请求记录'}</h1>
            <p>${view === 'accounts' ? '管理上游账户与本地调度策略' : '仅保存元数据，不保存提示词、密钥或上游响应'}</p>
          </div>
        </header>
        ${view === 'accounts' ? accountPage() : recordPage()}
      </main>
    </div>`;
  bindShell();
}

function modal() {
  if (document.querySelector('.modalBack')) return;
  const wrap = document.createElement('div');
  wrap.className = 'modalBack';
  wrap.innerHTML = `
    <form class="modal form" id="account-form">
      <div class="modalHead">
        <h2>添加账户</h2>
        <button type="button" class="icon" id="close">×</button>
      </div>
      <label>名称<input name="label" placeholder="例如：WorkBuddy 主账户" required></label>
      <label>Endpoint<input name="endpoint" placeholder="https://api.example.com/v1" required></label>
      <label>API Key<input name="APIKey" type="password" autocomplete="off" required></label>
      <label>模型 ID<input name="model" value="gpt-5.6-sol" required></label>
      <label class="hint check"><input type="checkbox" name="enabled" checked> 添加后立即启用</label>
      <p class="hint">Endpoint 仅用于发送 POST /chat/completions。</p>
      <button class="primary" type="submit">保存账户</button>
    </form>`;
  document.body.append(wrap);

  const close = () => wrap.remove();
  wrap.querySelector('#close').onclick = close;
  wrap.addEventListener('click', (event) => {
    if (event.target === wrap) close();
  });

  wrap.querySelector('#account-form').onsubmit = async (event) => {
    event.preventDefault();
    if (busy) return;
    const form = event.target;
    const payload = {
      label: form.label.value.trim(),
      endpoint: form.endpoint.value.trim(),
      APIKey: form.APIKey.value,
      model: form.model.value.trim(),
      enabled: !!form.enabled.checked,
    };
    busy = true;
    try {
      await api('/api/accounts', {
        method: 'POST',
        body: JSON.stringify(payload),
      });
      await load();
      close();
      view = 'accounts';
      showToast(`已添加账户：${payload.label}`);
      render();
    } catch (error) {
      showToast(error.message, 'error');
    } finally {
      busy = false;
    }
  };
}

async function diagnosticsModal() {
  if (document.querySelector('.modalBack')) return;
  try {
    const diagnostics = await api('/api/diagnostics');
    const wrap = document.createElement('div');
    wrap.className = 'modalBack';
    wrap.innerHTML = `
      <section class="modal diagnostics" role="dialog" aria-modal="true" aria-label="运行诊断">
        <div class="modalHead">
          <h2>运行诊断</h2>
          <button type="button" class="icon" id="close">×</button>
        </div>
        <div class="diagnosticSummary"><strong>服务已就绪</strong><span>账户池和本地运行状态可读取</span></div>
        <details open><summary>诊断 JSON</summary><pre>${esc(JSON.stringify(diagnostics, null, 2))}</pre></details>
      </section>`;
    document.body.append(wrap);
    const close = () => wrap.remove();
    wrap.querySelector('#close').onclick = close;
    wrap.addEventListener('click', (event) => {
      if (event.target === wrap) close();
    });
  } catch (error) {
    showToast(error.message, 'error');
  }
}

(async () => {
  try {
    const session = await api('/api/session');
    if (!session.authenticated) return login();
    csrf = session.csrf;
    await load();
    render();
  } catch {
    login();
  }
})();
