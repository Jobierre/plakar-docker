package procmon

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sync"
)

// ===== SSE hub and HTTP server =====

type sseMsg struct {
	Event string
	Data  any
}

type sseHub struct {
	mu         sync.RWMutex
	register   chan chan sseMsg
	unregister chan chan sseMsg
	clients    map[chan sseMsg]struct{}
	closed     chan struct{}
}

func newHub() *sseHub {
	return &sseHub{
		register:   make(chan chan sseMsg),
		unregister: make(chan chan sseMsg),
		clients:    make(map[chan sseMsg]struct{}),
		closed:     make(chan struct{}),
	}
}

func (h *sseHub) run() {
	for {
		select {
		case ch := <-h.register:
			h.clients[ch] = struct{}{}
		case ch := <-h.unregister:
			delete(h.clients, ch)
			close(ch)
		case <-h.closed:
			for ch := range h.clients {
				close(ch)
				delete(h.clients, ch)
			}
			return
		}
	}
}

func (h *sseHub) broadcast(ev string, data any) {
	msg := sseMsg{Event: ev, Data: data}
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
			// slow or stuck client — drop
		}
	}
}

var hub *sseHub // set when StartHTTP is called

// StartHTTP starts the live UI and SSE on addr (e.g. ":8080").
// maxConc is typically ctx.MaxConcurrency (printed in the UI).
// It returns stopHTTP() that gracefully shuts the server down.
func StartHTTP(ctx context.Context, addr, base, title string, maxConc int) (func(context.Context) error, error) {
	hub = newHub()
	go hub.run()

	mux := http.NewServeMux()

	// UI at "/"
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(minUIHTML(base, title, maxConc)))
	})

	// Samples
	mux.HandleFunc("/samples", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Samples())
	})

	// Markers
	mux.HandleFunc("/marks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Markers())
	})

	// SSE stream
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		ch := make(chan sseMsg, 16)
		hub.register <- ch
		defer func() { hub.unregister <- ch }()

		// initial hello
		_, _ = w.Write([]byte("event: hello\ndata: {\"ok\":true}\n\n"))
		flusher.Flush()

		notify := r.Context().Done()
		for {
			select {
			case msg := <-ch:
				b, _ := json.Marshal(msg.Data)
				_, _ = w.Write([]byte("event: " + msg.Event + "\n"))
				_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
				flusher.Flush()
			case <-notify:
				return
			}
		}
	})

	srv := &http.Server{Addr: addr, Handler: mux}

	// start
	go func() { _ = srv.ListenAndServe() }()

	// stop func
	stop := func(ctx context.Context) error {
		close(hub.closed)
		return srv.Shutdown(ctx)
	}
	return stop, nil
}

func minUIHTML(base, title string, maxConc int) string {
	tHTML := html.EscapeString(title)
	return `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"/><meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>` + tHTML + `</title>
<style>
 body{font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;margin:16px}
 .chartWrap{width:100%;height:320px;margin-top:8px}
 canvas{width:100%!important;height:100%!important;display:block}
 .row{display:flex;gap:8px;align-items:center;margin-bottom:8px;flex-wrap:wrap}
 .badge{display:inline-block;padding:4px 8px;border-radius:999px;background:#eee;font-variant-numeric:tabular-nums}
 .section{margin-top:6px}
 .hline{height:1px;background:#eee;margin:16px 0}
 .muted{opacity:.7}
 .link{color:#1976d2; cursor:pointer; text-decoration:underline}
</style>
</head><body>
<h2>` + tHTML + `</h2>

<div class="row section">
  <span class="badge" id="status">connecting…</span>
  <span class="badge">Max concurrency: <span id="maxc"></span></span>
  <span class="badge muted" id="freezeNote" style="display:none;">frozen after resume — <span id="resumeLink" class="link">resume live</span></span>
</div>

<div class="hline"></div>
<h3>CPU % <span class="muted">(min/max/avg/med lines shown)</span></h3>
<div class="chartWrap"><canvas id="cpuChart"></canvas></div>
<h3>RAM (MiB) <span class="muted">(min/max/avg/med lines shown)</span></h3>
<div class="chartWrap"><canvas id="ramChart"></canvas></div>
<h3>Goroutines <span class="muted">(min/max/avg/med lines shown)</span></h3>
<div class="chartWrap"><canvas id="grChart"></canvas></div>

<script>const BASE='` + base + `', MAXC=` + fmt.Sprint(maxConc) + `;</script>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.4/dist/chart.umd.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/chartjs-plugin-annotation@3.0.1/dist/chartjs-plugin-annotation.min.js"></script>
<script>
Chart.register(window['chartjs-plugin-annotation']);

// ---- single-metric line chart factory with explicit colors ----
function mkLineChart(ctx, yTitle, color){
  return new Chart(ctx, {
    type:'line',
    data:{datasets:[{
      label:yTitle, data:[], parsing:false, pointRadius:0, tension:0,
      borderColor: color, backgroundColor: color
    }]},
    options:{
      responsive:true, maintainAspectRatio:false, animation:false,
      layout:{ padding:{top:24} },
      plugins:{ legend:{display:false}, annotation:{ annotations:{} } },
      scales:{
        x:{type:'linear', title:{display:true,text:'Elapsed (s)'}},
        y:{title:{display:true,text:yTitle}}
      },
      interaction:{mode:'nearest', intersect:false}
    }
  });
}

// main series colors (distinct across charts)
const cpuChart = mkLineChart(document.getElementById('cpuChart').getContext('2d'), 'CPU %',      '#1976d2'); // blue
const ramChart = mkLineChart(document.getElementById('ramChart').getContext('2d'), 'MiB',        '#2e7d32'); // green
const grChart  = mkLineChart(document.getElementById('grChart').getContext('2d'),  'Goroutines', '#ef6c00'); // orange

// ----- state -----
const state = { cpu: [], ram: [], gr: [] };

// stat line palette (distinct per type)
const STAT_COLORS = {
  min: 'rgba(33,150,243,0.35)',   // blue-ish
  max: 'rgba(244,67,54,0.35)',    // red-ish
  avg: 'rgba(76,175,80,0.35)',    // green-ish
  med: 'rgba(156,39,176,0.35)'    // purple-ish
};

// helpers
function format1(n){ return (Math.round(n*10)/10).toFixed(1); }
function computeStats(arr){
  if(arr.length===0) return null;
  let sum=0, min=arr[0], max=arr[0];
  for(const v of arr){ sum+=v; if(v<min) min=v; if(v>max) max=v; }
  const avg = sum/arr.length;
  const tmp = arr.slice().sort((a,b)=>a-b);
  const mid = Math.floor(tmp.length/2);
  const med = (tmp.length%2)? tmp[mid] : (tmp[mid-1]+tmp[mid])/2;
  return {min, max, avg, med};
}
function hline(y, color, label){
  return {
    type:'line', yMin:y, yMax:y,
    borderColor: color, borderWidth: 1, borderDash:[4,4],
    label: {
      display: true, content: label, position: 'start',
      backgroundColor:'rgba(0,0,0,0.35)', color:'#fff',
      padding: 2, borderRadius: 3, font:{ size: 10 }
    },
    drawTime: 'afterDatasetsDraw'
  };
}
function applyAnns(chart, anns){ chart.options.plugins.annotation.annotations = anns; }

// per-chart annotation sets
let statAnns = { cpu:{}, ram:{}, gr:{} };
let markerAnns = {}; // same markers applied to all charts

function updateStatLines(){
  const c = computeStats(state.cpu);
  if(c){
    statAnns.cpu = {
      cpu_min: hline(c.min, STAT_COLORS.min, 'min ' + format1(c.min)),
      cpu_max: hline(c.max, STAT_COLORS.max, 'max ' + format1(c.max)),
      cpu_avg: hline(c.avg, STAT_COLORS.avg, 'avg ' + format1(c.avg)),
      cpu_med: hline(c.med, STAT_COLORS.med, 'med ' + format1(c.med)),
    };
    applyAnns(cpuChart, {...statAnns.cpu, ...markerAnns});
  }
  const r = computeStats(state.ram);
  if(r){
    statAnns.ram = {
      ram_min: hline(r.min, STAT_COLORS.min, 'min ' + format1(r.min)),
      ram_max: hline(r.max, STAT_COLORS.max, 'max ' + format1(r.max)),
      ram_avg: hline(r.avg, STAT_COLORS.avg, 'avg ' + format1(r.avg)),
      ram_med: hline(r.med, STAT_COLORS.med, 'med ' + format1(r.med)),
    };
    applyAnns(ramChart, {...statAnns.ram, ...markerAnns});
  }
  const g = computeStats(state.gr);
  if(g){
    statAnns.gr = {
      gr_min: hline(g.min, STAT_COLORS.min, 'min ' + Math.round(g.min)),
      gr_max: hline(g.max, STAT_COLORS.max, 'max ' + Math.round(g.max)),
      gr_avg: hline(g.avg, STAT_COLORS.avg, 'avg ' + format1(g.avg)),
      gr_med: hline(g.med, STAT_COLORS.med, 'med ' + format1(g.med)),
    };
    applyAnns(grChart, {...statAnns.gr, ...markerAnns});
  }
}

function updateStatLinesAndDraw(){
  updateStatLines();
  cpuChart.update(); ramChart.update(); grChart.update();
}

// ----- samples / markers -----
let t0=null;
const statusEl = document.getElementById('status');
const maxcEl = document.getElementById('maxc');
const freezeNote = document.getElementById('freezeNote');
const resumeLink = document.getElementById('resumeLink');
maxcEl.textContent = String(MAXC);

let connectedOnce = false;
let frozen = false; // when true, ignore live updates (post-resume)

resumeLink?.addEventListener('click', ()=>{
  frozen = false;
  freezeNote.style.display = 'none';
  statusEl.textContent = 'live';
});

function addSample(s){
  if(frozen) return;
  const ts = new Date(s.TS).getTime();
  if(t0===null) t0 = ts;
  const x  = (ts - t0)/1000;
  const cpu = s.CPUPercent;
  const mib = s.RSSBytes/1048576;
  const gr  = s.Goroutines ?? s.goroutines ?? 0;

  cpuChart.data.datasets[0].data.push({x:x,y:cpu});
  ramChart.data.datasets[0].data.push({x:x,y:mib});
  grChart.data.datasets[0].data.push({x:x,y:gr});

  state.cpu.push(cpu);
  state.ram.push(mib);
  state.gr.push(gr);
}

const markers = []; // {id,label,x,color}
function addMarker(m){
  if(frozen) return;
  const ts = new Date(m.TS || m.time).getTime();
  if(t0===null) return; // wait until we know t0
  const x = (ts - t0)/1000;
  const color = (m.Color || m.color || '#ff6f00');
  markers.push({id: (m.id || markers.length+1), label: m.Label || m.label || '', x, color});
  redrawMarkers();
}

function redrawMarkers(){
  const anns = {};
  const scale = cpuChart.scales.x; // use CPU scale for overlap calc (aligned axes)
  const minDx = 12, lineW = 1, baseYOffset = -12, step = 14;

  const entries = markers.map(m => ({...m, pixelX: scale.getPixelForValue(m.x)}))
                         .sort((a,b)=>a.pixelX-b.pixelX);

  const layers = [];
  for(const e of entries){
    let placed=false;
    for(const stack of layers){
      const last = stack[stack.length-1];
      if(Math.abs(e.pixelX - last.pixelX) <= minDx){ stack.push(e); placed=true; break; }
    }
    if(!placed) layers.push([e]);
  }
  let side = 1;
  for(const stack of layers){
    stack.forEach((e, i) => {
      const yAdjust = baseYOffset - i*step * side;
      anns['m_'+e.id] = {
        type: 'line',
        xMin: e.x, xMax: e.x,
        borderColor: e.color, borderWidth: lineW,
        label: {
          display: !!e.label,
          content: '• ' + e.label,
          position: 'start',
          yAdjust: yAdjust,
          backgroundColor: 'rgba(0,0,0,0.6)',
          color: '#fff', padding: 2, borderRadius: 3, font: { size: 10 }
        },
        drawTime: 'afterDatasetsDraw'
      };
    });
    side *= -1;
  }
  markerAnns = anns;
  // merge markers with stat lines on all three charts
  applyAnns(cpuChart, {...statAnns.cpu, ...markerAnns});
  applyAnns(ramChart, {...statAnns.ram, ...markerAnns});
  applyAnns(grChart,  {...statAnns.gr,  ...markerAnns});
}

// ----- initial load -----
fetch(BASE + '/samples').then(r=>{
  if(!r.ok) throw new Error('HTTP '+r.status);
  return r.json();
}).then(arr=>{
  if(arr.length>0){ t0 = new Date(arr[0].TS).getTime(); }
  arr.forEach(addSample);
  updateStatLinesAndDraw();
}).catch(err=>{
  statusEl.textContent = 'history error';
  console.error('samples fetch failed:', err);
});

fetch(BASE + '/marks').then(r=>r.json()).then(ms=>{
  ms.forEach(addMarker);
  updateStatLinesAndDraw();
});

// ----- live -----
const es = new EventSource(BASE + '/events');
es.onopen = () => {
  if(connectedOnce){
    // reconnected after a drop/resume; freeze further live updates
    frozen = true;
    freezeNote.style.display = '';
    statusEl.textContent = 'frozen';
  } else {
    statusEl.textContent = 'live';
    connectedOnce = true;
  }
};
es.onerror = () => statusEl.textContent = 'disconnected';

es.addEventListener('sample', ev=>{
  addSample(JSON.parse(ev.data));
  updateStatLinesAndDraw();
});

es.addEventListener('mark', ev=>{
  addMarker(JSON.parse(ev.data));
  updateStatLinesAndDraw();
});
</script>
</body></html>`
}
