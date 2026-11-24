package main

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Config 对应 config/sources.json
type Config struct {
	FX struct {
		URL string `json:"url"`
	} `json:"fx"`
	Cache struct {
		WorldXlsx   string `json:"world_xlsx"`
		EuMsZip     string `json:"eu_ms_zip"`
		RefreshDays int    `json:"refresh_days"`
	} `json:"cache"`
	Sources []Source `json:"sources"`
	Downloads []struct {
		Slug  string `json:"slug"`
		Label string `json:"label"`
		URL   string `json:"url"`
	} `json:"downloads"`
}

type Source struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Short       string `json:"short"`
	Market      string `json:"market"`
	Grade       string `json:"grade"`
	Kind        string `json:"kind"`
	Symbol      string `json:"symbol"`
	Column      string `json:"column"`
	Currency    string `json:"currency"`
	Unit        string `json:"unit"`
	Role        string `json:"role"`
	Enabled     bool   `json:"enabled"`
	MemberState string `json:"memberState"`
	ProductGrp  string `json:"productGroup"`
	MarketName  string `json:"marketName"`
	URL         string `json:"url"`
	ShotURL     string `json:"shot_url"`
	ShotSlug    string `json:"shot_slug"`
	ShotLabel   string `json:"shot_label"`
	ShotWait    int    `json:"shot_wait"`
}

func loadConfig() (*Config, error) {
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Cache.RefreshDays <= 0 {
		c.Cache.RefreshDays = 7
	}
	return &c, nil
}

func (s Source) slug() string {
	if s.ShotSlug != "" {
		return s.ShotSlug
	}
	return s.Key
}

func (s Source) label() string {
	if s.ShotLabel != "" {
		return s.ShotLabel
	}
	return s.Name
}

func (s Source) shotPage() string {
	if s.ShotURL != "" {
		return s.ShotURL
	}
	if s.URL != "" {
		return s.URL
	}
	if s.Symbol != "" {
		return "https://www.cnbc.com/quotes/" + s.Symbol
	}
	return ""
}

// Row 一行价格记录（字段与旧版 CSV 完全兼容）
type Row struct {
	Date      string
	Key       string
	Name      string
	Market    string
	Grade     string
	Price     string
	Currency  string
	Unit      string
	PriceUSD  string
	Status    string
	Note      string
	Screenshot string
	CapturedAt string
}

var csvFields = []string{"date", "key", "name", "market", "grade", "price",
	"currency", "unit", "price_usd_mt", "status", "note", "screenshot", "captured_at"}

func (r Row) cells() []string {
	return []string{r.Date, r.Key, r.Name, r.Market, r.Grade, r.Price,
		r.Currency, r.Unit, r.PriceUSD, r.Status, r.Note, r.Screenshot, r.CapturedAt}
}

func rowFrom(rec []string) Row {
	get := func(i int) string {
		if i < len(rec) {
			return rec[i]
		}
		return ""
	}
	return Row{get(0), get(1), get(2), get(3), get(4), get(5), get(6), get(7),
		get(8), get(9), get(10), get(11), get(12)}
}

func loadRows() []Row {
	f, err := os.Open(csvPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	all, err := r.ReadAll()
	if err != nil || len(all) < 2 {
		return nil
	}
	out := make([]Row, 0, len(all)-1)
	for _, rec := range all[1:] {
		out = append(out, rowFrom(rec))
	}
	return out
}

func saveRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Date != rows[j].Date {
			return rows[i].Date < rows[j].Date
		}
		return rows[i].Key < rows[j].Key
	})
	f, err := os.Create(csvPath)
	if err != nil {
		logf("写入 CSV 失败: %v", err)
		return
	}
	defer f.Close()
	f.WriteString("\ufeff") // Excel 打开中文不乱码
	w := csv.NewWriter(f)
	w.Write(csvFields)
	for _, r := range rows {
		w.Write(r.cells())
	}
	w.Flush()
	logf("写入 data/prices.csv：共 %d 条", len(rows))
}

// upsert 同一天同一品种只保留一条；重跑时保留已有截图路径
func upsert(rows []Row, rec Row) []Row {
	keepShot := ""
	for _, r := range rows {
		if r.Date == rec.Date && r.Key == rec.Key {
			keepShot = r.Screenshot
			break
		}
	}
	out := rows[:0]
	for _, r := range rows {
		if r.Date == rec.Date && r.Key == rec.Key {
			continue
		}
		out = append(out, r)
	}
	if rec.Screenshot == "" {
		rec.Screenshot = keepShot
	}
	return append(out, rec)
}

func toUSD(price, currency, unit string, fx map[string]float64) string {
	if strings.TrimSpace(price) == "" {
		return ""
	}
	p, err := strconv.ParseFloat(price, 64)
	if err != nil {
		return ""
	}
	switch unit {
	case "美分/蒲":
		return strconv.FormatFloat(p/(100.0*0.0272155), 'f', 2, 64)
	case "EUR/t", "USD/t", "TRY/t":
		if r, ok := fx[currency]; ok {
			return strconv.FormatFloat(p*r, 'f', 2, 64)
		}
	}
	return ""
}
