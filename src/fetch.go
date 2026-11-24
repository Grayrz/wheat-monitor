package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// getFX 返回 "1 单位外币 = 多少美元"
func getFX(cfg *Config) map[string]float64 {
	fallback := map[string]float64{"USD": 1.0, "EUR": 1.08, "TRY": 0.024, "CNY": 0.14}
	u := cfg.FX.URL
	if u == "" {
		u = "https://open.er-api.com/v6/latest/USD"
	}
	b, err := httpBytes(u, 25*time.Second)
	if err != nil {
		logf("汇率获取失败，用兜底值: %v", err)
		return fallback
	}
	var js struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := json.Unmarshal(b, &js); err != nil {
		logf("汇率解析失败，用兜底值")
		return fallback
	}
	fx := map[string]float64{"USD": 1.0}
	for _, cur := range []string{"EUR", "TRY", "CNY"} {
		if v, ok := js.Rates[cur]; ok && v > 0 {
			fx[cur] = 1.0 / v
		}
	}
	logf("汇率 OK: EUR/USD=%.4f  USD/TRY=%.3f", fx["EUR"], 1/fx["TRY"])
	return fx
}

// cnbcQuote 取 CNBC 实时/收盘报价（美分/蒲式耳）
func cnbcQuote(symbol string) (float64, string, error) {
	u := "https://quote.cnbc.com/quote-html-webservice/restQuote/symbolType/symbol" +
		"?symbols=" + url.QueryEscape(symbol) +
		"&requestMethod=itv&noform=1&partnerId=2&fund=1&output=json"
	b, err := httpBytes(u, 30*time.Second)
	if err != nil {
		return 0, "", err
	}
	var js struct {
		R struct {
			Q []struct {
				Symbol    string `json:"symbol"`
				Last      string `json:"last"`
				Code      int    `json:"code"`
				Name      string `json:"name"`
				ChangePct string `json:"change_pct"`
				Time      string `json:"last_timedate"`
			} `json:"FormattedQuote"`
		} `json:"FormattedQuoteResult"`
	}
	if err := json.Unmarshal(b, &js); err != nil {
		return 0, "", err
	}
	if len(js.R.Q) == 0 {
		return 0, "", fmt.Errorf("CNBC 无返回")
	}
	q := js.R.Q[0]
	if q.Code != 0 || q.Last == "" {
		return 0, "", fmt.Errorf("CNBC 无数据: %s", q.Symbol)
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(q.Last, ",", ""), 64)
	if err != nil {
		return 0, "", err
	}
	note := fmt.Sprintf("%s · 当日 %s · %s", q.Name, q.ChangePct, q.Time)
	return v, strings.TrimSpace(note), nil
}

// cnbcHistory 取历史日线，period: 1Y / 5Y / 10Y
func cnbcHistory(symbol, period string) ([]Point, error) {
	if period == "" {
		period = "1Y"
	}
	u := "https://ts-api.cnbc.com/harmony/app/charts/" + period + ".json?symbol=" + url.QueryEscape(symbol)
	b, err := httpBytes(u, 45*time.Second)
	if err != nil {
		return nil, err
	}
	var js struct {
		BarData struct {
			Bars []struct {
				TradeTime string `json:"tradeTime"`
				Close     string `json:"close"`
			} `json:"priceBars"`
		} `json:"barData"`
	}
	if err := json.Unmarshal(b, &js); err != nil {
		return nil, err
	}
	var out []Point
	for _, bar := range js.BarData.Bars {
		t := bar.TradeTime
		if len(t) < 8 {
			continue
		}
		d := fmt.Sprintf("%s-%s-%s", t[0:4], t[4:6], t[6:8])
		v, err := strconv.ParseFloat(bar.Close, 64)
		if err != nil {
			continue
		}
		out = append(out, Point{d, v})
	}
	return out, nil
}
