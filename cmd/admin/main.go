package main

import (
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"p2c-sniper/internal/db"
)

type chartPoint struct {
	Label   string
	Count   int
	Volume  float64
	BarY    float64 // top y-coord (0..100, smaller = taller)
	BarH    float64 // height (0..100)
	ShowLbl bool    // whether to render this label below the chart
	X       int     // x-coord (column index)
}

type dashboardView struct {
	Now            string
	Totals         db.DashboardTotals
	Users          []db.DashboardUserStat
	Recent         []db.RecentOrder
	Hourly         []chartPoint
	Hourly24hTotal float64
	Hourly24hCount int
	Attempts       int
	AvgOrder       float64
	SuccessRate    float64
	TopUsers       []db.DashboardUserStat
	PeakHourLabel  string
	PeakHourCount  int
}

func main() {
	_ = godotenv.Load()

	dbPath := strings.TrimSpace(os.Getenv("DB_PATH"))
	if dbPath == "" {
		dbPath = "bot_users.db"
	}

	adminUser := strings.TrimSpace(os.Getenv("ADMIN_WEB_USER"))
	adminPass := strings.TrimSpace(os.Getenv("ADMIN_WEB_PASS"))
	if adminUser == "" || adminPass == "" {
		log.Fatal("ADMIN_WEB_USER and ADMIN_WEB_PASS are required")
	}

	addr := strings.TrimSpace(os.Getenv("ADMIN_WEB_ADDR"))
	if addr == "" {
		addr = ":8080"
	}

	maxRows := intFromEnv("ADMIN_WEB_MAX_USERS", 500)

	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	tmpl := template.Must(template.New("dashboard").Funcs(template.FuncMap{
		"fmtAmount": func(v float64) string { return formatAmount(v) },
		"fmtPct":    func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) },
		"fmtTime":   func(s string) string { return fmtTime(s) },
		"statusCls": func(s string) string {
			switch s {
			case "completed":
				return "ok"
			case "canceled":
				return "bad"
			case "sniped":
				return "info"
			default:
				return "off"
			}
		},
	}).Parse(dashboardHTML))

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		totals, err := database.GetDashboardTotals()
		if err != nil {
			http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		users, err := database.GetDashboardUserStats(maxRows)
		if err != nil {
			http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		recent, err := database.GetRecentOrders(15)
		if err != nil {
			http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		hourly, err := database.GetHourlyActivity()
		if err != nil {
			http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		var totalVol float64
		var totalCnt int
		var peakCnt int
		var peakLabel string
		for _, h := range hourly {
			totalVol += h.Volume
			totalCnt += h.Count
			if h.Count > peakCnt {
				peakCnt = h.Count
				peakLabel = h.Hour
			}
		}
		if peakLabel == "" {
			peakLabel = "—"
		}
		chart := make([]chartPoint, 0, len(hourly))
		for i, h := range hourly {
			var barH float64
			if peakCnt > 0 && h.Count > 0 {
				barH = float64(h.Count) / float64(peakCnt) * 90
				if barH < 3 {
					barH = 3
				}
			}
			chart = append(chart, chartPoint{
				Label:   h.Hour,
				Count:   h.Count,
				Volume:  h.Volume,
				BarY:    100 - barH,
				BarH:    barH,
				ShowLbl: i == 0 || i == 6 || i == 12 || i == 18 || i == 23,
				X:       i,
			})
		}

		var avgOrder, successRate float64
		attempts := totals.OrdersSniped + totals.OrdersMissed
		if totals.OrdersSniped > 0 {
			avgOrder = totals.VolumeAllTime / float64(totals.OrdersSniped)
		}
		if attempts > 0 {
			successRate = float64(totals.OrdersSniped) / float64(attempts) * 100
		}

		top := users
		if len(top) > 5 {
			top = top[:5]
		}

		view := dashboardView{
			Now:            time.Now().Format("02.01.2006 15:04:05"),
			Totals:         totals,
			Users:          users,
			Recent:         recent,
			Hourly:         chart,
			Hourly24hTotal: totalVol,
			Hourly24hCount: totalCnt,
			Attempts:       attempts,
			AvgOrder:       avgOrder,
			SuccessRate:    successRate,
			TopUsers:       top,
			PeakHourLabel:  peakLabel,
			PeakHourCount:  peakCnt,
		}
		if err := tmpl.Execute(w, view); err != nil {
			http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
			return
		}
	})

	log.Printf("admin dashboard listening on %s", addr)
	if err := http.ListenAndServe(addr, basicAuth(adminUser, adminPass, mux)); err != nil {
		log.Fatal(err)
	}
}

func basicAuth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="admin"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func intFromEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return fallback
	}
	return v
}

func formatAmount(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	if strings.HasSuffix(s, ".00") {
		s = s[:len(s)-3]
	}
	// thousands separator
	dot := strings.Index(s, ".")
	intPart := s
	frac := ""
	if dot >= 0 {
		intPart = s[:dot]
		frac = s[dot:]
	}
	neg := strings.HasPrefix(intPart, "-")
	if neg {
		intPart = intPart[1:]
	}
	n := len(intPart)
	if n <= 3 {
		if neg {
			return "-" + intPart + frac
		}
		return intPart + frac
	}
	var b strings.Builder
	pre := n % 3
	if pre > 0 {
		b.WriteString(intPart[:pre])
		if n > pre {
			b.WriteByte(' ')
		}
	}
	for i := pre; i < n; i += 3 {
		b.WriteString(intPart[i : i+3])
		if i+3 < n {
			b.WriteByte(' ')
		}
	}
	if neg {
		return "-" + b.String() + frac
	}
	return b.String() + frac
}

// fmtTime converts stored UTC timestamps to Moscow local for display.
func fmtTime(s string) string {
	if s == "" {
		return "—"
	}
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	}
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		loc = time.FixedZone("MSK", 3*3600)
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return t.In(loc).Format("02.01 15:04")
		}
	}
	return s
}

const dashboardHTML = `<!doctype html>
<html lang="ru">
<head>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<title>P2C Sniper — Admin Dashboard</title>
	<meta http-equiv="refresh" content="15">
	<link rel="preconnect" href="https://fonts.googleapis.com">
	<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
	<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;600&display=swap" rel="stylesheet">
	<style>
		:root {
			--bg: #0b0f1a;
			--bg-2: #111728;
			--panel: #151c2e;
			--panel-2: #1b2338;
			--border: #232c46;
			--text: #e7ebf5;
			--muted: #8893b2;
			--accent: #7c9cff;
			--accent-2: #a87bff;
			--ok: #22c55e;
			--bad: #ef4444;
			--warn: #f59e0b;
			--info: #38bdf8;
			--shadow: 0 8px 30px rgba(0,0,0,.35);
		}
		* { box-sizing: border-box; }
		html, body { background: var(--bg); color: var(--text); }
		body {
			font-family: 'Inter', system-ui, -apple-system, sans-serif;
			margin: 0;
			font-size: 14px;
			line-height: 1.5;
			background:
				radial-gradient(1100px 600px at 10% -10%, rgba(124,156,255,.15), transparent 60%),
				radial-gradient(900px 500px at 100% 0%, rgba(168,123,255,.12), transparent 60%),
				var(--bg);
			min-height: 100vh;
		}
		.wrap { max-width: 1400px; margin: 0 auto; padding: 28px 24px 60px; }
		header {
			display: flex; align-items: center; justify-content: space-between;
			margin-bottom: 24px; flex-wrap: wrap; gap: 12px;
		}
		.brand { display: flex; align-items: center; gap: 12px; }
		.logo {
			width: 40px; height: 40px; border-radius: 10px;
			background: linear-gradient(135deg, var(--accent), var(--accent-2));
			display: grid; place-items: center; font-weight: 800; color: #0b0f1a;
			box-shadow: var(--shadow);
			font-size: 18px;
		}
		h1 { font-size: 20px; margin: 0; font-weight: 700; letter-spacing: -.01em; }
		.subtitle { color: var(--muted); font-size: 12px; margin-top: 2px; }
		.meta { display: flex; align-items: center; gap: 10px; color: var(--muted); font-size: 12px; }
		.meta .dot { width: 8px; height: 8px; background: var(--ok); border-radius: 50%; box-shadow: 0 0 0 4px rgba(34,197,94,.2); animation: pulse 2s infinite; }
		@keyframes pulse { 0%,100% { box-shadow: 0 0 0 4px rgba(34,197,94,.2); } 50% { box-shadow: 0 0 0 8px rgba(34,197,94,.05); } }

		.grid { display: grid; gap: 14px; }
		.kpi { grid-template-columns: repeat(4, 1fr); }
		.kpi-2 { grid-template-columns: repeat(4, 1fr); }
		@media (max-width: 1000px) { .kpi, .kpi-2 { grid-template-columns: repeat(2, 1fr); } }
		@media (max-width: 560px) { .kpi, .kpi-2 { grid-template-columns: 1fr; } }

		.card {
			background: linear-gradient(180deg, var(--panel), var(--panel-2));
			border: 1px solid var(--border);
			border-radius: 14px;
			padding: 16px 18px;
			position: relative;
			overflow: hidden;
			box-shadow: var(--shadow);
		}
		.card::before {
			content: '';
			position: absolute; inset: 0 auto auto 0; width: 100%; height: 2px;
			background: linear-gradient(90deg, transparent, var(--accent), var(--accent-2), transparent);
			opacity: .3;
		}
		.card .lbl {
			font-size: 11px; text-transform: uppercase; letter-spacing: .08em;
			color: var(--muted); font-weight: 600; display: flex; align-items: center; gap: 8px;
		}
		.card .val { font-size: 26px; font-weight: 800; margin-top: 6px; letter-spacing: -.02em; }
		.card .sub { color: var(--muted); font-size: 12px; margin-top: 4px; }
		.card .ico {
			width: 22px; height: 22px; border-radius: 6px;
			background: rgba(124,156,255,.15);
			display: inline-grid; place-items: center; color: var(--accent);
			font-size: 12px;
		}
		.card.accent::before { opacity: .7; }
		.pos { color: var(--ok); }
		.neg { color: var(--bad); }

		section {
			background: linear-gradient(180deg, var(--panel), var(--panel-2));
			border: 1px solid var(--border);
			border-radius: 14px;
			padding: 18px 20px;
			margin-top: 18px;
			box-shadow: var(--shadow);
		}
		section h2 {
			margin: 0 0 14px;
			font-size: 14px; text-transform: uppercase; letter-spacing: .08em;
			color: var(--muted); font-weight: 700;
			display: flex; align-items: center; gap: 8px;
		}
		section h2 .badge {
			background: rgba(124,156,255,.12); color: var(--accent);
			padding: 2px 8px; border-radius: 20px; font-size: 11px; letter-spacing: 0;
			text-transform: none;
		}

		.cols { display: grid; grid-template-columns: 2fr 1fr; gap: 18px; }
		@media (max-width: 1000px) { .cols { grid-template-columns: 1fr; } }

		.chart-wrap { padding: 8px 4px 4px; }
		.chart-meta {
			display: flex; justify-content: space-between; flex-wrap: wrap;
			gap: 12px; margin-bottom: 14px; color: var(--muted); font-size: 12px;
		}
		.chart-meta b { color: var(--text); font-weight: 700; }
		.chart-svg { width: 100%; height: 180px; display: block; }

		.feed { display: flex; flex-direction: column; gap: 10px; max-height: 340px; overflow: auto; }
		.feed-item {
			display: grid; grid-template-columns: auto 1fr auto; gap: 10px; align-items: center;
			padding: 10px 12px; background: rgba(255,255,255,.02);
			border: 1px solid var(--border); border-radius: 10px;
		}
		.feed-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--muted); }
		.feed-dot.ok { background: var(--ok); }
		.feed-dot.bad { background: var(--bad); }
		.feed-dot.info { background: var(--info); }
		.feed-info .fname { font-weight: 600; font-size: 13px; }
		.feed-info .fmeta { color: var(--muted); font-size: 11px; font-family: 'JetBrains Mono', monospace; margin-top: 2px; }
		.feed-amt { font-weight: 700; font-size: 14px; text-align: right; }
		.feed-amt .cur { color: var(--muted); font-weight: 500; font-size: 11px; margin-left: 4px; }

		table { width: 100%; border-collapse: collapse; }
		th, td { padding: 10px 12px; text-align: left; font-size: 13px; border-bottom: 1px solid var(--border); }
		th {
			color: var(--muted); font-weight: 600; font-size: 11px;
			text-transform: uppercase; letter-spacing: .06em;
			background: rgba(255,255,255,.02);
			position: sticky; top: 0;
		}
		tbody tr:hover { background: rgba(124,156,255,.05); }
		td.mono { font-family: 'JetBrains Mono', monospace; color: var(--muted); font-size: 12px; }
		td.num { text-align: right; font-variant-numeric: tabular-nums; }

		.tag {
			display: inline-flex; align-items: center; gap: 4px;
			padding: 3px 9px; border-radius: 20px; font-size: 11px; font-weight: 600;
			text-transform: uppercase; letter-spacing: .04em;
		}
		.tag.ok { background: rgba(34,197,94,.12); color: var(--ok); }
		.tag.off { background: rgba(136,147,178,.12); color: var(--muted); }
		.tag.bad { background: rgba(239,68,68,.12); color: var(--bad); }
		.tag.info { background: rgba(56,189,248,.12); color: var(--info); }
		.tag.warn { background: rgba(245,158,11,.12); color: var(--warn); }
		.tag .pulse { width: 6px; height: 6px; border-radius: 50%; background: currentColor; animation: tagpulse 1.6s infinite; }
		@keyframes tagpulse { 0%,100% { opacity: 1; } 50% { opacity: .3; } }

		.tbl-wrap { max-height: 600px; overflow: auto; border: 1px solid var(--border); border-radius: 12px; }

		.top-list { display: flex; flex-direction: column; gap: 10px; }
		.top-item {
			display: grid; grid-template-columns: 28px 1fr auto; align-items: center; gap: 10px;
			padding: 10px; background: rgba(255,255,255,.02); border: 1px solid var(--border); border-radius: 10px;
		}
		.top-rank {
			width: 28px; height: 28px; border-radius: 8px; display: grid; place-items: center;
			font-weight: 800; font-size: 13px;
			background: rgba(124,156,255,.15); color: var(--accent);
		}
		.top-item:nth-child(1) .top-rank { background: linear-gradient(135deg, #f1c40f, #e67e22); color: #1a1205; }
		.top-item:nth-child(2) .top-rank { background: linear-gradient(135deg, #bdc3c7, #95a5a6); color: #1a1a1a; }
		.top-item:nth-child(3) .top-rank { background: linear-gradient(135deg, #cd7f32, #8b4513); color: #fff; }
		.top-name { font-weight: 600; font-size: 13px; }
		.top-meta { color: var(--muted); font-size: 11px; font-family: 'JetBrains Mono', monospace; }
		.top-vol { font-weight: 700; font-size: 14px; text-align: right; }

		.empty { color: var(--muted); text-align: center; padding: 30px; font-size: 13px; }

		footer { color: var(--muted); font-size: 11px; text-align: center; margin-top: 30px; }
	</style>
</head>
<body>
<div class="wrap">
	<header>
		<div class="brand">
			<div class="logo">P2C</div>
			<div>
				<h1>Sniper Admin Dashboard</h1>
				<div class="subtitle">Realtime monitoring · P2P order capture</div>
			</div>
		</div>
		<div class="meta">
			<span class="dot"></span>
			<span>Live · обновлено {{.Now}}</span>
		</div>
	</header>

	<div class="grid kpi">
		<div class="card accent">
			<div class="lbl"><span class="ico">◆</span> Всего пользователей</div>
			<div class="val">{{.Totals.TotalUsers}}</div>
			<div class="sub">{{.Totals.UsersWithToken}} с токеном · {{.Totals.RunningUsers}} активных</div>
		</div>
		<div class="card accent">
			<div class="lbl"><span class="ico">▶</span> В работе сейчас</div>
			<div class="val pos">{{.Totals.RunningUsers}}</div>
			<div class="sub">из {{.Totals.UsersWithToken}} с подключённым токеном</div>
		</div>
		<div class="card accent">
			<div class="lbl"><span class="ico">₽</span> Объём за 24 часа</div>
			<div class="val">{{fmtAmount .Totals.Volume24h}}<span class="sub" style="display:inline; margin-left:6px;">₽</span></div>
			<div class="sub">{{.Hourly24hCount}} ордеров · пик {{.PeakHourLabel}} ({{.PeakHourCount}})</div>
		</div>
		<div class="card accent">
			<div class="lbl"><span class="ico">∑</span> Объём всего</div>
			<div class="val">{{fmtAmount .Totals.VolumeAllTime}}<span class="sub" style="display:inline; margin-left:6px;">₽</span></div>
			<div class="sub">средний чек {{fmtAmount .AvgOrder}} ₽</div>
		</div>
	</div>

	<div class="grid kpi-2" style="margin-top:14px;">
		<div class="card">
			<div class="lbl">Поймано</div>
			<div class="val pos">{{.Totals.OrdersSniped}}</div>
			<div class="sub">успешных захватов</div>
		</div>
		<div class="card">
			<div class="lbl">Неуспешных попыток</div>
			<div class="val neg">{{.Totals.OrdersMissed}}</div>
			<div class="sub">Invalid status · ордер ушёл другому</div>
		</div>
		<div class="card">
			<div class="lbl">Success rate</div>
			<div class="val">{{fmtPct .SuccessRate}}%</div>
			<div class="sub">{{.Totals.OrdersSniped}} из {{.Attempts}} попыток</div>
		</div>
		<div class="card">
			<div class="lbl">Средний чек</div>
			<div class="val">{{fmtAmount .AvgOrder}}<span class="sub" style="display:inline; margin-left:6px;">₽</span></div>
			<div class="sub">по пойманным ордерам</div>
		</div>
	</div>

	<div class="cols" style="margin-top:18px;">
		<section>
			<h2>Активность 24ч <span class="badge">{{.Hourly24hCount}} ордеров · {{fmtAmount .Hourly24hTotal}} ₽</span></h2>
			<div class="chart-wrap">
				{{template "chart" .Hourly}}
			</div>
		</section>
		<section>
			<h2>Топ по объёму</h2>
			<div class="top-list">
				{{range .TopUsers}}
				<div class="top-item">
					<div class="top-rank">#</div>
					<div>
						<div class="top-name">{{if .Username}}{{.Username}}{{else}}Trader{{end}}</div>
						<div class="top-meta">ID {{.UserID}} · {{.OrdersTotal}} ордеров</div>
					</div>
					<div class="top-vol">{{fmtAmount .VolumeAllTime}} ₽</div>
				</div>
				{{else}}
				<div class="empty">Нет данных</div>
				{{end}}
			</div>
		</section>
	</div>

	<section>
		<h2>Последние ордера <span class="badge">{{len .Recent}}</span></h2>
		<div class="feed">
			{{range .Recent}}
			<div class="feed-item">
				<span class="feed-dot {{statusCls .Status}}"></span>
				<div class="feed-info">
					<div class="fname">{{if .Username}}{{.Username}}{{else}}Trader{{end}} · <span class="tag {{statusCls .Status}}">{{.Status}}</span></div>
					<div class="fmeta">{{.OrderID}} · user {{.UserID}} · {{fmtTime .CreatedAt}}</div>
				</div>
				<div class="feed-amt">{{fmtAmount .Amount}}<span class="cur">₽</span></div>
			</div>
			{{else}}
			<div class="empty">Пока нет ордеров</div>
			{{end}}
		</div>
	</section>

	<section>
		<h2>Пользователи <span class="badge">{{len .Users}}</span></h2>
		<div class="tbl-wrap">
		<table>
			<thead>
				<tr>
					<th>User</th>
					<th>Токен</th>
					<th>Статус</th>
					<th>Лимиты, ₽</th>
					<th class="num">Поймано</th>
					<th class="num">Мисс</th>
					<th class="num">Объём 24ч</th>
					<th class="num">Объём всего</th>
					<th>Последний</th>
				</tr>
			</thead>
			<tbody>
				{{range .Users}}
				<tr>
					<td>
						<div style="font-weight:600;">{{if .Username}}{{.Username}}{{else}}Trader{{end}}</div>
						<div style="color:var(--muted); font-size:11px; font-family:'JetBrains Mono',monospace;">{{.UserID}}</div>
					</td>
					<td>{{if .HasToken}}<span class="tag ok">YES</span>{{else}}<span class="tag off">NO</span>{{end}}</td>
					<td>{{if .IsRunning}}<span class="tag ok"><span class="pulse"></span>RUNNING</span>{{else}}<span class="tag off">IDLE</span>{{end}}</td>
					<td class="mono">{{fmtAmount .MinAmount}} – {{fmtAmount .MaxAmount}}</td>
					<td class="num" style="color:var(--ok);">{{.SnipedOrders}}</td>
					<td class="num" style="color:var(--bad);">{{.MissedOrders}}</td>
					<td class="num">{{fmtAmount .Volume24h}}</td>
					<td class="num">{{fmtAmount .VolumeAllTime}}</td>
					<td class="mono">{{fmtTime .LastOrderAt}}</td>
				</tr>
				{{else}}
				<tr><td colspan="9" class="empty">Нет пользователей</td></tr>
				{{end}}
			</tbody>
		</table>
		</div>
	</section>

	<footer>P2C Sniper Admin · автообновление каждые 15 секунд</footer>
</div>

{{define "chart"}}
<svg class="chart-svg" viewBox="0 0 {{len .}} 100" preserveAspectRatio="none">
	<defs>
		<linearGradient id="barGrad" x1="0" y1="0" x2="0" y2="1">
			<stop offset="0%" stop-color="#a87bff" stop-opacity="0.95"/>
			<stop offset="100%" stop-color="#7c9cff" stop-opacity="0.35"/>
		</linearGradient>
	</defs>
	{{range .}}
		<rect x="{{.X}}.15" y="{{.BarY}}" width="0.7" height="{{.BarH}}" fill="url(#barGrad)" rx="0.12"/>
	{{end}}
</svg>
<div style="display:flex; justify-content:space-between; color:var(--muted); font-size:10px; font-family:'JetBrains Mono',monospace; margin-top:6px;">
	{{range .}}{{if .ShowLbl}}<span>{{.Label}}</span>{{end}}{{end}}
</div>
{{end}}
</body>
</html>`
