package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed template.html
var templateHTML string

//go:embed assets/echarts.min.js
var echartsJS []byte

// ensureAssets 图表库随程序内置一份，assets 被误删也能自动恢复
func ensureAssets() {
	p := filepath.Join(root, "assets", "echarts.min.js")
	if st, err := os.Stat(p); err == nil && st.Size() > 500000 {
		return
	}
	os.MkdirAll(filepath.Dir(p), 0755)
	if err := os.WriteFile(p, echartsJS, 0644); err == nil {
		logf("已释放图表库 assets/echarts.min.js")
	}
}

type Series struct {
	Name     string     `json:"name"`
	Market   string     `json:"market"`
	Grade    string     `json:"grade"`
	Role     string     `json:"role"`
	Unit     string     `json:"unit"`
	Currency string     `json:"currency"`
	Dates    []string   `json:"dates"`
	USD      []*float64 `json:"usd"`
	Native   []*float64 `json:"native"`
	Status   []string   `json:"status"`
	Notes    []string   `json:"notes"`
}

func num(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

func fmtNum(v *float64, digits int) string {
	if v == nil {
		return "—"
	}
	s := strconv.FormatFloat(*v, 'f', digits, 64)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	intPart := s
	frac := ""
	if i := strings.Index(s, "."); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	var b strings.Builder
	for i, ch := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	out := b.String() + frac
	if neg {
		out = "-" + out
	}
	return out
}

func buildSeries(rows []Row, cfg *Config) map[string]*Series {
	out := map[string]*Series{}
	order := []string{}
	for _, s := range cfg.Sources {
		out[s.Key] = &Series{Name: s.Name, Market: s.Market, Grade: s.Grade,
			Role: s.Role, Unit: s.Unit, Currency: s.Currency}
		order = append(order, s.Key)
	}
	type cell struct {
		u, p *float64
		st   string
		n    string
		ok   bool
	}
	pts := map[string]map[string]cell{}
	for _, r := range rows {
		if _, exist := out[r.Key]; !exist {
			continue
		}
		u, p := num(r.PriceUSD), num(r.Price)
		if u == nil && p == nil && r.Status != "need_review" {
			continue
		}
		if pts[r.Key] == nil {
			pts[r.Key] = map[string]cell{}
		}
		prev, has := pts[r.Key][r.Date]
		if !has || r.Status == "ok" {
			pts[r.Key][r.Date] = cell{u, p, r.Status, r.Note, true}
		} else {
			_ = prev
		}
	}
	for k, m := range pts {
		ds := make([]string, 0, len(m))
		for d := range m {
			ds = append(ds, d)
		}
		sort.Strings(ds)
		s := out[k]
		for _, d := range ds {
			c := m[d]
			s.Dates = append(s.Dates, d)
			s.USD = append(s.USD, c.u)
			s.Native = append(s.Native, c.p)
			s.Status = append(s.Status, c.st)
			s.Notes = append(s.Notes, c.n)
		}
	}
	_ = order
	return out
}

func buildTable(series map[string]*Series, cfg *Config) string {
	var sb strings.Builder
	today := nowCST()
	for _, s := range cfg.Sources {
		v := series[s.Key]
		if v == nil || len(v.Dates) == 0 {
			sb.WriteString(fmt.Sprintf(
				"<tr class='empty'><td><b>%s</b></td><td>%s</td><td colspan='5'>暂无数据</td></tr>\n",
				s.Name, s.Market))
			continue
		}
		i := len(v.Dates) - 1
		cur := v.Native[i]
		var prev *float64
		if i > 0 {
			prev = v.Native[i-1]
		}
		if cur == nil {
			sb.WriteString(fmt.Sprintf(
				"<tr><td><b>%s</b></td><td class='muted'>%s</td>"+
					"<td class='num future'>待人工核对</td><td class='num'>—</td>"+
					"<td class='num'>—</td><td class='num'>—</td>"+
					"<td class='muted nowrap'>%s</td></tr>\n", s.Name, s.Market, v.Dates[i]))
			continue
		}
		var chg, pct *float64
		cls, sign := "", ""
		if prev != nil {
			c := *cur - *prev
			chg = &c
			if *prev != 0 {
				p := c / *prev * 100
				pct = &p
			}
			if c > 0 {
				cls, sign = "up", "+"
			} else if c < 0 {
				cls = "down"
			}
		}
		chgTxt := "—"
		if chg != nil {
			chgTxt = fmt.Sprintf("<span class='%s'>%s%s</span>", cls, sign, fmtNum(chg, 2))
		}
		pctTxt := "—"
		if pct != nil {
			pctTxt = fmt.Sprintf("<span class='%s'>%s%.2f%%</span>", cls, sign, *pct)
		}
		dt, err := time.Parse("2006-01-02", v.Dates[i])
		gap := 999
		if err == nil {
			gap = int(today.Sub(dt).Hours() / 24)
		}
		// 日期与"滞后多少天"分两行：挤在一行会把最后一列顶出纸面（纵向打印时尤其明显）
		var fresh string
		switch {
		case gap >= 30:
			fresh = fmt.Sprintf("<span class='stale'>%s</span><span class='lag'>滞后 %d 天</span>", v.Dates[i], gap)
		case gap >= 9:
			fresh = fmt.Sprintf("<span class='mild'>%s</span><span class='lag'>%d 天前</span>", v.Dates[i], gap)
		default:
			fresh = fmt.Sprintf("<span class='fresh'>%s</span>", v.Dates[i])
		}
		tag := ""
		switch s.Role {
		case "policy_floor":
			tag = "<span class='pill'>政策价</span>"
		case "crosscheck":
			tag = "<span class='pill gray'>交叉验证</span>"
		}
		sb.WriteString(fmt.Sprintf(
			"<tr><td><b>%s</b> %s</td><td class='muted'>%s</td>"+
				"<td class='num'>%s <span class='unit'>%s</span></td>"+
				"<td class='num'>%s</td><td class='num'>%s</td>"+
				"<td class='num'>%s</td><td class='muted nowrap'>%s</td></tr>\n",
			s.Name, tag, s.Market, fmtNum(cur, 2), s.Unit,
			chgTxt, pctTxt, fmtNum(v.USD[i], 0), fresh))
	}
	return sb.String()
}

type shotItem struct {
	Key string `json:"key"`
	Rel string `json:"rel"`
	Ext string `json:"ext"`
}

type shotDay struct {
	Date  string     `json:"date"`
	Items []shotItem `json:"items"`
}

func buildShots() []shotDay {
	ents, err := os.ReadDir(snapDir)
	if err != nil {
		return nil
	}
	var days []string
	for _, e := range ents {
		if e.IsDir() {
			days = append(days, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	var out []shotDay
	for _, d := range days {
		var items []shotItem
		for _, f := range listSnapshot(d) {
			ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f), "."))
			items = append(items, shotItem{
				Key: strings.TrimSuffix(f, filepath.Ext(f)),
				Rel: "snapshots/" + d + "/" + f,
				Ext: ext,
			})
		}
		if len(items) > 0 {
			out = append(out, shotDay{d, items})
		}
	}
	return out
}

func buildDashboard(cfg *Config) error {
	ensureAssets()
	rows := loadRows()
	series := buildSeries(rows, cfg)

	live := map[string]*Series{}
	for k, v := range series {
		has := false
		for _, x := range v.USD {
			if x != nil {
				has = true
				break
			}
		}
		if !has {
			for _, x := range v.Native {
				if x != nil {
					has = true
					break
				}
			}
		}
		if has {
			live[k] = v
		}
	}
	var soft, durum []string
	for _, s := range cfg.Sources {
		if v, ok := live[s.Key]; ok {
			if v.Grade == "soft" {
				soft = append(soft, s.Key)
			} else if v.Grade == "durum" {
				durum = append(durum, s.Key)
			}
		}
	}

	d0, d1 := "", ""
	if len(rows) > 0 {
		ds := make([]string, 0, len(rows))
		for _, r := range rows {
			ds = append(ds, r.Date)
		}
		sort.Strings(ds)
		d0, d1 = ds[0], ds[len(ds)-1]
	}

	shotLabels := map[string]string{}
	meta := map[string]interface{}{}
	for _, s := range cfg.Sources {
		if s.ShotSlug != "" {
			shotLabels[s.ShotSlug] = s.label()
		}
		meta[s.Key] = map[string]string{"name": s.Name, "short": s.Short, "unit": s.Unit,
			"currency": s.Currency, "role": s.Role}
	}

	payload := map[string]interface{}{
		"series":     live,
		"soft":       soft,
		"durum":      durum,
		"d0":         d0,
		"d1":         d1,
		"built":      nowCST().Format("2006-01-02 15:04"),
		"cur":        map[string]string{"USD": "$", "EUR": "€", "TRY": "₺"},
		"shotLabels": shotLabels,
		"meta":       meta,
	}
	pj, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	shots := buildShots()
	sj, err := json.Marshal(shots)
	if err != nil {
		return err
	}
	nShots := 0
	for _, s := range shots {
		nShots += len(s.Items)
	}

	html := templateHTML
	repl := map[string]string{
		"__PAYLOAD__": string(pj),
		"__SHOTS__":   string(sj),
		"__TABLE__":   buildTable(series, cfg),
		"__D0__":      orDash(d0),
		"__D1__":      orDash(d1),
		"__BUILT__":   payload["built"].(string),
		"__TOTAL__":   strconv.Itoa(len(rows)),
		"__NSHOTS__":  strconv.Itoa(nShots),
	}
	for k, v := range repl {
		html = strings.ReplaceAll(html, k, v)
	}
	out := filepath.Join(root, "dashboard.html")
	if err := os.WriteFile(out, []byte(html), 0644); err != nil {
		return err
	}
	logf("生成 dashboard.html：%d 个序列 / %d 条记录 / %d 份存档", len(live), len(rows), nShots)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
