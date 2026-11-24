package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

func today() string { return nowCST().Format("2006-01-02") }

// ---------------- 每日采集 ----------------
func cmdDaily(cfg *Config, day string, noShot, refresh, doOpen bool) {
	if day == "" {
		day = today()
	}
	fx := getFX(cfg)
	rows := loadRows()
	var world map[string][]Point
	var ms map[string][]Point
	var worldErr, msErr error

	logf("=== 开始采集 %s ===", day)
	for _, src := range cfg.Sources {
		if !src.Enabled || src.Kind == "manual" {
			continue
		}
		var rec Row
		fail := func(e error) {
			logf("取价失败 %-16s %v", src.Key, e)
			rec = Row{Date: day, Key: src.Key, Name: src.Name, Market: src.Market,
				Grade: src.Grade, Price: "", Currency: src.Currency, Unit: src.Unit,
				Status: "fail", Note: trunc(e.Error(), 150),
				CapturedAt: nowCST().Format("2006-01-02T15:04:05")}
			rows = upsert(rows, rec)
		}

		switch src.Kind {
		case "cnbc":
			v, note, err := cnbcQuote(src.Symbol)
			if err != nil {
				fail(err)
				continue
			}
			ps := strconv.FormatFloat(v, 'f', 2, 64)
			rec = newRec(src, day, ps, fx, "ok", note)

		case "eu_world":
			if world == nil && worldErr == nil {
				world, worldErr = worldPrices(cfg, refresh)
			}
			if worldErr != nil {
				fail(worldErr)
				continue
			}
			ser := world[src.Column]
			if len(ser) == 0 {
				fail(fmt.Errorf("表内无此列：%s", src.Column))
				continue
			}
			last := ser[len(ser)-1]
			rec = newRec(src, last.Date, strconv.FormatFloat(last.Price, 'f', 2, 64),
				fx, "ok", "欧盟周报 · 报价周 "+last.Date)

		case "eu_ms":
			if ms == nil && msErr == nil {
				ms, msErr = euMemberPrices(cfg, refresh)
			}
			if msErr != nil {
				fail(msErr)
				continue
			}
			ser := ms[src.MemberState+"|"+src.ProductGrp+"|"+src.MarketName]
			if len(ser) == 0 {
				fail(fmt.Errorf("无该市场报价"))
				continue
			}
			last := ser[len(ser)-1]
			rec = newRec(src, last.Date, strconv.FormatFloat(last.Price, 'f', 2, 64),
				fx, "ok", "欧盟官方现货周报 · 报价周 "+last.Date)

		case "page":
			rec = newRec(src, day, "", fx, "need_review", "页面需人工核对，见当日截图")

		default:
			continue
		}
		rows = upsert(rows, rec)
		logf("取价 OK  %-16s = %-10s %-8s (%s)", src.Key, rec.Price, src.Unit, rec.Note)
	}

	// ---- 截图存档 ----
	if !noShot {
		shots := takeShots(cfg, day)
		for slug, rel := range shots {
			for i := range rows {
				if rows[i].Date == day && slugOf(rows[i].Key, cfg) == slug {
					rows[i].Screenshot = rel
				}
			}
		}
	}

	saveRows(rows)
	saveLog()
	if err := buildDashboard(cfg); err != nil {
		logf("看板生成失败: %v", err)
	}
	if doOpen {
		openLocal(filepath.Join(root, "dashboard.html"))
	}
}

func slugOf(key string, cfg *Config) string {
	for _, s := range cfg.Sources {
		if s.Key == key {
			return s.slug()
		}
	}
	return key
}

func newRec(src Source, day, price string, fx map[string]float64, status, note string) Row {
	return Row{Date: day, Key: src.Key, Name: src.Name, Market: src.Market,
		Grade: src.Grade, Price: price, Currency: src.Currency, Unit: src.Unit,
		PriceUSD: toUSD(price, src.Currency, src.Unit, fx),
		Status:   status, Note: note,
		CapturedAt: nowCST().Format("2006-01-02T15:04:05")}
}

func takeShots(cfg *Config, day string) map[string]string {
	dir := filepath.Join(snapDir, day)
	os.MkdirAll(dir, 0755)

	// 同一页面只截一次（一张欧盟面板覆盖多个品种）
	jobs := map[string]shotJob{}
	var order []string
	for _, s := range cfg.Sources {
		if !s.Enabled {
			continue
		}
		page := s.shotPage()
		if page == "" {
			continue
		}
		slug := s.slug()
		if _, ok := jobs[slug]; !ok {
			jobs[slug] = shotJob{slug, page, s.ShotWait}
			order = append(order, slug)
		}
	}
	if len(order) == 0 {
		return nil
	}

	browser := findBrowser()
	out := map[string]string{}
	if browser == "" {
		logf("未找到 Edge/Chrome，跳过网页截图（数据不受影响）。装 Edge 即自动恢复。")
	} else {
		logf("浏览器: %s（最多 2 个页面同时截图）", browser)
		sem := make(chan struct{}, 2)
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, slug := range order {
			wg.Add(1)
			go func(slug string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				j := jobs[slug]
				p := filepath.Join(dir, slug+".png")
				if !probe(j.URL) {
					logf("  跳过 %s：页面当前无法访问（网络或站点封锁）", slug)
					return
				}
				logf("截图 %s …", slug)
				if err := shotPage(browser, j.URL, p, j.WaitMS); err != nil {
					logf("  截图失败 %s: %v", slug, err)
					return
				}
				mu.Lock()
				out[slug] = "snapshots/" + day + "/" + slug + ".png"
				mu.Unlock()
				if st, err := os.Stat(p); err == nil {
					logf("  截图 OK  %s (%d KB)", slug, st.Size()/1024)
				}
			}(slug)
		}
		wg.Wait()
	}

	// 官方报告类文件（如欧盟周报 PDF）：官网限速很慢，与截图并行下载
	type dlJob struct{ slug, label, url string }
	type dlRes struct {
		slug, rel string
		err       error
	}
	var dlJobs []dlJob
	for _, d := range cfg.Downloads {
		dlJobs = append(dlJobs, dlJob{d.Slug, d.Label, d.URL})
	}
	ch := make(chan dlRes, len(dlJobs))
	for _, j := range dlJobs {
		go func(j dlJob) {
			p := filepath.Join(dir, j.slug+".pdf")
			logf("存档 %s …（限速，可能要几分钟）", j.label)
			if err := grabFile(j.url, p); err != nil {
				ch <- dlRes{j.slug, "", err}
				return
			}
			ch <- dlRes{j.slug, "snapshots/" + day + "/" + j.slug + ".pdf", nil}
		}(j)
	}
	for i := 0; i < len(dlJobs); i++ {
		r := <-ch
		if r.err != nil {
			logf("  存档失败 %s: %v", r.slug, r.err)
			continue
		}
		out[r.slug] = r.rel
		p := filepath.Join(dir, r.slug+".pdf")
		if st, err := os.Stat(p); err == nil {
			logf("  存档 OK  %s (%d KB)", r.slug, st.Size()/1024)
		}
	}
	return out
}

// ---------------- 回补历史 ----------------
func cmdBackfill(cfg *Config, period string, refresh bool) {
	if period == "" {
		period = "5Y"
	}
	fx := getFX(cfg)
	rows := loadRows()
	var world map[string][]Point
	var ms map[string][]Point
	var worldErr, msErr error
	n := 0
	logf("=== 回补历史数据 ===")
	for _, src := range cfg.Sources {
		if !src.Enabled || src.Kind == "manual" || src.Kind == "page" {
			continue
		}
		var ser []Point
		switch src.Kind {
		case "cnbc":
			h, err := cnbcHistory(src.Symbol, period)
			if err != nil {
				logf("回补失败 %s: %v", src.Key, err)
				continue
			}
			ser = h
		case "eu_world":
			if world == nil && worldErr == nil {
				world, worldErr = worldPrices(cfg, refresh)
			}
			if worldErr != nil {
				logf("回补失败 %s: %v", src.Key, worldErr)
				continue
			}
			ser = world[src.Column]
		case "eu_ms":
			if ms == nil && msErr == nil {
				ms, msErr = euMemberPrices(cfg, refresh)
			}
			if msErr != nil {
				logf("回补失败 %s: %v", src.Key, msErr)
				continue
			}
			ser = ms[src.MemberState+"|"+src.ProductGrp+"|"+src.MarketName]
		}
		if len(ser) == 0 {
			logf("回补 %-16s 无数据", src.Key)
			continue
		}
		logf("回补 %-16s %d 个点  %s → %s", src.Key, len(ser), ser[0].Date, ser[len(ser)-1].Date)
		for _, p := range ser {
			rows = upsert(rows, newRec(src, p.Date,
				strconv.FormatFloat(p.Price, 'f', 2, 64), fx, "ok", "历史回补"))
			n++
		}
	}
	saveRows(rows)
	logf("回补完成，写入 %d 条", n)
	saveLog()
	buildDashboard(cfg)
}

// ---------------- 手工录入 ----------------
func cmdManual(cfg *Config, key, price, date, note string) {
	var src *Source
	for i := range cfg.Sources {
		if cfg.Sources[i].Key == key {
			src = &cfg.Sources[i]
			break
		}
	}
	if src == nil {
		p("没有这个品种代码:", key)
		var keys []string
		for _, s := range cfg.Sources {
			keys = append(keys, s.Key)
		}
		p("可用代码:", strings.Join(keys, " / "))
		return
	}
	if date == "" {
		date = today()
	}
	if note == "" {
		note = "人工录入"
	}
	fx := getFX(cfg)
	rows := upsert(loadRows(), newRec(*src, date, price, fx, "manual", note))
	saveRows(rows)
	logf("已录入 %s = %s %s @ %s", key, price, src.Unit, date)
	buildDashboard(cfg)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// ---------------- 入口 ----------------
func main() {
	initPaths()
	setupConsole()
	args := os.Args[1:]

	cfg, err := loadConfig()
	if err != nil {
		p("读取配置失败：", err)
		p("请确认 config\\sources.json 与程序在同一目录下。")
		pause()
		return
	}

	if len(args) == 0 {
		menu(cfg)
		return
	}

	cmd := strings.ToLower(args[0])
	rest := args[1:]
	opt := func(name, def string) string {
		for i, a := range rest {
			if a == name && i+1 < len(rest) {
				return rest[i+1]
			}
		}
		return def
	}
	has := func(name string) bool {
		for _, a := range rest {
			if a == name {
				return true
			}
		}
		return false
	}

	switch cmd {
	case "daily", "update":
		cmdDaily(cfg, opt("--date", today()), has("--no-shot"), has("--refresh"), has("--open"))
	case "backfill":
		cmdBackfill(cfg, opt("--period", "5Y"), has("--refresh"))
	case "build":
		buildDashboard(cfg)
	case "manual":
		if len(rest) < 2 {
			p("用法: wheat.exe manual <品种代码> <价格> [--date 2026-06-02] [--note 备注]")
			return
		}
		cmdManual(cfg, rest[0], rest[1], opt("--date", ""), opt("--note", ""))
	default:
		p("未知命令:", cmd)
	}
	saveLog()
}

func menu(cfg *Config) {
	reader := bufio.NewReader(os.Stdin)
	for {
		pf("\n")
		p("==========================================================")
		p("            小麦价格监控 · 美国 / 欧洲 / 土耳其")
		p("==========================================================")
		p("   [1] 更新今日数据（抓价 + 截图 + 刷新看板）")
		p("   [2] 手动录入价格（土耳其 TMO 等政策价）")
		p("   [3] 回补历史数据（首次使用或换电脑后跑一次）")
		p("   [4] 只重建看板（不联网）")
		p("   [0] 退出")
		p("----------------------------------------------------------")
		pf("请选择 [1] 直接回车: ")

		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		sel := strings.TrimSpace(line)
		if sel == "" {
			sel = "1"
		}
		switch sel {
		case "0", "q", "exit":
			return
		case "1":
			p()
			cmdDaily(cfg, today(), false, false, true)
			p("\n完成。看板已打开（dashboard.html）。")
		case "2":
			pf("品种代码（如 tmo_soft / tmo_durum，直接回车查看全部）: ")
			k, _ := reader.ReadString('\n')
			k = strings.TrimSpace(k)
			if k == "" {
				for _, s := range cfg.Sources {
					p(fmt.Sprintf("   %-18s %s (%s)", s.Key, s.Name, s.Unit))
				}
				continue
			}
			pf("价格（原单位数字）: ")
			val, _ := reader.ReadString('\n')
			val = strings.TrimSpace(val)
			if val == "" {
				p("已取消")
				continue
			}
			pf("日期 [回车=今天]: ")
			d, _ := reader.ReadString('\n')
			d = strings.TrimSpace(d)
			pf("备注: ")
			n, _ := reader.ReadString('\n')
			cmdManual(cfg, k, val, d, strings.TrimSpace(n))
			p("\n已录入。")
		case "3":
			p()
			cmdBackfill(cfg, "5Y", false)
			p("\n回补完成。")
		case "4":
			if err := buildDashboard(cfg); err != nil {
				p("生成失败:", err)
			} else {
				openLocal(filepath.Join(root, "dashboard.html"))
			}
		default:
			p("输入无效，请重新选择。")
		}
	}
}

var _ = sort.Strings
