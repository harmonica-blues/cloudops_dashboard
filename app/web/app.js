const pages = {
  overview: ['概要', 'AWSインフラの全体概要', '▦'],
  waf: ['WAF', 'トラフィック・アクション・セキュリティイベント', '◈'],
  ec2: ['EC2', 'インスタンスのCPU・メモリ・状態', '▣'],
  rds: ['RDS', 'データベースの稼働状況とパフォーマンス', '◉'],
  queries: ['クエリ', 'Athenaオンデマンドクエリ', '⌕']
};
// バックエンドが返すステータス値（英語）を画面表示用の日本語に変換する
const statusLabels = { Healthy: '正常', Check: '要確認', Unknown: '不明', screenshot: '撮影用データ表示中', updated: 'AWSデータ更新済み', waiting: '収集待ち', error: 'エラー', QUEUED: '待機中', RUNNING: '実行中', SUCCEEDED: '成功', FAILED: '失敗', CANCELLED: 'キャンセル' };
// インスタンスごとの線の色（複数インスタンスを1つのグラフに重ねるとき用）。CSS 変数なのでライト/ダークの切り替えに追従する
const SERIES_COLORS = ['var(--s1)', 'var(--s2)', 'var(--s3)', 'var(--s4)', 'var(--s5)', 'var(--s6)'];
// WAF のアクションごとの色。バックエンドが返す色を Bento テーマの色に置き換える
const ACTION_COLORS = { 許可: 'var(--s2)', ブロック: 'var(--s1)', カウント: 'var(--s5)', チャレンジ: 'var(--s3)', CAPTCHA: 'var(--s4)' };
// 概要画面のタイル見出しに使う期間の名前
const PERIOD_LABELS = { 1: '24時間', 7: '7日間', 30: '30日間' };
const CHART = { width: 680, height: 230, left: 52, right: 14, top: 14, bottom: 30 };
const CHART_ENTRANCE_MS = 1100;
let chartEntranceStartedAt = -CHART_ENTRANCE_MS;
const AXIS_DATE = { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' };
// 画面を自動更新する間隔。バックエンドのキャッシュを読むだけなので AWS への追加呼び出しは発生しない
const AUTO_REFRESH_MS = 60 * 1000;
const NO_DATA = 'データなし';
// Athena クエリ実行中に状態を確認する間隔
const QUERY_POLL_MS = 2000;
// selectedQuery: プルダウンで選択中の保存済みクエリ、filledSql: プレースホルダーを置き換えた直後の SQL（未編集かどうかの判定用）
// privateQuery: 「結果を共有しない」のチェック（SQL と結果を実行したユーザーにだけ表示する）
const state = { page: 'overview', snapshot: null, queries: [], sql: '', search: '', loading: false, athena: null, savedQueries: [], selectedQuery: '', filledSql: '', resultId: '', polling: false, privateQuery: false };
// 「カスタム期間」を開いただけでは適用済みの期間を変えない。
let appliedPeriod = '1';
// 描画中のグラフの情報。ツールチップ表示に使い、render() のたびに作り直す
const chartRegistry = [];
const THEME_KEY = 'cloudops-theme';
const $ = id => document.getElementById(id);
const esc = value => String(value ?? '').replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char]));
const hasSeries = points => Array.isArray(points) && points.length > 0;
const statusLabel = value => statusLabels[value] || value;
const timeZone = () => state.snapshot?.timezone || 'UTC';
const formatDate = (value, options = { dateStyle: 'short', timeStyle: 'short' }) => new Intl.DateTimeFormat('ja-JP', { ...options, timeZone: timeZone() }).format(new Date(value));
const decimal = value => {
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) return NO_DATA;
  return new Intl.NumberFormat('en-US', { maximumFractionDigits: 3, useGrouping: false }).format(numeric);
};
const axisNumber = value => new Intl.NumberFormat('ja-JP', { notation: 'compact', maximumFractionDigits: 1 }).format(Number(value) || 0);
const number = value => new Intl.NumberFormat('ja-JP', { maximumFractionDigits: 3 }).format(Number(value) || 0);
// ログインの有効期限が切れたら（401）、ログイン画面に移動する
const api = async (path, options) => { const response = await fetch(path, options); if (response.status === 401) { window.location.href = '/login' + window.location.search; throw new Error('ログインしてください'); } const data = await response.json(); if (!response.ok) throw new Error(data.error || 'APIエラー'); return data; };
const badge = (label, tone = '') => `<span class="badge ${tone}">${esc(label)}</span>`;
const card = (title, body, extra = '', className = '') => `<article class="card ${className}"><div class="head"><h2>${title}</h2>${extra}</div>${body}</article>`;
const meter = value => `${decimal(value)}%<span class="meter"><i style="width:${Math.max(0, Math.min(100, Number(value) || 0))}%"></i></span>`;
// 前期間との比較。カスタム期間は直前の同じ長さの期間と比べる
const COMPARE_LABELS = { 1: '前日比', 7: '前週比', 30: '前月比' };
const previousOf = (group, id, key) => state.snapshot?.comparison?.[group]?.[id]?.[key];
const mean = values => values.length ? values.reduce((sum, value) => sum + value, 0) / values.length : NaN;
function delta(current, previous) {
  const comparison = state.snapshot?.comparison;
  if (!comparison || previous == null || !Number.isFinite(Number(current)) || !Number.isFinite(Number(previous))) return '';
  const label = COMPARE_LABELS[state.snapshot.period] || '前期間比';
  const title = `前の期間（${formatDate(comparison.start)} 〜 ${formatDate(comparison.end)}）: ${number(previous)}`;
  if (Number(previous) === 0) return `<span class="delta" title="${esc(title)}">${label} —</span>`;
  const percent = (current - previous) / previous * 100;
  const size = Math.abs(percent);
  const text = size < 0.05 ? '±0%' : `${percent > 0 ? '▲' : '▼'} ${size >= 100 ? Math.round(size) : size.toFixed(1)}%`;
  // 大きく増えたときだけ色を付ける（+50% 以上で注意、+100% 以上で警告）
  const tone = percent >= 100 ? 'red' : percent >= 50 ? 'yellow' : '';
  return `<span class="delta ${tone}" title="${esc(title)}">${label} ${text}</span>`;
}
const wafDelta = action => delta(action.count, state.snapshot?.comparison?.waf?.[action.name]);

function setTheme(theme, persist = true) {
  const next = theme === 'light' ? 'light' : 'dark';
  document.documentElement.dataset.theme = next;
  if (persist) localStorage.setItem(THEME_KEY, next);
  const toggle = $('theme-toggle');
  if (!toggle) return;
  const light = next === 'light';
  toggle.setAttribute('aria-pressed', String(light));
  $('theme-icon').textContent = light ? '☀' : '☾';
  $('theme-label').textContent = light ? 'ライトモード' : 'ダークモード';
  $('theme-detail').textContent = light ? 'ダークに切り替え' : 'ライトに切り替え';
}

function initializeTheme() {
  const saved = localStorage.getItem(THEME_KEY);
  const preferred = window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  setTheme(saved || preferred, false);
  $('theme-toggle').addEventListener('click', () => {
    setTheme(document.documentElement.dataset.theme === 'light' ? 'dark' : 'light');
  });
}

function nav() {
  $('nav').innerHTML = Object.entries(pages).map(([key, page]) => `<button data-page="${key}"><span>${page[2]}</span>${page[0]}</button>`).join('');
  $('nav').querySelectorAll('[data-page]').forEach(button => button.addEventListener('click', () => changePage(button.dataset.page)));
}
function changePage(page) {
  if (!Object.hasOwn(pages, page) || state.page === page) return;
  state.page = page;
  state.search = '';
  render(true);
  loadDashboard(false, false, false);
  if (page === 'queries') loadQueriesPage();
}
function setHeader() {
  const page = pages[state.page]; $('page-title').textContent = page[0]; $('page-subtitle').textContent = page[1]; $('breadcrumb').textContent = `CLOUDOPS / ${page[0].toUpperCase()}`;
  document.querySelectorAll('[data-page]').forEach(button => { button.classList.toggle('active', button.dataset.page === state.page); button.setAttribute('aria-current', button.dataset.page === state.page ? 'page' : 'false'); });
}
function setStatus(message, updated = '') { $('status-text').textContent = message; $('updated-at').textContent = updated; }
function snapshotMessage() { return state.snapshot?.collector?.message || 'データが読み込まれていません。'; }
function kpis(items, className = '') { return `<div class="grid kpis ${className}">${items.map(([label, value, caption, color, extra = '']) => `<article class="card"><small>${label}</small><div class="value" style="color:${color || 'var(--text)'}">${value}</div><small>${caption}</small>${extra}</article>`).join('')}</div>`; }
function legend(items = []) { const list = Array.isArray(items) ? items : []; return `<div class="legend">${list.map(item => `<span><i style="background:${item.color}"></i>${esc(item.name)}</span>`).join('')}</div>`; }

// 軸の上限を 1, 2, 2.5, 5 × 10^n のきりのよい値に切り上げる
function niceMax(value) {
  if (!(value > 0)) return 1;
  const magnitude = 10 ** Math.floor(Math.log10(value));
  return [1, 2, 2.5, 5, 10].map(step => step * magnitude).find(candidate => candidate >= value);
}
// 時系列の折れ線グラフ。series は { name, color, points: [{ t, v }] } の配列
function timeChart(series, { unit = '', max = null, label = '' } = {}) {
  const lines = series.filter(item => hasSeries(item.points)).map(item => ({ ...item, times: item.points.map(point => Date.parse(point.t)) }));
  if (!lines.length) return '<div class="empty">選択した期間のデータがありません。</div>';
  const allTimes = lines.flatMap(item => item.times);
  const start = Date.parse(state.snapshot?.start) || Math.min(...allTimes);
  const end = Date.parse(state.snapshot?.end) || Math.max(...allTimes);
  const upper = max ?? niceMax(Math.max(...lines.flatMap(item => item.points.map(point => point.v))));
  const chart = { lines, start, end, unit, upper };
  const id = chartRegistry.push(chart) - 1;
  return `<div class="chart-wrap" data-chart="${id}"><svg class="chart" viewBox="0 0 ${CHART.width} ${CHART.height}" role="img" aria-label="${esc(label)}">${drawChart(chart, CHART.width)}</svg><div class="chart-tooltip" role="status" hidden></div></div>`;
}
// グラフの中身を指定した幅で描画する。表示後に layoutCharts が実際の箱の幅で描き直し、
// 文字を拡大・縮小させずに横幅いっぱいに表示する
function drawChart(chart, width) {
  const { lines, start, end, unit, upper } = chart;
  const { height, left, right, top, bottom } = CHART;
  const x = time => left + (end > start ? (time - start) / (end - start) : 0.5) * (width - left - right);
  const y = value => top + (1 - Math.max(0, Math.min(value, upper)) / upper) * (height - top - bottom);
  chart.width = width;
  chart.x = x;
  const grid = [0, 0.5, 1].map(ratio => { const lineY = y(upper * ratio).toFixed(1); return `<line class="gridline" x1="${left}" x2="${width - right}" y1="${lineY}" y2="${lineY}"/><text class="axis" x="${left - 8}" y="${Number(lineY) + 4}" text-anchor="end">${axisNumber(upper * ratio)}${esc(unit.trim())}</text>`; }).join('');
  const ticks = [start, (start + end) / 2, end].map((time, index) => `<text class="axis" x="${x(time).toFixed(1)}" y="${height - 8}" text-anchor="${['start', 'middle', 'end'][index]}">${esc(formatDate(time, AXIS_DATE))}</text>`).join('');
  const marks = lines.map(item => item.points.length === 1
    ? `<circle cx="${x(item.times[0]).toFixed(1)}" cy="${y(item.points[0].v).toFixed(1)}" r="3" style="fill:${item.color}"/>`
    : `<polyline class="series" style="stroke:${item.color}" points="${item.points.map((point, index) => `${x(item.times[index]).toFixed(1)},${y(point.v).toFixed(1)}`).join(' ')}"/>`).join('');
  return `${grid}${ticks}<g class="chart-data">${marks}</g><line class="guide" y1="${top}" y2="${height - bottom}" visibility="hidden"/><rect class="hit" x="${left}" y="${top}" width="${width - left - right}" height="${height - top - bottom}"/>`;
}
function layoutCharts() {
  document.querySelectorAll('.chart-wrap').forEach(wrap => {
    const chart = chartRegistry[wrap.dataset.chart];
    const svg = wrap.querySelector('svg');
    const width = Math.round(svg?.clientWidth || 0);
    if (!chart || !svg || width < 200 || width === chart.width) return;
    wrap.querySelector('.chart-tooltip').hidden = true;
    svg.setAttribute('viewBox', `0 0 ${width} ${CHART.height}`);
    svg.innerHTML = drawChart(chart, width);
  });
}
// 描画し直した場合も同じ再生位置から続け、バックグラウンド更新では再開しない。
function animateCharts() {
  const elapsed = performance.now() - chartEntranceStartedAt;
  if (elapsed >= CHART_ENTRANCE_MS || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return;
  const play = (element, keyframes) => {
    if (!element.animate || element.getAnimations().length) return;
    const animation = element.animate(keyframes, { duration: CHART_ENTRANCE_MS, easing: 'cubic-bezier(0.22, 1, 0.36, 1)' });
    animation.currentTime = elapsed;
  };
  document.querySelectorAll('.chart-data, .bars').forEach(element => play(element, [
    { clipPath: 'inset(-6px 100% -6px -6px)' },
    { clipPath: 'inset(-6px)' }
  ]));
  document.querySelectorAll('.ring-value').forEach(element => play(element, [
    { strokeDasharray: `0 ${element.getTotalLength()}` },
    { strokeDasharray: element.getAttribute('stroke-dasharray') }
  ]));
}
// 概要画面のタイル内に置く小さな棒グラフ（軸なし）。系列を最大 32 本にまとめる
function miniBars(points, color) {
  if (!hasSeries(points)) return '';
  const size = Math.ceil(points.length / 32); const sums = [];
  for (let index = 0; index < points.length; index += size) sums.push(points.slice(index, index + size).reduce((sum, point) => sum + point.v, 0));
  const top = Math.max(...sums) || 1;
  return `<div class="bars" aria-hidden="true">${sums.map(value => `<i style="height:${Math.max(4, value / top * 100).toFixed(1)}%;background:${color}"></i>`).join('')}</div>`;
}
// 同心円のリング。rings は { percent, color } の配列（外側から最大 3 本）
function ring(rings) {
  return `<svg class="ring" viewBox="0 0 120 120" aria-hidden="true">${rings.slice(0, 3).map(({ percent, color }, index) => {
    const radius = 50 - index * 15; const length = 2 * Math.PI * radius; const filled = Math.max(0, Math.min(100, Number(percent) || 0)) / 100 * length;
    return `<circle class="ring-track" cx="60" cy="60" r="${radius}"/><circle class="ring-value" cx="60" cy="60" r="${radius}" style="stroke:${color}" stroke-dasharray="${filled.toFixed(1)} ${length.toFixed(1)}"/>`;
  }).join('')}</svg>`;
}
const wafSeries = actions => actions.map(action => ({ name: action.name, color: action.color, points: action.series }));
const instanceSeries = (items, nameField, seriesField) => items.map((item, index) => ({ name: item[nameField], color: SERIES_COLORS[index % SERIES_COLORS.length], points: item[seriesField] }));

function statusNotice() { const message = snapshotMessage(); return message ? `<div class="notice ${state.snapshot?.mode === 'aws' ? 'warn' : ''}">${esc(message)}</div>` : ''; }
// 概要画面。大きさの異なるタイルを Bento グリッドに並べる（ブロック数を主役にする）
function renderOverview() {
  const { waf, rds, ec2 } = state.snapshot; const actions = waf.actions || []; const databases = rds || []; const instances = (ec2 || []).filter(item => item.instance_id);
  const periodName = PERIOD_LABELS[state.snapshot.period] || '選択期間';
  const blocked = actions.find(item => item.name === 'ブロック'); const allowed = actions.find(item => item.name === '許可');
  // 前期間比 +50% 以上（delta() の黄色と同じ基準）で「要確認」を付ける
  const previousBlocked = blocked ? state.snapshot.comparison?.waf?.[blocked.name] : null;
  const surge = previousBlocked > 0 && (blocked.count - previousBlocked) / previousBlocked >= 0.5;
  const hero = card(`WAF ブロック数 · ${periodName}`, `<div class="hero-value">${blocked ? number(blocked.count) : NO_DATA}</div>${blocked ? wafDelta(blocked) : ''}${timeChart(wafSeries(actions), { label: 'アクション別WAFリクエスト数の推移' })}${legend(actions)}`, surge ? badge('要確認', 'red') : '', 'hero span-2 tall');
  const allowedTile = `<article class="card tile"><span class="tile-label">許可リクエスト · ${periodName}</span><div class="big">${allowed ? number(allowed.count) : NO_DATA}</div>${allowed ? wafDelta(allowed) + miniBars(allowed.series, allowed.color) : ''}</article>`;
  // EC2 の CPU・メモリをインスタンスごとのリングで表示する（色は EC2 画面のグラフと同じ順番）
  const ec2Tile = (title, field, available) => {
    const rings = instances.slice(0, 3).map((item, index) => ({ name: item.name || item.instance_id, percent: item[field], available: item[available], color: SERIES_COLORS[index] }));
    const shown = rings.filter(item => item.available);
    return `<article class="card tile"><span class="tile-label">EC2 · ${title} 平均</span>${shown.length ? `<div class="ring-row">${ring(shown)}<div class="ring-legend">${rings.map(item => `<div><i style="background:${item.color}"></i>${esc(item.name)} <strong>${item.available ? `${decimal(item.percent)}%` : NO_DATA}</strong></div>`).join('')}</div></div>` : `<div class="big">${NO_DATA}</div>`}<button class="link-button" data-page-link="ec2">詳細 →</button></article>`;
  };
  const db = databases[0];
  const connections = db && hasSeries(db.connections_series) ? `<div class="big-row"><span class="big">${number(db.connections)}</span><small>接続 · 最大 ${number(db.connections_max)}</small></div>${delta(db.connections, previousOf('rds', db.identifier, 'connections'))}` : `<div class="big">${NO_DATA}</div>`;
  const rdsBody = db ? `${connections}<div><div class="kv"><span>CPU</span><b>${hasSeries(db.cpu_series) ? `${decimal(db.cpu_percent)}%` : NO_DATA}</b></div><div class="capsule"><i style="width:${hasSeries(db.cpu_series) ? Math.max(0, Math.min(100, db.cpu_percent)) : 0}%"></i></div></div><div class="kv"><span>空きストレージ</span><b>${hasSeries(db.free_storage_series) ? `${decimal(db.free_storage_gb)} GB` : NO_DATA}</b></div>${databases.length > 1 ? `<small>他 ${databases.length - 1} 件</small>` : ''}` : `<div class="big">${NO_DATA}</div>`;
  const rdsTile = `<article class="card tile"><span class="tile-label">RDS · ${db ? esc(db.identifier) : 'データベース'}</span>${rdsBody}<button class="link-button" data-page-link="rds">詳細 →</button></article>`;
  // 保存済みクエリへのショートカット。押してもクエリ画面に SQL が入るだけで、実行はしない
  const shortcuts = state.savedQueries.slice(0, 4).map(query => `<button type="button" data-open-query="${esc(query.id)}">${esc(query.name)}<span aria-hidden="true">↗</span></button>`).join('');
  const athenaTile = card('Athena · ワンクリック調査', `${shortcuts ? `<div class="quick">${shortcuts}</div>` : '<p class="muted">保存済みクエリがありません（queries/ フォルダ）。</p>'}<small class="tile-foot">選ぶとクエリ画面に SQL が入ります。「実行」を押すまで料金は発生しません。</small>`, '<button class="link-button" data-page-link="queries">すべてのクエリ →</button>', 'tile span-2');
  const ec2Table = card('EC2インスタンス', table(['インスタンス', 'CPU（平均）', 'メモリ（平均）', 'ステータス'], (ec2 || []).map(item => [esc(item.name), item.cpu_available ? meter(item.cpu_percent) : NO_DATA, item.memory_available ? `${decimal(item.memory_percent)}%` : NO_DATA, badge(item.status ? statusLabel(item.status) : NO_DATA, item.status === 'Healthy' ? '' : 'yellow')])), '', 'span-2');
  return `<div class="bento">${hero}${allowedTile}${rdsTile}${ec2Tile('CPU', 'cpu_percent', 'cpu_available')}${ec2Tile('メモリ', 'memory_percent', 'memory_available')}${athenaTile}${ec2Table}</div>${statusNotice()}`;
}
// サンプルリクエストのアクション（バックエンドが日本語の表示名で返す）ごとのバッジの色
const WAF_EVENT_TONES = { ブロック: 'red', BLOCK: 'red', チャレンジ: 'yellow', CHALLENGE: 'yellow', CAPTCHA: 'yellow', カウント: 'blue', COUNT: 'blue', EXCLUDED_AS_COUNT: 'blue' };
// ルール別ブロック数（選択期間、上位 10 件）。棒の長さは 1 位を 100% とした比率
function ruleBars(rules = [], message = '') {
  if (message) return `<div class="notice warn">${esc(message)}</div>`;
  if (!rules.length) return '<div class="empty">選択した期間にブロックしたルールはありません</div>';
  const top = rules.slice(0, 10); const max = top[0].blocked || 1;
  return `<div class="rule-bars">${top.map(rule => `<div class="rule-bar"><span class="rule-name" title="${esc(rule.rule)}">${esc(rule.rule)}</span><strong>${number(rule.blocked)}</strong><span class="rule-track" aria-hidden="true"><i style="width:${rule.blocked / max * 100}%"></i></span></div>`).join('')}</div>`;
}
function renderWAF() {
  const { waf } = state.snapshot; const actions = Array.isArray(waf.actions) ? waf.actions : [];
  // サンプルリクエストは期間の選択に関係なく、常に直近 3 時間のもの（GetSampledRequests、無料）。
  // 最大 200 件あるため、クエリ結果と同じく表の高さを固定してスクロールさせる（.result-table）
  const events = (waf.events || []).map(event => [esc(formatDate(event.time, { dateStyle: 'short', timeStyle: 'medium' })), badge(event.action, WAF_EVENT_TONES[event.action] || ''), esc(event.ip || '—'), esc(event.country || '—'), esc(event.method || '—'), `<span class="uri">${esc(event.uri || '—')}</span>`, esc(event.rule || '—')]);
  const sampleWindow = waf.events_from && waf.events_to ? ` · ${formatDate(waf.events_from)} 〜 ${formatDate(waf.events_to)}` : '';
  const eventsNote = waf.events_message ? `<div class="notice warn">${esc(waf.events_message)}</div>` : `<p class="muted card-note">AWS WAF が記録したサンプルで、すべてのリクエストではありません${esc(sampleWindow)}。全件を調べる場合は「クエリ」画面の Athena クエリを使用します。</p>`;
  const rules = card('ルール別ブロック数', ruleBars(waf.rules || [], waf.rules_message), '<small>選択期間 · 上位 10 件</small>');
  const samples = card('サンプルリクエスト（直近3時間）', `${eventsNote}<div class="result-table">${table([`時刻 (${esc(timeZone())})`, 'アクション', 'クライアントIP', '国', 'メソッド', 'URI', 'ルール'], events)}</div>`, '<input id="search" aria-label="サンプルリクエストを絞り込み" placeholder="IP・URI・ルールで絞り込み…">');
  return kpis(actions.map(item => [item.name, number(item.count), 'リクエスト数 · 選択期間', item.color, wafDelta(item)]), 'waf-kpis') + `<div class="section">${card('アクションの推移', timeChart(wafSeries(actions), { label: 'アクション別WAFリクエスト数の推移' }), legend(actions))}</div><div class="section">${rules}</div>` + samples + statusNotice();
}
function renderEC2() {
  const items = state.snapshot.ec2 || []; const configured = items.some(item => item.instance_id);
  const withData = available => items.filter(item => item[available]);
  const average = (field, available) => { const values = withData(available); return values.length ? `${decimal(values.reduce((sum, item) => sum + item[field], 0) / values.length)}%` : NO_DATA; };
  const peak = (field, available) => { const values = withData(available); return values.length ? `最大 ${decimal(Math.max(...values.map(item => item[field])))}%` : ''; };
  // 前期間の平均は、前期間のデータがあるインスタンスだけで計算する
  const averageDelta = (field, available, key) => { const pairs = withData(available).map(item => [item[field], previousOf('ec2', item.instance_id, key)]).filter(([, previous]) => previous != null); return delta(mean(pairs.map(([current]) => current)), mean(pairs.map(([, previous]) => previous))); };
  const instanceCaption = state.snapshot.mode === 'screenshot' ? '撮影用の架空インスタンス' : configured ? 'AWS収集中' : (state.snapshot.mode === 'demo' ? 'デモモード' : '設定待ち');
  const cpuLines = instanceSeries(items, 'name', 'cpu_series'); const memoryLines = instanceSeries(items, 'name', 'memory_series');
  return kpis([['インスタンス数', configured ? number(items.length) : NO_DATA, instanceCaption], ['正常', configured ? number(items.filter(item => item.status === 'Healthy').length) : NO_DATA, 'ステータスチェック'], ['平均CPU', average('cpu_percent', 'cpu_available'), peak('cpu_max_percent', 'cpu_available') || 'CloudWatch', '', averageDelta('cpu_percent', 'cpu_available', 'cpu')], ['平均メモリ', average('memory_percent', 'memory_available'), peak('memory_max_percent', 'memory_available') || 'CloudWatch', '', averageDelta('memory_percent', 'memory_available', 'memory')]])
    + `<div class="grid equal">${card('CPU使用率の推移', timeChart(cpuLines, { unit: '%', max: 100, label: 'インスタンス別CPU使用率の推移' }), legend(cpuLines))}${card('メモリ使用率の推移', timeChart(memoryLines, { unit: '%', max: 100, label: 'インスタンス別メモリ使用率の推移' }), legend(memoryLines))}</div>`
    + card('インスタンス一覧', table(['名前', 'インスタンスID', 'CPU 平均', 'CPU 最大', 'メモリ 平均', 'メモリ 最大', '状態', 'ゾーン'], items.map(item => [esc(item.name), esc(item.instance_id || '—'), item.cpu_available ? meter(item.cpu_percent) + delta(item.cpu_percent, previousOf('ec2', item.instance_id, 'cpu')) : NO_DATA, item.cpu_available ? `${decimal(item.cpu_max_percent)}%` : NO_DATA, item.memory_available ? `${decimal(item.memory_percent)}%` + delta(item.memory_percent, previousOf('ec2', item.instance_id, 'memory')) : NO_DATA, item.memory_available ? `${decimal(item.memory_max_percent)}%` : NO_DATA, badge(item.state || NO_DATA), esc(item.zone || '—')])), '<input id="search" aria-label="インスタンスを検索" placeholder="インスタンスを検索…">') + statusNotice();
}
function renderRDS() {
  const items = state.snapshot.rds || [];
  const sections = items.length ? items.map((item, index) => {
    const color = SERIES_COLORS[index % SERIES_COLORS.length];
    const metric = (points, value, caption) => hasSeries(points) ? [value, caption] : [NO_DATA, 'CloudWatch'];
    const change = (points, current, key) => hasSeries(points) ? delta(current, previousOf('rds', item.identifier, key)) : '';
    const [cpu, cpuCaption] = metric(item.cpu_series, `${decimal(item.cpu_percent)}%`, `平均 · 最大 ${decimal(item.cpu_max_percent)}%`);
    const [latency, latencyCaption] = metric(item.write_latency_series, `${decimal(item.write_latency_ms)} ms`, `平均 · 最大 ${decimal(item.write_latency_max_ms)} ms`);
    const [connections, connectionsCaption] = metric(item.connections_series, number(item.connections), `平均 · 最大 ${number(item.connections_max)}`);
    const [storage, storageCaption] = metric(item.free_storage_series, `${decimal(item.free_storage_gb)} GB`, '最新値');
    const chart = (title, points, unit, max = null) => card(title, timeChart([{ name: title, color, points }], { unit, max, label: `${item.identifier} ${title}の推移` }));
    return `${items.length > 1 ? `<h2 class="section-title">${esc(item.identifier)}</h2>` : ''}${kpis([['CPU', cpu, cpuCaption, '', change(item.cpu_series, item.cpu_percent, 'cpu')], ['書き込みレイテンシ', latency, latencyCaption, '', change(item.write_latency_series, item.write_latency_ms, 'write_latency_ms')], ['接続数', connections, connectionsCaption, '', change(item.connections_series, item.connections, 'connections')], ['空きストレージ', storage, storageCaption]])}<div class="grid equal">${chart('CPU使用率', item.cpu_series, '%', 100)}${chart('書き込みレイテンシ', item.write_latency_series, ' ms')}</div><div class="grid equal">${chart('接続数', item.connections_series, '')}${chart('空きストレージ', item.free_storage_series, ' GB')}</div>`;
  }).join('') : kpis([['CPU', NO_DATA, 'CloudWatch'], ['書き込みレイテンシ', NO_DATA, 'CloudWatch'], ['接続数', NO_DATA, 'CloudWatch'], ['空きストレージ', NO_DATA, 'CloudWatch']]);
  return sections + card('インスタンス詳細', table(['識別子', 'エンジン', 'ステータス'], items.map(item => [esc(item.identifier || NO_DATA), esc(item.engine || '—'), badge(item.status || NO_DATA)]))) + statusNotice();
}
const isRunning = query => ['QUEUED', 'RUNNING'].includes(query.state);
const queryTone = value => ({ SUCCEEDED: '', FAILED: 'red', CANCELLED: 'yellow' }[value] ?? 'blue');
const formatCost = value => value ? `$${Number(value).toFixed(4)}` : '—';
function formatBytes(value) { let size = Number(value) || 0; const units = ['B', 'KB', 'MB', 'GB', 'TB']; let index = 0; while (size >= 1024 && index < units.length - 1) { size /= 1024; index += 1; } return `${number(Number(size.toFixed(index ? 2 : 0)))} ${units[index]}`; }
// WAF ログは数分おきに S3 へ配信され、配信時刻のフォルダに入る。期間末尾のレコードを取りこぼさないよう、パーティションの終了だけ余裕を持たせる
const PARTITION_END_MARGIN_MS = 15 * 60 * 1000;
// 保存済みクエリの {{...}} を、ページ上部で選択中の期間と Athena の設定値に置き換える。未知のプレースホルダーはそのまま残す
function fillPlaceholders(sql) {
  const { start, end } = selectedRange();
  const utc = date => date.toISOString().slice(0, 19).replace('T', ' ');
  const day = date => date.toISOString().slice(0, 10).replaceAll('-', '/');
  // パーティション射影（yyyy/MM/dd/HH/mm、UTC）用
  const minute = date => date.toISOString().slice(0, 16).replace(/[-T:]/g, '/');
  const values = { table: state.athena?.table, database: state.athena?.database, timezone: timeZone(), start: utc(start), end: utc(end), start_ms: start.getTime(), end_ms: end.getTime(), start_date: day(start), end_date: day(end), start_minute: minute(start), end_minute: minute(new Date(end.getTime() + PARTITION_END_MARGIN_MS)) };
  return sql.replace(/\{\{(\w+)\}\}/g, (match, key) => values[key] ? String(values[key]) : match);
}
function renderQueries() {
  const athena = state.athena || {};
  const running = state.queries.some(isRunning);
  const selected = state.savedQueries.find(query => query.id === state.selectedQuery);
  const options = [`<option value="">保存済みクエリを選択…（${state.savedQueries.length}件）</option>`, ...state.savedQueries.map(query => `<option value="${esc(query.id)}"${query.id === state.selectedQuery ? ' selected' : ''}>${esc(query.name)}</option>`)].join('');
  const usage = athena.usage;
  const locked = Boolean(usage?.locked);
  const runLabel = state.snapshot.mode === 'screenshot' ? '撮影用・実行不可' : running ? '実行中…' : locked ? '上限に到達' : athena.configured ? '▶ クエリを実行' : 'Athena未設定';
  const editor = `<select id="saved-query" class="query-picker" aria-label="保存済みクエリ">${options}</select>${selected?.description ? `<p class="muted query-description">${esc(selected.description)}</p>` : ''}<textarea id="sql" aria-label="SQLエディタ" spellcheck="false" placeholder="保存済みクエリを選択するか、SQLを入力してください">${esc(state.sql)}</textarea><label class="private-option"><input type="checkbox" id="private-query"${state.privateQuery ? ' checked' : ''}>結果を共有しない（SQL と結果は自分にだけ表示。実行したことは履歴に残ります）</label><div class="actions"><small>SELECT/WITHのみ · log_time の範囲は最大 ${number(usage?.max_query_days ?? 31)} 日 · 同時実行は 1 件まで</small><button class="button" id="run" ${athena.configured && !running && !locked ? '' : 'disabled'}>${runLabel}</button></div>`;
  const settings = [['ワークグループ', athena.workgroup], ['データベース', athena.database], ['テーブル', athena.table], ['リージョン', athena.region]].map(([label, value]) => `<div class="metric"><small>${label}</small><strong class="setting">${esc(value || '未設定')}</strong></div>`).join('');
  const summary = `${usageBlock(usage)}<div class="mini settings">${settings}</div>${athena.configured ? '' : `<div class="notice warn">${esc(athena.message || 'Athenaの設定を読み込み中…')}</div>`}<div class="notice">クエリは「実行」を押したときだけ発行されます。料金はスキャン量 1TB あたり約 5 USD（1 クエリ最小 10MB）です。1日の使用額が上限に達すると、管理者が上限を引き上げるまで実行できなくなります。</div>`;
  const rows = state.queries.map(query => [esc(formatDate(query.started_at)), `${esc(query.name)}${query.private ? ` ${badge('非共有', 'yellow')}` : ''}`, esc(query.user || '—'), badge(statusLabel(query.state), queryTone(query.state)), query.duration_ms ? `${number(query.duration_ms / 1000)} 秒` : '—', query.bytes_scanned ? formatBytes(query.bytes_scanned) : '—', formatCost(query.cost_usd), isRunning(query) ? `<button data-cancel="${esc(query.id)}">キャンセル</button>` : `<button data-result="${esc(query.id)}">結果を表示</button>`]);
  const result = state.queries.find(query => query.id === state.resultId) || state.queries[0];
  return `<div class="grid two">${card('SQLエディタ', editor)}${card('Athena設定', summary)}</div><div class="section">${card('クエリ履歴', table(['開始時刻', 'クエリ', '実行者', '状態', '所要時間', 'スキャン量', '推定コスト', ''], rows), '<small>最新 20 件を表示（すべての履歴はデータベースに保存）</small>')}</div>${result ? resultCard(result) : ''}`;
}
// 今日の使用額（同じワークグループで実行したすべてのクエリの推定料金）と上限
function usageBlock(usage) {
  if (!usage) return '';
  const percent = Math.max(0, Math.min(100, usage.spent_usd / usage.budget_usd * 100 || 0));
  const head = `<div class="usage"><div class="usage-head"><small>今日の使用額${usage.day ? `（${esc(usage.day)}）` : ''}</small><strong>$${Number(usage.spent_usd).toFixed(4)}<span> / $${Number(usage.budget_usd).toFixed(2)}</span></strong></div><span class="usage-bar${usage.locked || percent >= 80 ? ' high' : ''}" role="img" aria-label="上限の ${decimal(percent)}%"><i style="width:${percent}%"></i></span></div>`;
  const locked = usage.locked ? `<div class="notice danger">1日の使用額の上限に達したため、クエリの実行を停止しています。管理者が config.json の athena.daily_budget_usd を引き上げてコンテナを再作成するか、停止を解除するまで実行できません。</div>` : '';
  const error = usage.error ? `<div class="notice warn">今日の使用額を確認できません。確認できるまでクエリは実行されません。${esc(usage.error)}</div>` : '';
  return head + locked + error;
}
function resultCard(query) {
  // 他のユーザーが「結果を共有しない」で実行したクエリは、SQL も結果も返ってこない
  if (query.hidden) return card(`結果: ${esc(query.name)}`, `<div class="notice">このクエリは ${esc(query.user || '他のユーザー')} さんが「結果を共有しない」で実行したため、SQL と結果は表示できません。</div>`, `<div class="result-actions"><small>${esc(formatDate(query.started_at))}</small></div>`);
  let body;
  if (isRunning(query)) body = '<div class="empty">クエリを実行中です…</div>';
  else if (query.state !== 'SUCCEEDED') body = `<div class="notice warn">${esc(query.error || statusLabel(query.state))}</div>`;
  else if (!query.columns?.length) body = `<div class="notice warn">${esc(query.error || '結果を取得できませんでした。')}</div>`;
  // 結果が長くても画面が伸びないよう、表は高さを固定してスクロールさせる
  else body = `<p class="muted result-count">${number((query.rows || []).length)} 行${query.truncated ? '（表示上限）' : ''}</p><div class="result-table">${table(query.columns.map(esc), (query.rows || []).map(row => row.map(esc)))}</div>` + (query.truncated ? '<div class="notice">先頭 1,000 行のみ表示しています。全件は「CSVダウンロード」で取得できます。</div>' : '');
  const download = query.state === 'SUCCEEDED' && state.snapshot.mode !== 'screenshot' ? `<button class="button" data-csv="${esc(query.id)}">⤓ CSVダウンロード</button>` : '';
  return card(`結果: ${esc(query.name)}`, `${body}<details class="query-sql"><summary>実行したSQL</summary><pre>${esc(query.sql)}</pre></details>`, `<div class="result-actions"><small>${esc(formatDate(query.started_at))}</small>${download}</div>`);
}
// S3 の署名付き URL（有効期限 5 分）を受け取り、ブラウザで全件の CSV をダウンロードする
async function downloadCsv(id, button) {
  button.disabled = true; button.textContent = '準備中…';
  try { const { url } = await api(`/api/queries/${encodeURIComponent(id)}/csv`); window.location.href = url; } catch (error) { window.alert(error.message); } finally { button.disabled = false; button.textContent = '⤓ CSVダウンロード'; }
}
function selectSavedQuery(id) {
  // 編集中の SQL を別のクエリで上書きする前に確認する
  if (state.sql.trim() && state.sql !== state.filledSql && !window.confirm('編集中のSQLを選択したクエリで置き換えますか？')) { render(); return; }
  state.selectedQuery = id;
  const saved = state.savedQueries.find(query => query.id === id);
  if (saved) {
    try { state.sql = fillPlaceholders(saved.sql); } catch (error) { state.sql = saved.sql; setStatus('期間を確認してください', error.message); }
    state.filledSql = state.sql;
  }
  render();
}
// 期間を変更したとき、保存済みクエリが未編集であれば新しい期間で入れ直す
function refillSavedQuery() {
  const saved = state.savedQueries.find(query => query.id === state.selectedQuery);
  if (!saved || state.sql !== state.filledSql) return;
  try { state.sql = state.filledSql = fillPlaceholders(saved.sql); } catch { /* カスタム期間が未入力の場合は何もしない */ }
}
function table(heads, rows) { return `<div class="scroll"><table><thead><tr>${heads.map(head => `<th>${head}</th>`).join('')}</tr></thead><tbody>${rows.length ? rows.map(row => `<tr>${row.map(cell => `<td>${cell}</td>`).join('')}</tr>`).join('') : '<tr><td colspan="99" class="empty">データがありません</td></tr>'}</tbody></table></div>`; }
function nearestIndex(times, time) { let best = 0; times.forEach((value, index) => { if (Math.abs(value - time) < Math.abs(times[best] - time)) best = index; }); return best; }
function bindChartTooltips() {
  document.querySelectorAll('.chart-wrap').forEach(wrap => {
    const tooltip = wrap.querySelector('.chart-tooltip');
    const chart = chartRegistry[wrap.dataset.chart];
    const svg = wrap.querySelector('svg');
    if (!tooltip || !chart || !svg) return;
    const position = (clientX, clientY) => {
      const bounds = wrap.getBoundingClientRect();
      const tooltipBounds = tooltip.getBoundingClientRect();
      let left = clientX - bounds.left + 14;
      let top = clientY - bounds.top - tooltipBounds.height - 14;
      if (left + tooltipBounds.width > bounds.width - 8) left = clientX - bounds.left - tooltipBounds.width - 14;
      if (left < 8) left = 8;
      if (top < 8) top = clientY - bounds.top + 14;
      tooltip.style.left = `${left}px`;
      tooltip.style.top = `${top}px`;
    };
    // 中身は layoutCharts で描き直されるため、ガイド線は毎回取得する
    const hide = () => { tooltip.hidden = true; svg.querySelector('.guide')?.setAttribute('visibility', 'hidden'); };
    // グラフの描画領域（.hit）の上にカーソルがあるときだけ表示する。
    // 座標は getScreenCTM で viewBox 座標に変換する
    svg.addEventListener('pointermove', event => {
      const matrix = svg.getScreenCTM();
      if (!event.target.classList?.contains('hit') || !matrix) { hide(); return; }
      const guide = svg.querySelector('.guide');
      const viewX = new DOMPoint(event.clientX, event.clientY).matrixTransform(matrix.inverse()).x;
      const time = chart.start + (viewX - CHART.left) / (chart.width - CHART.left - CHART.right) * (chart.end - chart.start);
      // カーソルに最も近い時刻を基準に、各系列のその時刻に最も近い値を表示する
      const candidates = chart.lines.map(line => line.times[nearestIndex(line.times, time)]);
      const reference = candidates[nearestIndex(candidates, time)];
      const rows = chart.lines.map(line => { const index = nearestIndex(line.times, reference); return `<span><i style="background:${line.color}"></i>${esc(line.name)}: ${decimal(line.points[index].v)}${esc(chart.unit)}</span>`; }).join('');
      tooltip.innerHTML = `<strong>${esc(formatDate(reference))}</strong>${rows}`;
      tooltip.hidden = false;
      guide?.setAttribute('x1', chart.x(reference).toFixed(1));
      guide?.setAttribute('x2', chart.x(reference).toFixed(1));
      guide?.setAttribute('visibility', 'visible');
      requestAnimationFrame(() => position(event.clientX, event.clientY));
    });
    svg.addEventListener('pointerleave', hide);
  });
}
function applySearch() { const term = state.search.toLowerCase(); document.querySelectorAll('tbody tr').forEach(row => { row.hidden = !row.textContent.toLowerCase().includes(term); }); }
function bindContent() {
  document.querySelectorAll('[data-page-link]').forEach(button => button.addEventListener('click', () => changePage(button.dataset.pageLink)));
  document.querySelectorAll('[data-open-query]').forEach(button => button.addEventListener('click', () => openSavedQuery(button.dataset.openQuery)));
  const search = $('search'); if (search) { search.value = state.search; search.addEventListener('input', event => { state.search = event.target.value; applySearch(); }); if (state.search) applySearch(); }
  const sql = $('sql'); if (sql) sql.addEventListener('input', event => { state.sql = event.target.value; }); const run = $('run'); if (run) run.addEventListener('click', runQuery);
  const savedQuery = $('saved-query'); if (savedQuery) savedQuery.addEventListener('change', event => selectSavedQuery(event.target.value));
  const privateQuery = $('private-query'); if (privateQuery) privateQuery.addEventListener('change', event => { state.privateQuery = event.target.checked; });
  document.querySelectorAll('[data-result]').forEach(button => button.addEventListener('click', () => { state.resultId = button.dataset.result; render(); }));
  document.querySelectorAll('[data-cancel]').forEach(button => button.addEventListener('click', () => cancelQuery(button.dataset.cancel)));
  document.querySelectorAll('[data-csv]').forEach(button => button.addEventListener('click', () => downloadCsv(button.dataset.csv, button)));
  layoutCharts();
  bindChartTooltips();
  animateCharts();
}
async function runQuery() {
  const saved = state.savedQueries.find(query => query.id === state.selectedQuery);
  const edited = !saved || state.sql !== state.filledSql;
  // 保存済みクエリ以外はスキャン量が読めないため、実行前に確認する
  const usage = state.athena?.usage;
  if (edited && !window.confirm(`編集した SQL を実行します。スキャン量に応じて料金が発生します${usage ? `（今日の使用額 $${Number(usage.spent_usd).toFixed(4)} / 上限 $${Number(usage.budget_usd).toFixed(2)}）` : ''}。実行しますか？`)) return;
  const run = $('run'); run.disabled = true; run.textContent = '開始中…';
  const name = saved ? (edited ? `${saved.name}（編集済み）` : saved.name) : 'カスタムクエリ';
  try { const query = await api('/api/queries', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ sql: state.sql, name, private: state.privateQuery }) }); state.resultId = query.id; await loadQueries(); } catch (error) { window.alert(error.message); await loadAthenaStatus(); render(); }
}
// 使用額と停止状態を取り直す（クエリの終了後や、実行を断られたとき）
async function loadAthenaStatus() {
  try { state.athena = await api('/api/athena'); } catch (error) { setStatus('Athenaの情報を取得できません', error.message); }
}
async function cancelQuery(id) { try { await api(`/api/queries/${encodeURIComponent(id)}`, { method: 'POST' }); } catch (error) { window.alert(error.message); } await loadQueries(); }
// 実行中のクエリがある間だけ、QUERY_POLL_MS ごとに状態を確認する（Athena の状態確認 API は無料）
async function loadQueries() {
  const wasRunning = state.queries.some(isRunning);
  try { state.queries = await api('/api/queries'); } catch (error) { setStatus('クエリ履歴を取得できません', error.message); return; }
  if (wasRunning && !state.queries.some(isRunning)) await loadAthenaStatus();
  if (state.page === 'queries') render();
  if (state.queries.some(isRunning) && !state.polling) { state.polling = true; setTimeout(() => { state.polling = false; loadQueries(); }, QUERY_POLL_MS); }
}
async function loadQueriesPage() {
  try { const [athena, saved] = await Promise.all([api('/api/athena'), api('/api/saved-queries')]); state.athena = athena; state.savedQueries = saved; } catch (error) { setStatus('Athenaの情報を取得できません', error.message); }
  await loadQueries();
}
// ヘッダーにログイン中のユーザー名を表示する
async function loadUser() {
  try { const { username } = await api('/api/me'); $('user-name').textContent = username; $('password-username').value = username; $('user-card').hidden = false; } catch { /* 401 の場合は api() がログイン画面に移動する */ }
}
// 自分のパスワードを変更する。成功するとサーバーがこの端末に新しいセッションを発行し、他の端末のログインは解除される
function initializePasswordDialog() {
  const dialog = $('password-dialog'), form = $('password-form'), error = $('password-error'), submit = $('password-submit');
  const showError = message => { error.textContent = message; error.hidden = !message; };
  $('open-password').addEventListener('click', () => { form.reset(); showError(''); dialog.showModal(); });
  dialog.querySelector('[data-password-cancel]').addEventListener('click', () => dialog.close());
  form.addEventListener('submit', async event => {
    event.preventDefault();
    const current = $('current-password').value, next = $('new-password').value;
    if (next !== $('confirm-password').value) { showError('新しいパスワードが確認用と一致しません。'); return; }
    submit.disabled = true; submit.textContent = '変更中…';
    try {
      const result = await api('/api/password', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ current_password: current, new_password: next }) });
      dialog.close(); form.reset();
      if (result.relogin) { window.alert('パスワードを変更しました。新しいパスワードでログインしてください。'); window.location.href = '/login' + window.location.search; return; }
      window.alert('パスワードを変更しました。他の端末やブラウザのログインは解除されました。');
    } catch (failure) { showError(failure.message); } finally { submit.disabled = false; submit.textContent = '変更する'; }
  });
}
// 概要画面のショートカット用。queries/ のファイルを読むだけなので Athena の料金は発生しない
async function loadSavedQueries() {
  try { state.savedQueries = await api('/api/saved-queries'); if (state.page === 'overview') render(); } catch { /* ショートカットを表示しないだけ */ }
}
// ショートカットからクエリ画面を開き、保存済みクエリを選択する（実行はしない）
async function openSavedQuery(id) {
  state.page = 'queries'; state.search = ''; syncViewURL(); render();
  await loadQueriesPage();
  selectSavedQuery(id);
}
function selectedRangeLabel() { if (!state.snapshot?.start || !state.snapshot?.end) return ''; const comparison = state.snapshot.comparison; return `期間: ${formatDate(state.snapshot.start)} 〜 ${formatDate(state.snapshot.end)}${comparison ? ` · 比較: ${formatDate(comparison.start)} 〜 ${formatDate(comparison.end)}` : ''}`; }
function render(animate = false) { if (!state.snapshot) return; if (animate) chartEntranceStartedAt = performance.now(); setHeader(); $('mode-label').textContent = state.snapshot.mode === 'screenshot' ? '撮影モード（架空）' : state.snapshot.mode === 'demo' ? 'デモモード（空）' : 'AWSモード'; $('mode-detail').textContent = state.snapshot.mode === 'screenshot' ? '架空データ · AWS 接続なし' : state.snapshot.mode === 'demo' ? '架空データなし' : `${state.snapshot.region} · ${state.snapshot.account ? `アカウント ${state.snapshot.account}` : '収集待ち'}`; const views = { overview: renderOverview, waf: renderWAF, ec2: renderEC2, rds: renderRDS, queries: renderQueries }; chartRegistry.length = 0; $('app').innerHTML = views[state.page](); bindContent(); const collector = state.snapshot.collector; const updated = collector.latest_data ? `${formatDate(collector.latest_data)} までのデータ` : '収集未実行'; setStatus(statusLabel(collector.status), [updated, selectedRangeLabel()].filter(Boolean).join(' · ')); }
// カスタム期間は常に JST（UTC+9）で扱い、利用者の PC のタイムゾーンには依存しない。
// hidden input には JST の時刻を "YYYY-MM-DDTHH:mm" で入れ、API に送るときに UTC へ変換する。
// ピッカー内の「JST の時刻」は、UTC のフィールド（getUTCHours など）が JST の値を表す Date で持つ
const JST_OFFSET_MS = 9 * 60 * 60 * 1000;
const DAY_MS = 24 * 60 * 60 * 1000;
// バックエンド（parseMetricRange）と同じ上限
const MAX_RANGE_DAYS = 90;
const WEEKDAYS = ['日', '月', '火', '水', '木', '金', '土'];
const pad2 = value => String(value).padStart(2, '0');
const jstNow = () => { const now = new Date(Date.now() + JST_OFFSET_MS); now.setUTCSeconds(0, 0); return now; };
const jstFromInput = value => { const date = new Date(`${value}:00Z`); return value && !Number.isNaN(date.getTime()) ? date : null; };
const jstToInput = jst => jst.toISOString().slice(0, 16);
const jstDate = jst => `${jst.getUTCFullYear()}/${pad2(jst.getUTCMonth() + 1)}/${pad2(jst.getUTCDate())}`;
const jstTime = jst => `${pad2(jst.getUTCHours())}:${pad2(jst.getUTCMinutes())}`;
const jstDay = jst => Date.UTC(jst.getUTCFullYear(), jst.getUTCMonth(), jst.getUTCDate());
const PICKER_HINT = `日付は YYYY/MM/DD、時刻は hh:mm（24時間・JST）。最大${MAX_RANGE_DAYS}日、未来の日時は選べません。`;
function initializeCustomRange() { const end = jstNow(); $('period-start').value = jstToInput(new Date(end.getTime() - DAY_MS)); $('period-end').value = jstToInput(end); updateRangePill(); }
function updateRangePill() {
  const start = jstFromInput($('period-start').value); const end = jstFromInput($('period-end').value);
  $('range-label').textContent = start && end ? `${jstDate(start)} ${jstTime(start)} › ${jstDate(end)} ${jstTime(end)}` : '開始日 › 終了日';
}
// AWS コンソールと同じ形式の期間ピッカー（2か月表示・1回目のクリックで開始日、2回目で終了日）。
// start / end は編集中の値で、「適用」を押すまで hidden input には反映しない
const picker = { open: false, start: null, end: null, month: null };
function renderPickerMonths() {
  const today = jstDay(jstNow());
  const startDay = picker.start ? jstDay(picker.start) : null; const endDay = picker.end ? jstDay(picker.end) : null;
  // 未来の日は選べない。開始日だけ選んだ状態では、開始日から 90 日より先も選べない
  const limit = startDay !== null && endDay === null ? Math.min(today, startDay + MAX_RANGE_DAYS * DAY_MS) : today;
  const month = offset => {
    const first = new Date(Date.UTC(picker.month.getUTCFullYear(), picker.month.getUTCMonth() + offset, 1));
    const year = first.getUTCFullYear(); const index = first.getUTCMonth(); const lead = first.getUTCDay();
    const count = new Date(Date.UTC(year, index + 1, 0)).getUTCDate();
    const days = Array.from({ length: count }, (_, number) => {
      const day = Date.UTC(year, index, number + 1); const weekday = (lead + number) % 7;
      const classes = [weekday === 0 ? 'sun' : weekday === 6 ? 'sat' : '', day === today ? 'today' : '', day === startDay || day === endDay ? 'edge' : '', startDay !== null && endDay !== null && day > startDay && day < endDay ? 'in-range' : ''].join(' ');
      return `<button type="button" class="${classes}" data-day="${day}" aria-label="${year}年${index + 1}月${number + 1}日"${day > limit ? ' disabled' : ''}>${number + 1}</button>`;
    });
    return `<div class="picker-month"><strong>${year}年${index + 1}月</strong><div class="picker-weekdays">${WEEKDAYS.map((name, weekday) => `<span class="${weekday === 0 ? 'sun' : weekday === 6 ? 'sat' : ''}">${name}</span>`).join('')}</div><div class="picker-days">${'<span></span>'.repeat(lead)}${days.join('')}</div></div>`;
  };
  // 右側に今月が表示されていれば、それより先には進めない
  const now = jstNow(); const latest = picker.month.getUTCFullYear() * 12 + picker.month.getUTCMonth() + 1 >= now.getUTCFullYear() * 12 + now.getUTCMonth();
  $('picker-months').innerHTML = `<button type="button" class="picker-nav prev" data-month="-1" aria-label="前の月">‹</button>${month(0)}${month(1)}<button type="button" class="picker-nav next" data-month="1" aria-label="次の月"${latest ? ' disabled' : ''}>›</button>`;
}
function fillPickerFields() {
  [['start', picker.start], ['end', picker.end]].forEach(([key, value]) => {
    $(`field-${key}-date`).value = value ? jstDate(value) : ''; $(`field-${key}-time`).value = value ? jstTime(value) : '';
    $(`field-${key}-date`).removeAttribute('aria-invalid'); $(`field-${key}-time`).removeAttribute('aria-invalid');
  });
}
// 入力欄の日付と時刻を読み取る。どちらかが不正（または空）なら null
function readPickerFields(key) {
  const dateField = $(`field-${key}-date`); const timeField = $(`field-${key}-time`);
  const dateMatch = dateField.value.trim().match(/^(\d{4})[/-](\d{1,2})[/-](\d{1,2})$/);
  const timeMatch = timeField.value.trim().match(/^(\d{1,2}):(\d{2})$/);
  const [year, month, day] = dateMatch ? dateMatch.slice(1).map(Number) : [];
  const [hour, minute] = timeMatch ? timeMatch.slice(1).map(Number) : [];
  const date = dateMatch ? new Date(Date.UTC(year, month - 1, day)) : null;
  const dateValid = Boolean(date) && date.getUTCMonth() === month - 1 && date.getUTCDate() === day;
  const timeValid = Boolean(timeMatch) && hour < 24 && minute < 60;
  // 入力途中の空欄はエラーにしない
  dateField.setAttribute('aria-invalid', String(dateField.value.trim() !== '' && !dateValid));
  timeField.setAttribute('aria-invalid', String(timeField.value.trim() !== '' && !timeValid));
  return dateValid && timeValid ? new Date(date.getTime() + (hour * 60 + minute) * 60 * 1000) : null;
}
function selectPickerDay(day) {
  const timeOf = (value, fallback) => (value ? value.getUTCHours() * 60 + value.getUTCMinutes() : fallback);
  if (!picker.start || picker.end || day < jstDay(picker.start)) {
    // 新しく選び直す（開始日を決めて、終了日は未選択に戻す）
    picker.start = new Date(day + timeOf(picker.start, 0) * 60 * 1000); picker.end = null;
  } else {
    // 終了時刻の既定は 23:59。今日を選んだ場合は現在時刻までに抑える
    const end = new Date(day + timeOf(picker.end, 23 * 60 + 59) * 60 * 1000); const now = jstNow();
    picker.end = end > now ? now : end;
  }
  fillPickerFields(); renderPickerMonths();
}
function validatePicker() {
  if (!picker.start || !picker.end) return '開始と終了の日時を入力してください。';
  if (picker.end <= picker.start) return '終了は開始より後の日時にしてください。';
  if (picker.end > jstNow()) return '未来の日時は指定できません。';
  if (picker.end - picker.start > MAX_RANGE_DAYS * DAY_MS) return `指定できる期間は最大${MAX_RANGE_DAYS}日です。`;
  return '';
}
function setPickerHint(error = '') { $('picker-hint').textContent = error || PICKER_HINT; $('picker-hint').classList.toggle('error', Boolean(error)); }
function openDatePicker() {
  picker.start = jstFromInput($('period-start').value); picker.end = jstFromInput($('period-end').value);
  // 右側に終了日の月、左側にその前月を表示する
  const anchor = picker.end || jstNow(); picker.month = new Date(Date.UTC(anchor.getUTCFullYear(), anchor.getUTCMonth() - 1, 1));
  fillPickerFields(); renderPickerMonths(); setPickerHint();
  const box = $('date-picker'); box.hidden = false; box.style.left = '0px'; picker.open = true;
  $('range-pill').setAttribute('aria-expanded', 'true');
  // 画面の右端からはみ出す場合は左へずらす
  const overflow = box.getBoundingClientRect().right - (window.innerWidth - 16);
  if (overflow > 0) box.style.left = `${-overflow}px`;
}
function closeDatePicker(returnFocus = false) {
  if (!picker.open) return;
  picker.open = false; $('date-picker').hidden = true; $('range-pill').setAttribute('aria-expanded', 'false');
  if (returnFocus) $('range-pill').focus();
}
function initializeDatePicker() {
  $('range-pill').addEventListener('click', () => (picker.open ? closeDatePicker() : openDatePicker()));
  $('picker-months').addEventListener('click', event => {
    const button = event.target.closest('button'); if (!button || button.disabled) return;
    if (button.dataset.month) { picker.month = new Date(Date.UTC(picker.month.getUTCFullYear(), picker.month.getUTCMonth() + Number(button.dataset.month), 1)); renderPickerMonths(); return; }
    if (button.dataset.day) { selectPickerDay(Number(button.dataset.day)); setPickerHint(); }
  });
  ['start', 'end'].forEach(key => ['date', 'time'].forEach(part => $(`field-${key}-${part}`).addEventListener('input', () => { picker[key] = readPickerFields(key); renderPickerMonths(); setPickerHint(); })));
  document.querySelector('[data-picker-clear]').addEventListener('click', () => { picker.start = null; picker.end = null; fillPickerFields(); renderPickerMonths(); setPickerHint(); });
  document.querySelector('[data-picker-cancel]').addEventListener('click', () => closeDatePicker(true));
  // 既存の「適用」の処理（期間の読み込み）より先に登録する。入力が不正なら読み込みを止める
  $('apply-period').addEventListener('click', event => {
    const error = validatePicker();
    if (error) { event.stopImmediatePropagation(); setPickerHint(error); return; }
    $('period-start').value = jstToInput(picker.start); $('period-end').value = jstToInput(picker.end);
    updateRangePill(); closeDatePicker(true);
  });
  // ピッカーの中身はクリックのたびに描き直されるため、外側かどうかはイベント発生時の経路で判定する
  document.addEventListener('click', event => { if (picker.open && !event.composedPath().some(node => node.id === 'date-picker' || node.id === 'range-pill')) closeDatePicker(); });
  document.addEventListener('keydown', event => { if (event.key === 'Escape' && picker.open) closeDatePicker(true); });
}
// ページ上部で選択中の期間を返す。preset は固定期間（日数）のときだけ設定される
function selectedRange() {
  const period = appliedPeriod;
  if (period !== 'custom') { const end = new Date(); return { start: new Date(end.getTime() - Number(period) * 24 * 60 * 60 * 1000), end, preset: period }; }
  // hidden input の値は JST なので、+09:00 を付けて実際の時刻にする（API には toISOString で UTC として送る）
  const start = new Date(`${$('period-start').value}:00+09:00`); const end = new Date(`${$('period-end').value}:00+09:00`);
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime())) throw new Error('期間の開始と終了を選択してください。');
  if (end <= start) throw new Error('終了は開始より後の日時にしてください。');
  if (end > new Date()) throw new Error('未来の日時は指定できません。');
  if (end - start > MAX_RANGE_DAYS * DAY_MS) throw new Error(`指定できる期間は最大${MAX_RANGE_DAYS}日です。`);
  return { start, end };
}
function rangeQuery() { const range = selectedRange(); const params = new URLSearchParams(); if (range.preset) { params.set('period', range.preset); } else { params.set('start', range.start.toISOString()); params.set('end', range.end.toISOString()); } return params.toString(); }
// URL には表示中のページと適用済み期間だけを保存する。SQL や未適用のピッカー入力は含めない。
function syncViewURL(replace = false) {
  const url = new URL(window.location.href);
  const range = selectedRange();
  url.searchParams.set('page', state.page);
  url.searchParams.set('period', range.preset || 'custom');
  url.searchParams.delete('start'); url.searchParams.delete('end');
  if (!range.preset) {
    url.searchParams.set('start', range.start.toISOString());
    url.searchParams.set('end', range.end.toISOString());
  }
  if (url.href !== window.location.href) window.history[replace ? 'replaceState' : 'pushState'](null, '', url);
}
// ピッカーと同じ分単位の UTC 日時を受け付け、存在しない日付も除外する。
function dateFromURL(value) {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::00(?:\.000)?)?Z$/.test(value || '')) return null;
  const date = new Date(value);
  return Number.isFinite(date.getTime()) && date.toISOString().slice(0, 16) === value.slice(0, 16) ? date : null;
}
function restoreViewFromURL() {
  const params = new URLSearchParams(window.location.search);
  const page = params.get('page');
  state.page = Object.hasOwn(pages, page) ? page : 'overview';
  state.search = '';
  const period = params.get('period') || '1';
  $('period').value = Object.hasOwn(PERIOD_LABELS, period) ? period : '1';
  initializeCustomRange();
  if (period === 'custom') {
    const start = dateFromURL(params.get('start')); const end = dateFromURL(params.get('end'));
    if (start && end && end > start && end <= new Date() && end - start <= MAX_RANGE_DAYS * DAY_MS) {
      $('period').value = 'custom';
      $('period-start').value = jstToInput(new Date(start.getTime() + JST_OFFSET_MS));
      $('period-end').value = jstToInput(new Date(end.getTime() + JST_OFFSET_MS));
    }
  }
  closeDatePicker();
  appliedPeriod = $('period').value;
  $('custom-range').hidden = $('period').value !== 'custom';
  updateRangePill();
  // ページや期間の不正な指定はそれぞれ既定値へ戻し、履歴を増やさず正規化する。
  syncViewURL(true);
}
let dashboardRequestID = 0;
// silent = true のときは読み込み中の表示を出さず、失敗しても現在の画面を残す（自動更新用）
async function loadDashboard(refresh = false, silent = false, animate = !silent) {
  const requestID = ++dashboardRequestID;
  const apply = $('apply-period'); const refreshButton = $('refresh');
  state.loading = true;
  try {
    const query = rangeQuery();
    if (!silent) syncViewURL();
    if (!silent) { if (apply && $('period').value === 'custom') { apply.disabled = true; apply.textContent = '適用中…'; } refreshButton.disabled = true; setStatus('選択期間を読み込み中', 'CloudWatchに問い合わせ中…'); }
    const snapshot = await api(`/api/${refresh ? 'refresh' : 'dashboard'}?${query}`, refresh ? { method: 'POST' } : undefined);
    if (requestID !== dashboardRequestID) return;
    state.snapshot = snapshot;
    (state.snapshot.waf?.actions || []).forEach(action => { action.color = ACTION_COLORS[action.name] || action.color; });
    render(animate);
  } catch (error) {
    if (requestID !== dashboardRequestID) return;
    if (!silent) $('app').innerHTML = `<div class="card empty">ダッシュボードを読み込めませんでした: ${esc(error.message)}</div>`;
    setStatus('データの読み込みエラー', error.message);
  } finally {
    if (requestID === dashboardRequestID) {
      state.loading = false;
      refreshButton.disabled = false; if (apply) { apply.disabled = false; apply.textContent = '適用'; }
    }
  }
}
// カスタム期間はリクエストのたびに CloudWatch を直接呼び出すため、自動更新の対象外にする
function autoRefresh() {
  if (document.hidden || state.loading || appliedPeriod === 'custom') return;
  const active = document.activeElement;
  if (active && $('app').contains(active) && ['INPUT', 'TEXTAREA', 'SELECT'].includes(active.tagName)) return;
  loadDashboard(false, true);
}
// カスタム期間を選んだら、そのままピッカーを開く（プルダウンのクリック処理が終わってから開く）
function toggleCustomRange() { const custom = $('period').value === 'custom'; $('custom-range').hidden = !custom; if (custom && !$('period-start').value) initializeCustomRange(); if (custom) setTimeout(openDatePicker); else closeDatePicker(); }
initializeTheme(); restoreViewFromURL(); initializeDatePicker(); initializePasswordDialog(); nav(); setHeader();
$('period').addEventListener('change', () => { toggleCustomRange(); if ($('period').value !== 'custom') { appliedPeriod = $('period').value; refillSavedQuery(); loadDashboard(); } });
$('apply-period').addEventListener('click', () => { appliedPeriod = 'custom'; refillSavedQuery(); loadDashboard(); });
$('refresh').addEventListener('click', () => loadDashboard(true));
window.addEventListener('popstate', () => {
  restoreViewFromURL(); refillSavedQuery(); setHeader(); loadDashboard();
  if (state.page === 'queries') loadQueriesPage();
});
loadUser(); loadDashboard(); loadSavedQueries(); if (state.page === 'queries') loadQueriesPage(); setInterval(autoRefresh, AUTO_REFRESH_MS);
// ウィンドウの幅が変わったら、グラフを新しい幅で描き直す
let resizeFrame = 0; window.addEventListener('resize', () => { cancelAnimationFrame(resizeFrame); resizeFrame = requestAnimationFrame(() => { layoutCharts(); animateCharts(); }); });
