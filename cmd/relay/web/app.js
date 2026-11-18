const $ = (s) => document.querySelector(s);
let applications = [], nodes = [];
async function api(path, options = {}) {
  const response = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...options });
  const data = await response.json().catch(() => ({}));
  if (response.status === 401) { showLogin(); throw new Error('未登录'); }
  if (!response.ok || data.code) throw new Error(data.msg || '请求失败');
  return data.data;
}
function showLogin() { $('#login').hidden = false; $('#console').hidden = true; }
function showConsole() { $('#login').hidden = true; $('#console').hidden = false; loadAll(); }
function esc(value) { return String(value ?? '').replace(/[&<>"']/g, (c) => ({ '&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;' }[c])); }
function localTime(value) { return value ? new Date(value).toLocaleString() : '-'; }
async function loadAll() {
  try {
    [applications, nodes] = await Promise.all([api('/api/applications'), api('/api/nodes')]);
    renderApplications(); renderNodes();
    const limits = await api('/api/settings/limits');
    $('#bandwidth').value = (+limits.default_bandwidth_bytes / 1048576).toFixed(1);
    $('#connections').value = limits.max_connections;
  } catch (error) { if (error.message !== '未登录') alert(error.message); }
}
function renderApplications() {
  $('#pendingCount').textContent = applications.filter((x) => x.status === 'pending').length;
  $('#applications').innerHTML = applications.map((x) => `<tr><td><b>${esc(x.device_name)}</b><small>${esc(x.hostname || '')}</small></td><td>${esc(x.platform || '-')}</td><td>${esc(x.version || '-')}</td><td>${localTime(x.created_at)}</td><td><span class="status ${x.status}">${x.status}</span></td><td>${x.status === 'pending' ? `<button data-act="approve" data-id="${x.id}">允许</button><button class="danger" data-act="reject" data-id="${x.id}">拒绝</button>` : '-'}</td></tr>`).join('') || '<tr><td colspan="6" class="empty">暂无申请</td></tr>';
}
function renderNodes() {
  $('#nodeCount').textContent = nodes.length;
  $('#onlineCount').textContent = nodes.filter((x) => x.status === 'online').length;
  $('#nodes').innerHTML = nodes.map((x) => `<tr><td><b>${esc(x.device_name)}</b><small>${esc(x.hostname || '')}</small></td><td><code>${esc(x.node_id)}</code></td><td><span class="status ${x.status}">${x.status}</span></td><td>${(+x.bandwidth_bytes / 1048576).toFixed(1)} MB/s</td><td>${x.max_connections}</td><td>${x.status !== 'revoked' ? `<button class="danger" data-revoke="${x.node_id}">撤销</button>` : '-'}</td></tr>`).join('') || '<tr><td colspan="6" class="empty">暂无节点</td></tr>';
}
$('#loginForm').onsubmit = async (event) => { event.preventDefault(); $('#loginError').textContent = ''; try { await api('/api/login', { method: 'POST', body: JSON.stringify({ username: $('#username').value, password: $('#password').value }) }); showConsole(); } catch (error) { $('#loginError').textContent = error.message; } };
$('#logout').onclick = async () => { await api('/api/logout', { method: 'POST' }); showLogin(); };
$('#refresh').onclick = loadAll; $('#refreshNodes').onclick = loadAll;
$('#applications').onclick = async (event) => { const button = event.target.closest('[data-act]'); if (!button) return; try { await api(`/api/applications/${button.dataset.id}/${button.dataset.act}`, { method: 'POST' }); loadAll(); } catch (error) { alert(error.message); } };
$('#nodes').onclick = async (event) => { const button = event.target.closest('[data-revoke]'); if (!button || !confirm('撤销这个节点？')) return; try { await api(`/api/nodes/${button.dataset.revoke}/revoke`, { method: 'POST' }); loadAll(); } catch (error) { alert(error.message); } };
$('#limitForm').onsubmit = async (event) => { event.preventDefault(); try { await api('/api/settings/limits', { method: 'PUT', body: JSON.stringify({ default_bandwidth_bytes: String(Math.round(+$('#bandwidth').value * 1048576)), max_connections: String($('#connections').value) }) }); $('#limitSaved').textContent = '已保存'; setTimeout(() => $('#limitSaved').textContent = '', 1500); } catch (error) { alert(error.message); } };
api('/api/me').then(showConsole).catch(() => showLogin());
