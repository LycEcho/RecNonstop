package api

import (
	"html"
	"net/http"
	"strings"
)

// ui 是本机（127.0.0.1）时间线页面。不做远程访问，避免把转写暴露到局域网。
func (s *Server) ui(w http.ResponseWriter, r *http.Request) {
	if !isLocal(r) {
		http.Error(w, "local only: open http://127.0.0.1:18082/ on the PC", http.StatusForbidden)
		return
	}
	name, fp := "电脑", ""
	if s.Identity != nil {
		name = s.Identity.Name
		fp = s.Identity.Fingerprint()
	}
	page := strings.ReplaceAll(timelineHTML, "{{PC_NAME}}", html.EscapeString(name))
	page = strings.ReplaceAll(page, "{{PC_FP}}", html.EscapeString(fp))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}

// adminJob 给本机页面用：不需要 device token，但只允许 localhost。
func (s *Server) adminJob(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/jobs/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	id := parts[0]
	j, ok := s.Store.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch action {
	case "result":
		if j.Result == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "result not ready"})
			return
		}
		writeJSON(w, http.StatusOK, j.Result)
	case "audio":
		if j.AudioDeleted || j.AudioPath == "" {
			writeJSON(w, http.StatusGone, map[string]string{"error": "audio deleted"})
			return
		}
		w.Header().Set("Content-Type", contentType(j.AudioPath))
		http.ServeFile(w, r, j.AudioPath)
	default:
		writeJSON(w, http.StatusOK, j)
	}
}

func (s *Server) adminList(w http.ResponseWriter, r *http.Request) {
	limit := intQuery(r, "limit", 100)
	q := r.URL.Query().Get("q")
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":  s.Store.List(limit, q),
		"stats": s.Store.Stats(),
	})
}

const timelineHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>录不停 · 本机时间线</title>
<style>
:root{--bg:#f6f7f9;--card:#fff;--ink:#1c1e21;--sub:#6b7280;--line:#e5e7eb;--accent:#2563eb}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}
header{padding:22px 24px 8px;max-width:1040px;margin:0 auto}
h1{margin:0;font-size:20px}
h1 small{font-weight:400;color:var(--sub);font-size:13px;margin-left:8px}
.bar{max-width:1040px;margin:12px auto 0;padding:0 24px;display:flex;gap:10px;flex-wrap:wrap}
input{flex:1;min-width:240px;padding:10px 12px;border:1px solid var(--line);border-radius:10px;background:#fff;font-size:14px;outline:none}
input:focus{border-color:var(--accent)}
.stats{color:var(--sub);font-size:12px;padding:4px 2px}
main{display:grid;grid-template-columns:minmax(300px,420px) 1fr;gap:16px;max-width:1040px;margin:14px auto 40px;padding:0 24px}
@media (max-width:820px){main{grid-template-columns:1fr}}
#list{display:flex;flex-direction:column;gap:8px;max-height:calc(100vh - 190px);overflow:auto}
.item{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:11px 12px;cursor:pointer}
.item:hover{border-color:#c7d2fe}
.item.active{border-color:var(--accent);box-shadow:0 0 0 2px #dbeafe}
.item .head{display:flex;align-items:flex-start;gap:8px}
.item .body{flex:1;min-width:0}
.item input.pick{margin:3px 0 0;accent-color:var(--accent);flex:none}
.item .top{display:flex;justify-content:space-between;gap:8px;font-size:12px;color:var(--sub)}
.item .sum{margin-top:5px;font-size:13px;color:#111827;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden}
.tag{padding:1px 7px;border-radius:999px;background:#eef2ff;color:#3730a3;font-size:11px}
.tag.failed{background:#fef2f2;color:#991b1b}
.tag.merge{background:#ecfdf5;color:#065f46}
button{padding:9px 14px;border:1px solid var(--line);border-radius:10px;background:#fff;font-size:13px;cursor:pointer;flex:none}
button:disabled{opacity:.45;cursor:not-allowed}
button.primary{background:var(--accent);border-color:var(--accent);color:#fff}
.mode{display:flex;align-items:center;gap:6px;font-size:12px;color:var(--sub);white-space:nowrap}
.mode input{flex:none;min-width:0;width:16px;height:16px;padding:0;accent-color:var(--accent)}
#detail{background:var(--card);border:1px solid var(--line);border-radius:14px;padding:16px;min-height:220px}
#detail h2{margin:0 0 4px;font-size:16px}
.meta{color:var(--sub);font-size:12px;margin-bottom:10px}
audio{width:100%;margin:8px 0 14px}
.seg{display:grid;grid-template-columns:64px 70px 1fr;gap:8px;padding:6px 0;border-top:1px dashed var(--line);font-size:13px}
.seg .t{color:var(--sub);font-variant-numeric:tabular-nums}
.seg .s{color:#3730a3;font-size:12px}
.seg .src{color:#9ca3af;font-size:11px;margin-right:4px}
.empty{color:var(--sub);padding:20px 0}
.error{color:#991b1b;background:#fef2f2;border-radius:8px;padding:8px 10px;margin:8px 0;font-size:12px}
.devices{max-width:1040px;margin:12px auto 0;padding:12px 16px;background:var(--card);border:1px solid var(--line);border-radius:12px}
.devices .head{display:flex;align-items:center;gap:10px;flex-wrap:wrap}
.devices .head strong{font-size:14px}
.devrow{display:flex;align-items:center;gap:8px;flex-wrap:wrap;padding:7px 0;border-top:1px dashed var(--line);font-size:13px}
.devrow .grow{flex:1;min-width:140px}
#devbar{position:fixed;left:50%;bottom:20px;transform:translateX(-50%);z-index:20;display:none;background:#fff;border:1px solid var(--line);box-shadow:0 12px 32px rgba(0,0,0,.14);border-radius:14px;padding:14px 16px;width:min(420px,calc(100% - 32px))}
#devbar h3{margin:0 0 4px;font-size:15px}
#devbar .who{font-size:13px;color:var(--sub);margin:2px 0}
#devbar .row{display:flex;gap:8px;margin-top:10px}
</style>
</head>
<body>
<header>
  <h1>录不停 <small>本机时间线 · {{PC_NAME}} {{PC_FP}} · 只监听 127.0.0.1</small></h1>
</header>
<div class="bar">
  <input id="q" placeholder="搜索转写、摘要、设备名、文件名…" autocomplete="off">
  <button id="mergeBtn" class="primary" disabled>合并选中</button>
  <label class="mode"><input type="checkbox" id="audioMerge">统一说话人（重新识别）</label>
  <span class="stats" id="stats"></span>
</div>
<section class="devices">
  <div class="head">
    <strong>设备</strong>
    <button id="scanBtn">扫描附近手机</button>
    <span class="stats" id="devStats">手机和电脑连同一个 Wi-Fi，手机 App 在运行就能被扫到。</span>
  </div>
  <div id="nearby"></div>
</section>
<div id="devbar"></div>
<main>
  <div id="list"></div>
  <div id="detail"><div class="empty">选择左侧一段录音，查看摘要和逐句转写。</div></div>
</main>
<script>
const listEl = document.getElementById('list');
const detailEl = document.getElementById('detail');
const statsEl = document.getElementById('stats');
const qEl = document.getElementById('q');
const mergeBtn = document.getElementById('mergeBtn');
const audioMerge = document.getElementById('audioMerge');
let active = null;
let timer = null;
const picked = new Set();

function el(tag, cls, text){ const n=document.createElement(tag); if(cls)n.className=cls; if(text!=null)n.textContent=text; return n; }
function fmtTime(iso){ if(!iso) return ''; const d=new Date(iso); if(isNaN(d)) return iso; return d.toLocaleString('zh-CN',{hour12:false}); }
function dur(s){ s=Math.round(s||0); const m=Math.floor(s/60); return m+':'+String(s%60).padStart(2,'0'); }

function updateMergeBtn(){
  const n = picked.size;
  mergeBtn.disabled = n < 2;
  mergeBtn.textContent = n ? '合并选中（'+n+'）' : '合并选中';
}

async function doMerge(){
  if(picked.size < 2) return;
  mergeBtn.disabled = true;
  mergeBtn.textContent = '合并中…';
  try{
    const r = await fetch('/api/v1/admin/merge', {
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body: JSON.stringify({job_ids:[...picked], mode: audioMerge.checked ? 'audio' : 'transcript'})
    });
    const d = await r.json().catch(()=>({}));
    if(!r.ok){ alert('合并失败：' + (d.error || r.status)); return; }
    picked.clear();
    await load();
    show(d.job_id);
  } finally {
    updateMergeBtn();
  }
}
mergeBtn.onclick = doMerge;

async function load(){
  const q = qEl.value.trim();
  const r = await fetch('/api/v1/admin/jobs?limit=100&q='+encodeURIComponent(q));
  const d = await r.json();
  listEl.replaceChildren();
  if(d.stats){ statsEl.textContent = '共 '+d.stats.total+' 段 · 完成 '+(d.stats.done||0)+' · 失败 '+(d.stats.failed||0); }
  if(!d.jobs || !d.jobs.length){ listEl.append(el('div','empty','没有匹配的录音。')); updateMergeBtn(); return; }
  d.jobs.forEach(j=>{
    const item = el('div','item'+(active===j.id?' active':''));
    const head = el('div','head');
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.className = 'pick';
    cb.checked = picked.has(j.id);
    cb.disabled = j.kind==='merge' || j.status!=='done' || !j.result;
    cb.onclick = e=>e.stopPropagation();
    cb.onchange = ()=>{ if(cb.checked) picked.add(j.id); else picked.delete(j.id); updateMergeBtn(); };
    head.append(cb);

    const body = el('div','body');
    const top = el('div','top');
    top.append(el('span',null,fmtTime(j.recorded_at ? new Date(j.recorded_at).toISOString() : j.created_at)));
    const tags = el('span');
    if(j.kind==='merge') tags.append(el('span','tag merge', j.merge_mode==='audio'?'音频合并':'合并'));
    tags.append(el('span','tag'+(j.status==='failed'?' failed':''), j.status));
    top.append(tags);
    body.append(top);
    const r = j.result || {};
    const sum = r.summary || j.error || (j.status==='running'?'分析中…':'（无摘要）');
    body.append(el('div','sum', sum));
    head.append(body);
    item.append(head);
    item.onclick = ()=>show(j.id,item);
    listEl.append(item);
  });
  updateMergeBtn();
}

async function show(id,item){
  active = id;
  document.querySelectorAll('.item').forEach(n=>n.classList.remove('active'));
  if(item) item.classList.add('active');
  detailEl.replaceChildren(el('div','empty','加载中…'));
  const r = await fetch('/api/v1/admin/jobs/'+id);
  const j = await r.json();
  const res = j.result || {};
  const srcs = (res.sources && res.sources.length) ? res.sources : (j.source_spans || []);
  detailEl.replaceChildren();
  detailEl.append(el('h2',null,j.segment_name || j.id));
  const meta = el('div','meta');
  const when = j.recorded_at ? new Date(j.recorded_at).toISOString() : j.created_at;
  const bits = [j.device_name||j.device_id||'未知设备', fmtTime(when)];
  if(j.kind==='merge') bits.push((j.merge_mode==='audio'?'音频合并':'转写合并')+' '+(j.source_job_ids||[]).length+' 段');
  bits.push('时长 '+dur(res.duration), '状态 '+j.status);
  meta.textContent = bits.join(' · ');
  detailEl.append(meta);
  if(j.error) detailEl.append(el('div','error',j.error));
  if(!j.audio_deleted && j.status!=='failed' || res.duration){
    const a = document.createElement('audio'); a.controls=true; a.preload='none';
    a.src = '/api/v1/admin/jobs/'+id+'/audio';
    detailEl.append(a);
  }
  if(j.kind==='merge' && srcs.length){
    detailEl.append(el('h2',null,'来源分段'));
    srcs.forEach(s=>{
      const row = el('div','seg');
      row.title = s.segment_name || s.job_id || '';
      row.append(el('span','t',dur(s.offset)));
      row.append(el('span','s',''));
      row.append(el('span',null,srcLabel(s)+'  '+dur(s.duration)));
      detailEl.append(row);
    });
  }
  if(res.summary){ detailEl.append(el('h2',null,'摘要')); detailEl.append(el('div',null,res.summary)); }
  if(j.gaps){
    try{
      const gaps = JSON.parse(j.gaps);
      if(Array.isArray(gaps) && gaps.length){
        detailEl.append(el('h2',null,'空档 / 异常'));
        const map = {gap_start:'通话/暂停开始',gap_end:'恢复录音',low_level_start:'音量异常开始',low_level_end:'音量恢复',pause_timeout:'暂停超时，强制恢复'};
        gaps.forEach(g=>{
          const row = el('div','seg');
          row.append(el('span','t', fmtTime(new Date(g.ts).toISOString())));
          row.append(el('span','s',''));
          row.append(el('span',null, map[g.type] || g.type));
          detailEl.append(row);
        });
      }
    }catch(e){}
  }
  if(res.segments && res.segments.length){
    detailEl.append(el('h2',null,'逐句转写'));
    res.segments.forEach(s=>{
      const row = el('div','seg');
      row.append(el('span','t',dur(s.start)+'–'+dur(s.end)));
      row.append(el('span','s',s.speaker||''));
      const txt = el('span');
      const src = j.kind==='merge' ? srcAt(srcs, s.start) : '';
      if(src) txt.append(el('span','src','['+src+'] '));
      txt.append(document.createTextNode(s.text||''));
      row.append(txt);
      detailEl.append(row);
    });
  }
}

function srcAt(srcs, t){
  if(!srcs || !srcs.length) return '';
  let hit = srcs[0];
  for(const s of srcs){ if(s.offset <= t) hit = s; }
  return hit ? srcLabel(hit) : '';
}

function srcLabel(s){
  const n = typeof s === 'string' ? s : (s.segment_name || s.job_id || '');
  const m = n.match(/rec_\d{8}_(\d{2})(\d{2})(\d{2})(?:_\d+)?\./);
  if(m) return m[1]+':'+m[2]+':'+m[3];
  return n;
}

qEl.addEventListener('input', ()=>{ clearTimeout(timer); timer=setTimeout(load,250); });
load();

// ---------- 设备发现 / 连接确认 / 同步 ----------

const nearbyEl = document.getElementById('nearby');
const devStats = document.getElementById('devStats');
const devbar = document.getElementById('devbar');
const scanBtn = document.getElementById('scanBtn');
let scanning = false;

function fmtStamp(iso){ const d = new Date(iso); return isNaN(d) ? '' : d.toLocaleTimeString('zh-CN',{hour12:false}); }

function renderNearby(d){
  const list = (d && d.devices) || [];
  nearbyEl.replaceChildren();
  if(!list.length){
    devStats.textContent = '没发现手机。确认同一个 Wi-Fi，且手机端 App / 录音服务在运行。';
    return;
  }
  devStats.textContent = '发现 ' + list.length + ' 台手机';
  list.forEach(x=>{
    const row = el('div','devrow');
    const info = el('div','grow');
    info.append(el('span',null,'📱 '+(x.name || '手机')));
    if(x.fp) info.append(el('span','stats','  '+x.fp));
    row.append(info);
    if(x.paired){
      row.append(el('span','tag','已连接'));
      const b = el('button','primary','同步');
      b.onclick = async ()=>{
        b.disabled = true; b.textContent = '已通知';
        try{
          const r = await fetch('/api/v1/admin/sync',{
            method:'POST', headers:{'Content-Type':'application/json'},
            body: JSON.stringify({device_id:x.id})
          });
          if(!r.ok){
            const e = await r.json().catch(()=>({}));
            alert('同步失败：' + (e.error || r.status));
          }
        }catch(err){ alert('同步失败：' + err.message); }
        setTimeout(()=>{ b.textContent='同步'; b.disabled=false; }, 4000);
      };
      row.append(b);
    }else{
      row.append(el('span','tag failed','未连接'));
      const b = el('button','primary','连接');
      b.onclick = async ()=>{
        b.disabled = true; b.textContent = '已发送';
        try{
          const r = await fetch('/api/v1/admin/invite',{
            method:'POST', headers:{'Content-Type':'application/json'},
            body: JSON.stringify({device_id:x.id, host:x.host})
          });
          if(!r.ok){
            const e = await r.json().catch(()=>({}));
            alert('发送失败：' + (e.error || r.status));
          }
        }catch(err){ alert('发送失败：' + err.message); }
        setTimeout(()=>{ b.textContent='连接'; b.disabled=false; }, 5000);
      };
      row.append(b);
      row.append(el('span','stats','点「连接」，在手机上点允许'));
    }
    nearbyEl.append(row);
  });
}

scanBtn.onclick = async ()=>{
  if(scanning) return;
  scanning = true; scanBtn.disabled = true; scanBtn.textContent = '扫描中…';
  devStats.textContent = '正在向局域网广播…';
  try{
    const r = await fetch('/api/v1/admin/nearby');
    renderNearby(await r.json());
  }catch(e){ devStats.textContent = '扫描失败：' + e.message; }
  scanning = false; scanBtn.disabled = false; scanBtn.textContent = '扫描附近手机';
};

async function pollJoin(){
  try{
    const r = await fetch('/api/v1/admin/join');
    if(!r.ok) return;
    const d = await r.json();
    const req = (d.requests || [])[0];
    if(!req){ devbar.style.display = 'none'; return; }
    devbar.style.display = 'block';
    devbar.replaceChildren();
    devbar.append(el('h3',null,'有手机想连接'));
    devbar.append(el('div','who',(req.name || '手机') + (req.fp ? ' · ' + req.fp : '') + ' · ' + fmtStamp(req.created_at)));
    devbar.append(el('div','stats','确认是这台手机，再点允许。允许后它会自动同步录音。'));
    const row = el('div','row');
    const ok = el('button','primary','允许');
    const no = el('button',null,'拒绝');
    ok.onclick = async ()=>{ devbar.style.display='none'; await fetch('/api/v1/admin/join/'+encodeURIComponent(req.id)+'/approve',{method:'POST'}); };
    no.onclick = async ()=>{ devbar.style.display='none'; await fetch('/api/v1/admin/join/'+encodeURIComponent(req.id)+'/reject',{method:'POST'}); };
    row.append(ok,no);
    devbar.append(row);
  }catch(e){}
}
setInterval(pollJoin, 3000);
pollJoin();
</script>
</body>
</html>`
