package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

var (
	root     string
	dataDir  string
	cacheDir string
	snapDir  string
	cfgPath  string
	csvPath  string
	logBuf   []string
)

func initPaths() {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	root = filepath.Dir(exe)
	// 双击运行时工作目录可能是 system32，一律以 exe 所在目录为准
	os.Chdir(root)
	dataDir = filepath.Join(root, "data")
	cacheDir = filepath.Join(dataDir, "cache")
	snapDir = filepath.Join(root, "snapshots")
	cfgPath = filepath.Join(root, "config", "sources.json")
	csvPath = filepath.Join(dataDir, "prices.csv")
	for _, d := range []string{dataDir, cacheDir, snapDir} {
		os.MkdirAll(d, 0755)
	}
}

var logMu sync.Mutex

// consoleWriter 由平台实现填入；返回 false 表示该行没写成功，需回退普通输出
var consoleWriter func(string) bool

// p 输出一行。Windows 下优先走 Unicode 控制台接口，避免代码页问题导致中文乱码或重影。
func p(a ...interface{}) {
	s := fmt.Sprintln(a...)
	if consoleWriter != nil {
		if consoleWriter(strings.ReplaceAll(s, "\n", "\r\n")) {
			return
		}
	}
	fmt.Fprint(os.Stdout, s)
}

// pf 输出不带换行的片段（进度提示用）
func pf(a ...interface{}) {
	s := fmt.Sprint(a...)
	if consoleWriter != nil {
		if consoleWriter(strings.ReplaceAll(s, "\n", "\r\n")) {
			return
		}
	}
	fmt.Fprint(os.Stdout, s)
}

func logf(format string, a ...interface{}) {
	line := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
	logMu.Lock()
	p(line)
	logBuf = append(logBuf, line)
	logMu.Unlock()
}

func saveLog() {
	p := filepath.Join(dataDir, "run_"+time.Now().Format("2006-01-02")+".log")
	os.WriteFile(p, append([]byte{0xEF, 0xBB, 0xBF}, []byte(strings.Join(logBuf, "\r\n"))...), 0644)
}

func httpBytes(url string, timeout time.Duration) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Referer", "https://www.cnbc.com/")
	c := &http.Client{Timeout: timeout}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// download 断点续传下载；官方站点限速严重（2MB 要 60s+），必须支持续传
func download(url, dst string, minBytes int64) error {
	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		var have int64
		if st, err := os.Stat(dst); err == nil {
			have = st.Size()
		}
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", ua)
		if have > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}
		c := &http.Client{Timeout: 20 * time.Minute}
		resp, err := c.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(3 * time.Second)
			continue
		}
		mode := os.O_WRONLY | os.O_CREATE
		if have > 0 && resp.StatusCode == 206 {
			mode |= os.O_APPEND
		} else {
			mode |= os.O_TRUNC
		}
		f, err := os.OpenFile(dst, mode, 0644)
		if err != nil {
			resp.Body.Close()
			return err
		}
		_, cerr := io.Copy(f, resp.Body)
		f.Close()
		resp.Body.Close()
		if cerr != nil {
			lastErr = cerr
			logf("  下载中断(%d/4)，续传中…", attempt)
			time.Sleep(3 * time.Second)
			continue
		}
		if st, err := os.Stat(dst); err == nil && st.Size() >= minBytes {
			return nil
		}
		lastErr = fmt.Errorf("文件不完整")
	}
	if st, err := os.Stat(dst); err == nil && st.Size() >= minBytes {
		return nil
	}
	return fmt.Errorf("下载失败: %v", lastErr)
}

// probe 判断页面能否访问（任何 HTTP 状态码都算"有响应"，只有连不上才算不可达）
func probe(pageURL string) bool {
	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", ua)
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func cacheFresh(path string, days int) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(st.ModTime()) < time.Duration(days)*24*time.Hour
}

func pause() {
	pf("\n按回车键退出…")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func openLocal(p string) {
	if runtime.GOOS == "windows" {
		exec.Command("cmd", "/c", "start", "", p).Start()
	} else {
		exec.Command("xdg-open", p).Start()
	}
}
