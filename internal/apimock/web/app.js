const root = document.querySelector('#app');
let csrf = '';
let accounts = [];
let records = [];
let strategy = 'fill_first';
let view = 'accounts';
let busy = false;
let toastTimer = 0;
let toastHideTimer = 0;

async function apiResponse(path, init = {}) {
  const headers = new Headers(init.headers || {});
  if (init.body) headers.set('Content-Type', 'application/json');
  if (init.method && init.method !== 'GET') headers.set('X-CSRF-Token', csrf);
  const response = await fetch(path, { ...init, headers, credentials: 'same-origin' });
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    throw Error(body.error?.message || '请求失败');
  }
  return response;
}

async function api(path, init = {}) {
  return (await apiResponse(path, init)).json();
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

function apiPage() {
  const endpoint = `${location.origin}/v1/chat/completions`;
  const enabled = accounts.filter((account) => account.enabled);
  const models = [...new Set(enabled.map((account) => account.model.trim()).filter(Boolean))].sort();
  const options = models.map((model) => `<option value="${esc(model)}">${esc(model)}</option>`).join('');
  const jsonCurl = `curl ${endpoint} \\\n  -H "Authorization: Bearer $API_MOCK_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"<model-id>","messages":[{"role":"user","content":"Reply with OK."}]}'`;
  const sseCurl = `curl -N ${endpoint} \\\n  -H "X-API-Key: $API_MOCK_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"<model-id>","stream":true,"messages":[{"role":"user","content":"Reply with OK."}]}'`;
  return `
    <section class="apiGuide">
      <div class="panel guideCopy">
        <div class="sectionTitle"><div><h2>快速开始</h2><small>OpenAI Chat Completions 兼容接口</small></div></div>
        <dl class="referenceList">
          <div><dt>Endpoint</dt><dd><code>${esc(endpoint)}</code><button class="icon copy" type="button" data-copy="endpoint" title="复制 Endpoint">⎘</button></dd></div>
          <div><dt>认证</dt><dd><code>Bearer 或 X-API-Key</code></dd></div>
          <div><dt>账户</dt><dd>${enabled.map((account) => `<span>${esc(account.label)} · ${esc(account.model)}</span>`).join('') || '尚无可用账户'}</dd></div>
        </dl>
        <div class="codeHead"><b>JSON curl</b><button class="secondary" type="button" data-copy="json">⎘ 复制</button></div><pre>${esc(jsonCurl)}</pre>
        <div class="codeHead"><b>SSE curl</b><button class="secondary" type="button" data-copy="sse">⎘ 复制</button></div><pre>${esc(sseCurl)}</pre>
      </div>
      <div class="panel tryPanel">
        <div class="sectionTitle"><div><h2>发送测试请求</h2><small>使用服务器已配置的 Relay Key</small></div></div>
        <form class="form testForm" id="api-test">
          <label>模型<select name="model" ${models.length ? '' : 'disabled'}>${options}</select></label>
          <label>输入<textarea name="message" rows="5">你好，请简要介绍你自己。</textarea></label>
          <div class="formActions"><label class="toggle"><input name="stream" type="checkbox"><span></span>流式响应</label><button class="primary" type="submit" ${models.length ? '' : 'disabled'}>▷ 发送</button></div>
        </form>
        <div class="response"><div><b>响应</b><small>正文不会写入请求记录</small></div><pre id="api-output">等待请求</pre></div>
      </div>
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

  if (view === 'api') {
    const endpoint = `${location.origin}/v1/chat/completions`;
    const jsonCurl = `curl ${endpoint} \\\n  -H "Authorization: Bearer $API_MOCK_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"<model-id>","messages":[{"role":"user","content":"Reply with OK."}]}'`;
    const sseCurl = `curl -N ${endpoint} \\\n  -H "X-API-Key: $API_MOCK_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"<model-id>","stream":true,"messages":[{"role":"user","content":"Reply with OK."}]}'`;
    const copyValues = { endpoint, json: jsonCurl, sse: sseCurl };
    document.querySelectorAll('[data-copy]').forEach((button) => {
      button.onclick = async () => {
        try {
          await navigator.clipboard.writeText(copyValues[button.dataset.copy]);
          showToast('已复制');
        } catch {
          showToast('复制失败', 'error');
        }
      };
    });
    const form = document.querySelector('#api-test');
    if (form) form.onsubmit = async (event) => {
      event.preventDefault();
      if (busy) return;
      const model = form.model.value.trim();
      if (!model) {
        showToast('请选择可用模型', 'error');
        return;
      }
      const output = document.querySelector('#api-output');
      const submit = form.querySelector('button[type="submit"]');
      const stream = form.stream.checked;
      busy = true;
      submit.disabled = true;
      submit.textContent = '请求中';
      output.textContent = '请求中…';
      try {
        const response = await apiResponse('/api/test', {
          method: 'POST',
          body: JSON.stringify({
            model,
            stream,
            messages: [{ role: 'user', content: form.message.value }],
          }),
        });
        if (stream) {
          output.textContent = await response.text();
        } else {
          output.textContent = JSON.stringify(await response.json(), null, 2);
        }
        await load();
      } catch (error) {
        output.textContent = error.message;
        showToast(error.message, 'error');
      } finally {
        busy = false;
        submit.disabled = false;
        submit.textContent = '▷ 发送';
      }
    };
    return;
  }

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
        <button class="nav ${view === 'api' ? 'active' : ''}" type="button" data-view="api">▷ <span>API</span></button>
        <div class="sideActions">
          <button class="sideAction" id="diagnostics" type="button" title="运行诊断">
            <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 7h-9"/><path d="M14 17H5"/><circle cx="17" cy="17" r="3"/><circle cx="7" cy="7" r="3"/></svg>
            <span>诊断</span>
          </button>
          <button class="sideAction" id="logout" type="button" title="退出登录">
            <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" x2="9" y1="12" y2="12"/></svg>
            <span>退出</span>
          </button>
        </div>
      </aside>
      <main class="main">
        <header class="top">
          <div>
            <h1>${view === 'accounts' ? '账户池' : view === 'records' ? '请求记录' : 'API'}</h1>
            <p>${view === 'accounts' ? '管理上游账户与本地调度策略' : view === 'records' ? '仅保存元数据，不保存提示词、密钥或上游响应' : '查看调用示例并发送测试请求'}</p>
          </div>
        </header>
        ${view === 'accounts' ? accountPage() : view === 'records' ? recordPage() : apiPage()}
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
      <section class="modal modalWide diagnostics" role="dialog" aria-modal="true" aria-label="运行诊断">
        <div class="modalHead">
          <h2>运行诊断</h2>
          <button type="button" class="icon modalClose" id="close" aria-label="关闭"><svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg></button>
        </div>
        <div class="diagnosticSummary ${diagnostics.ok ? 'ok' : ''}">
          <span class="diagnosticIcon">${diagnostics.ok
            ? '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg>'
            : '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><path d="M12 8v4"/><path d="M12 16h.01"/></svg>'}</span>
          <div><b>${diagnostics.ok ? '服务已就绪' : '仍有待处理项'}</b><small>${diagnostics.ok ? '账户池和本地转发服务均可用' : '检查账户池与部署环境变量'}</small></div>
        </div>
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
