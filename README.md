# 小麦价格监控 · GitHub 自动版

美国 / 欧洲 / 土耳其的软麦与杜兰麦价格，每周一自动抓取、截图存档、生成看板，并发布成一个网页链接。

- 定时：**每周一 09:17（北京时间）**，也可在 Actions 页面手动点一次
- 产出：一个 Pages 链接，手机电脑都能开；仓库里同时留档 `data/prices.csv` 和每日页面截图
- 不需要服务器，公开仓库跑 Actions 和 Pages 都免费

## 一、部署（5 步，约 5 分钟）

1. 在 GitHub 新建一个 **Public** 仓库（比如 `wheat-monitor`），不要勾选初始化 README
2. 把本文件夹里**所有内容**（含 `.github` 这个隐藏目录）上传上去
   - 网页上传：Add file → Upload files → 把文件拖进去 → Commit
   - 如果 `.github` 文件夹被浏览器吞掉（隐藏目录常被忽略），用
     Add file → **Create new file**，文件名直接填 `.github/workflows/weekly.yml`，
     再把本文件夹里同名文件的内容粘进去
   - 命令行：`git init && git add -A && git commit -m init && git remote add origin <你的仓库地址> && git push -u origin main`
3. 仓库 **Settings → Actions → General → Workflow permissions**，选 **Read and write permissions** → Save
   （不设这一步，跑完没法把数据提交回来）
4. 仓库 **Settings → Pages → Build and deployment → Source**，选 **GitHub Actions**
5. 仓库 **Actions** 页面 → 左边选「小麦价格周更」→ 右边 **Run workflow** → 跑一次
   约 3-5 分钟，跑完在 Actions 里能看到 Pages 链接，形如
   `https://<用户名>.github.io/wheat-monitor/`
   （第一次跑之前这个链接是 404，属正常；启用 Pages 后要等 1-2 分钟生效）

## 二、之后会发生什么

- 每周一 09:17 自动跑：抓价 → 3 个官方页面截图 → 下载欧盟周报 PDF → 刷新看板 → 提交回仓库 → 发布
- 看板地址固定，收藏一次就行，每周自动变新
- 想改时间：编辑 `.github/workflows/weekly.yml` 里的 `cron`。**注意是 UTC**，
  北京时间减 8 小时，例如周一 08:00 → `0 0 * * 1`
- 想改成每个工作日：`17 1 * * 1-5`
  （只跑周一的话，CBOT 那条线一周只有一个点，短线波动看不到；日线要连起来得靠工作日跑）

## 三、几个要提前知道的点

| 事项 | 说明 |
|---|---|
| 内容公开 | Public 仓库意味着价格数据和官方页面截图对外可见。介意的话把仓库设为 Private，但 Private 仓库开 Pages 需要 GitHub Pro |
| 定时延迟 | GitHub 的定时任务不保证准点，通常延迟几分钟到半小时，属正常 |
| 会不会被停 | 仓库 60 天没有任何提交，定时任务会被自动停用。本流程每周都会提交数据，正常不会触发 |
| 土耳其 TMO | 官网抓不到，一直是**手动政策价**，已存在 `data/prices.csv` 里，云端跑不受影响 |
| 存档保留 | 只保留最近 8 周截图（每周约 2-3MB），更早的自动删。想留更久改 `head -n -8` 这个数字 |
| 意大利现货 | 欧盟官方报送滞后 1-2 个月，图上末端只到几个月前是正常的 |

## 四、Windows 本地版

这个仓库同时保留了本地运行所需的一切。想要「双击就跑、不依赖 GitHub」的版本，用之前那个
`小麦价格监控_Windows.zip`（内含编译好的 `wheat.exe` 和 `update.bat`），两者共用同一套 `data/prices.csv`。
