package main

import (
	"archive/zip"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Point 一个报价点
type Point struct {
	Date  string
	Price float64
}

// ---------------------------------------------------------------------------
// XLSX 读取（只用标准库：xlsx 就是个 zip + 若干 xml）
// ---------------------------------------------------------------------------

type xlsxCell struct {
	R  string `xml:"r,attr"`
	T  string `xml:"t,attr"`
	V  string `xml:"v"`
	IS struct {
		T string `xml:"t"`
	} `xml:"is"`
}

type xlsxRow struct {
	R     string     `xml:"r,attr"`
	Cells []xlsxCell `xml:"c"`
}

type xlsxSheetXML struct {
	Rows []xlsxRow `xml:"sheetData>row"`
}

type xlsxWorkbook struct {
	Sheets []struct {
		Name string `xml:"name,attr"`
		RID  string `xml:"id,attr"`
	} `xml:"sheets>sheet"`
}

type xlsxRels struct {
	Rels []struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

// colIndex 把 "B12" 这类引用转成 0 基列号
func colIndex(ref string) int {
	n := 0
	for _, ch := range ref {
		if ch >= 'A' && ch <= 'Z' {
			n = n*26 + int(ch-'A'+1)
		} else if ch >= 'a' && ch <= 'z' {
			n = n*26 + int(ch-'a'+1)
		} else {
			break
		}
	}
	return n - 1
}

func excelDate(serial float64) string {
	if serial < 1 || serial > 90000 {
		return ""
	}
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	return base.AddDate(0, 0, int(serial)).Format("2006-01-02")
}

// readXlsxSheet 返回指定 sheet 的二维字符串表
func readXlsxSheet(file, sheetName string) ([][]string, error) {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	read := func(name string) ([]byte, error) {
		for _, f := range zr.File {
			if f.Name == name {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, fmt.Errorf("xlsx 内缺少 %s", name)
	}

	// 共享字符串
	var shared []string
	if b, err := read("xl/sharedStrings.xml"); err == nil {
		var sst struct {
			SI []struct {
				T  string `xml:"t"`
				R  []struct {
					T string `xml:"t"`
				} `xml:"r"`
			} `xml:"si"`
		}
		if xml.Unmarshal(b, &sst) == nil {
			for _, si := range sst.SI {
				if si.T != "" {
					shared = append(shared, si.T)
					continue
				}
				var sb strings.Builder
				for _, r := range si.R {
					sb.WriteString(r.T)
				}
				shared = append(shared, sb.String())
			}
		}
	}

	// 工作簿 -> sheet 路径
	wbBytes, err := read("xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	var wb xlsxWorkbook
	if err := xml.Unmarshal(wbBytes, &wb); err != nil {
		return nil, err
	}
	target := ""
	for _, s := range wb.Sheets {
		if s.Name == sheetName {
			target = s.RID
			break
		}
	}
	if target == "" {
		return nil, fmt.Errorf("找不到工作表 %s", sheetName)
	}
	relsBytes, err := read("xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	var rels xlsxRels
	xml.Unmarshal(relsBytes, &rels)
	sheetPath := ""
	for _, r := range rels.Rels {
		if r.ID == target {
			sheetPath = "xl/" + strings.TrimPrefix(path.Clean(r.Target), "/")
			if strings.HasPrefix(r.Target, "/") {
				sheetPath = strings.TrimPrefix(r.Target, "/")
			}
			break
		}
	}
	if sheetPath == "" {
		return nil, fmt.Errorf("找不到工作表文件")
	}

	sheetBytes, err := read(sheetPath)
	if err != nil {
		return nil, err
	}
	var sx xlsxSheetXML
	if err := xml.Unmarshal(sheetBytes, &sx); err != nil {
		return nil, err
	}

	rows := make([][]string, 0, len(sx.Rows))
	for _, r := range sx.Rows {
		vals := map[int]string{}
		maxc := -1
		for _, c := range r.Cells {
			ci := colIndex(c.R)
			if ci < 0 {
				continue
			}
			val := c.V
			switch c.T {
			case "s":
				if idx, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && idx >= 0 && idx < len(shared) {
					val = shared[idx]
				}
			case "str":
				// 公式字符串结果，原样
			case "inlineStr":
				val = c.IS.T
			}
			vals[ci] = strings.TrimSpace(val)
			if ci > maxc {
				maxc = ci
			}
		}
		line := make([]string, maxc+1)
		for k, v := range vals {
			line[k] = v
		}
		rows = append(rows, line)
	}
	return rows, nil
}

// worldPrices 读欧盟世界谷物价格表 -> {列名: 序列}
// 注意：该表左半区是美元/吨、右半区是欧元/吨，列名重复，只取美元区（列 1..11）
func worldPrices(cfg *Config, force bool) (map[string][]Point, error) {
	p := filepath.Join(cacheDir, "world_prices.xlsx")
	if force || !cacheFresh(p, cfg.Cache.RefreshDays) {
		logf("更新欧盟世界谷物价格表…")
		tmp := p + ".tmp"
		if err := download(cfg.Cache.WorldXlsx, tmp, 50000); err != nil {
			if _, statErr := os.Stat(p); statErr != nil {
				return nil, fmt.Errorf("世界谷物价格表下载失败: %v", err)
			}
			logf("  下载失败，改用本地已有缓存继续")
		} else {
			os.Rename(tmp, p)
		}
	}
	rows, err := readXlsxSheet(p, "Data")
	if err != nil {
		return nil, err
	}
	if len(rows) < 3 {
		return nil, fmt.Errorf("世界谷物价格表内容异常")
	}
	header := rows[1]
	out := map[string][]Point{}
	const usdLast = 11
	for i := 1; i <= usdLast && i < len(header); i++ {
		if header[i] != "" {
			out[header[i]] = nil
		}
	}
	for _, row := range rows[2:] {
		if len(row) == 0 || row[0] == "" {
			continue
		}
		serial, err := strconv.ParseFloat(row[0], 64)
		if err != nil {
			continue
		}
		d := excelDate(serial)
		if d == "" {
			continue
		}
		for i := 1; i <= usdLast && i < len(row) && i < len(header); i++ {
			if header[i] == "" || row[i] == "" {
				continue
			}
			v, err := strconv.ParseFloat(row[i], 64)
			if err != nil {
				continue
			}
			out[header[i]] = append(out[header[i]], Point{d, v})
		}
	}
	for k := range out {
		sort.Slice(out[k], func(a, b int) bool { return out[k][a].Date < out[k][b].Date })
	}
	return out, nil
}

// euMemberPrices 读欧盟成员国现货周报（zip 内一个大 CSV）-> {"IT|Soft wheat|Bologna": 序列}
func euMemberPrices(cfg *Config, force bool) (map[string][]Point, error) {
	zp := filepath.Join(cacheDir, "eu_ms_raw.zip")
	if force || !cacheFresh(zp, cfg.Cache.RefreshDays) {
		logf("更新欧盟成员国现货周报（约 2MB，官网限速，可能要 1-3 分钟）…")
		// 先下到临时文件并校验完整性，通过才替换旧包。
		// 之前直接覆盖，曾出现只下了 2.2MB 中的一截、zip 打不开，把好包也一起弄坏的情况。
		tmp := zp + ".tmp"
		var lastErr error
		for attempt := 1; attempt <= 2; attempt++ {
			os.Remove(tmp)
			if err := download(cfg.Cache.EuMsZip, tmp, 1000000); err != nil {
				lastErr = err
			}
			if zipOK(tmp) {
				if err := os.Rename(tmp, zp); err == nil {
					lastErr = nil
					break
				}
			}
			if lastErr == nil {
				lastErr = fmt.Errorf("下载的数据包不完整")
			}
			logf("  第 %d 次下载不完整，重试…", attempt)
		}
		if lastErr != nil {
			os.Remove(tmp)
			if !zipOK(zp) {
				return nil, fmt.Errorf("欧盟成员国数据包下载失败: %v", lastErr)
			}
			logf("  沿用上次完好的数据包继续")
		}
	}
	zr, err := zip.OpenReader(zp)
	if err != nil {
		return nil, fmt.Errorf("数据包损坏: %v", err)
	}
	defer zr.Close()
	var target *zip.File
	for _, f := range zr.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
			target = f
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("数据包内没有 CSV")
	}
	rc, err := target.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	r := csv.NewReader(rc)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	r.ReuseRecord = false
	head, err := r.Read()
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, h := range head {
		idx[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}
	need := []string{"Member State Code", "Product Group Name", "Market Name",
		"Product Stage Name", "Week - Begin Date", "EU Price"}
	for _, n := range need {
		if _, ok := idx[n]; !ok {
			return nil, fmt.Errorf("CSV 缺少字段 %s", n)
		}
	}
	out := map[string][]Point{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		get := func(n string) string {
			i := idx[n]
			if i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		grp := get("Product Group Name")
		if grp != "Soft wheat" && grp != "Durum wheat" {
			continue
		}
		if !strings.Contains(get("Product Stage Name"), "Deliver to first customer") {
			continue
		}
		raw := strings.ReplaceAll(strings.ReplaceAll(get("EU Price"), "€", ""), ",", ".")
		raw = strings.ReplaceAll(raw, " ", "")
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		d := get("Week - Begin Date")
		if len(d) > 10 {
			d = d[:10]
		}
		if len(d) != 10 {
			continue
		}
		k := get("Member State Code") + "|" + grp + "|" + get("Market Name")
		out[k] = append(out[k], Point{d, v})
	}
	for k := range out {
		sort.Slice(out[k], func(a, b int) bool { return out[k][a].Date < out[k][b].Date })
	}
	return out, nil
}
