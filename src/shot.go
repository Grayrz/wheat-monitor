package main

import (
	"archive/zip"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// zipOK 真正校验压缩包内容，而不只是"能打开"。
// 下载被截断时中央目录往往仍在，只查能否打开会放过半截文件，导致后面解析才报错。
func zipOK(p string) bool {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return false
	}
	defer zr.Close()
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return false
		}
		h := crc32.NewIEEE()
		n, err := io.Copy(h, rc)
		rc.Close()
		if err != nil {
			return false
		}
		if uint64(n) != f.UncompressedSize64 || h.Sum32() != f.CRC32 {
			return false
		}
	}
	return true
}

// findBrowser 找本机可用的 Chromium 内核浏览器（Edge 是 Windows 自带的，无需安装）
func findBrowser() string {
	var cands []string
	if runtime.GOOS == "windows" {
		envs := []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"),
			os.Getenv("LOCALAPPDATA")}
		rels := []string{
			`Microsoft\Edge\Application\msedge.exe`,
			`Google\Chrome\Application\chrome.exe`,
			`Microsoft\Edge Beta\Application\msedge.exe`,
		}
		for _, e := range envs {
			if e == "" {
				continue
			}
			for _, r := range rels {
				cands = append(cands, filepath.Join(e, r))
			}
		}
	} else {
		cands = []string{"/usr/bin/chromium", "/usr/bin/chromium-browser",
			"/usr/bin/google-chrome", "/usr/bin/google-chrome-stable",
			"/usr/bin/microsoft-edge", "/snap/bin/chromium"}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	for _, name := range []string{"msedge", "chrome", "chromium", "chromium-browser", "google-chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// shotPage 用浏览器的 headless 模式给网页截图（等价于打开页面按全屏截图）
func shotPage(browser, pageURL, outPath string, waitMS int) error {
	if waitMS < 4000 {
		waitMS = 6000
	}
	abs, _ := filepath.Abs(outPath)
	os.Remove(abs)
	profile, _ := os.MkdirTemp("", "wheatprof")
	defer os.RemoveAll(profile)

	modes := [][]string{
		{"--headless=new"},
		{"--headless"},
	}
	var lastErr error
	for _, m := range modes {
		args := append([]string{}, m...)
		args = append(args,
			"--disable-gpu", "--hide-scrollbars", "--no-sandbox",
			"--disable-dev-shm-usage", "--no-first-run", "--no-default-browser-check",
			"--disable-extensions", "--mute-audio",
			"--user-data-dir="+profile,
			"--window-size=1440,2100",
			fmt.Sprintf("--virtual-time-budget=%d", waitMS),
			"--screenshot="+abs,
			pageURL,
		)
		cmd := exec.Command(browser, args...)
		done := make(chan error, 1)
		go func() { done <- cmd.Run() }()
		timedOut := false
		select {
		case <-done:
		case <-time.After(180 * time.Second):
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
			timedOut = true
		}
		if st, err := os.Stat(abs); err == nil && st.Size() > 8000 {
			return nil
		}
		if timedOut {
			// 页面打不开就别再用另一种模式重试，省时间
			return fmt.Errorf("页面加载超时（网络慢或被拦截）")
		}
		lastErr = fmt.Errorf("未生成截图")
	}
	return lastErr
}

type shotJob struct {
	Slug   string
	URL    string
	WaitMS int
}

// grabFiles 无法截图的官方看板，改为每天存原始文件（PDF 等）
func grabFile(pageURL, outPath string) error {
	return download(pageURL, outPath, 20000)
}

// listSnapshot 列出某天存档目录里的文件（用于看板展示）
func listSnapshot(day string) []string {
	dir := filepath.Join(snapDir, day)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".pdf" {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
