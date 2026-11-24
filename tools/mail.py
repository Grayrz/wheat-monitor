#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
从 data/prices.csv 生成周报邮件并发送。

只用标准库，GitHub Actions 的 ubuntu runner 上直接能跑，不需要 pip install。

环境变量（都从 Actions Secrets 注入）：
  SMTP_HOST   如 smtp.qq.com / smtp.163.com
  SMTP_PORT   465（SSL）
  SMTP_USER   发件邮箱地址
  SMTP_PASS   授权码（不是登录密码）
  MAIL_TO     收件地址，可逗号分隔多个
  MAIL_FROM   可选，默认用 SMTP_USER
  SITE_URL    可选，看板地址
  DRY_RUN     设为 1 时只打印不发送，用于本地验证
"""

import csv
import html
import os
import smtplib
import ssl
import sys
import time
from datetime import date, datetime
from email.message import EmailMessage
from email.utils import formataddr

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CSV_PATH = os.path.join(ROOT, "data", "prices.csv")

# 展示顺序：软麦在前，杜兰在后，与看板一致
ORDER = [
    "cbot_srw",
    "eu_rouen_soft",
    "us_gulf_srw",
    "it_bologna_soft",
    "it_bologna_durum",
    "fr_la_nouvelle_durum",
    "ca_cwad",
    "matif_milling",
]

# 单位换算：美分/蒲式耳 → 美元/吨
CENTS_BU_TO_USD_T = 0.367437


def read_rows(path):
    for enc in ("utf-8-sig", "utf-8", "gbk"):
        try:
            with open(path, encoding=enc, newline="") as f:
                return list(csv.DictReader(f))
        except UnicodeDecodeError:
            continue
    raise SystemExit("prices.csv 编码无法识别")


def to_float(v):
    try:
        return float(str(v).strip())
    except (TypeError, ValueError):
        return None


def build_view(rows):
    """每个品种取最新一条，并算出与上一条的变动。"""
    by_key = {}
    for r in rows:
        k = (r.get("key") or "").strip()
        if not k:
            continue
        by_key.setdefault(k, []).append(r)

    out = []
    keys = [k for k in ORDER if k in by_key] + [k for k in by_key if k not in ORDER]
    for k in keys:
        rs = sorted(by_key[k], key=lambda r: (r.get("date") or "", r.get("captured_at") or ""))
        # 最新一条（不论 status，need_review 也要显示，否则表格里会缺行）
        last = rs[-1]
        prev = None
        for cand in reversed(rs[:-1]):
            if cand.get("date") != last.get("date"):
                prev = cand
                break

        usd = to_float(last.get("price_usd_mt"))
        prev_usd = to_float(prev.get("price_usd_mt")) if prev else None
        chg = usd - prev_usd if (usd is not None and prev_usd) else None
        pct = (chg / prev_usd * 100.0) if (chg is not None and prev_usd) else None

        out.append(
            {
                "key": k,
                "name": (last.get("name") or k).strip(),
                "market": (last.get("market") or "").strip(),
                "grade": (last.get("grade") or "").strip(),
                "date": (last.get("date") or "").strip(),
                "price": to_float(last.get("price")),
                "currency": (last.get("currency") or "").strip(),
                "unit": (last.get("unit") or "").strip(),
                "usd": usd,
                "chg": chg,
                "pct": pct,
                "status": (last.get("status") or "").strip(),
            }
        )
    return out


def fmt_price(v, cur, unit):
    if v is None:
        return "—"
    if cur == "USD" and unit == "美分/蒲":
        return f"{v:,.2f} 美分/蒲"
    # 邮件客户端字体常缺 ₺ 这类符号，里拉直接写 TRY 最保险
    unit_txt = "吨" if unit.endswith("/t") else (unit.split("/")[-1] if "/" in unit else unit)
    sym = {"USD": "$", "EUR": "€"}.get(cur)
    if sym:
        return f"{sym}{v:,.2f}/{unit_txt}"
    return f"{v:,.2f} {cur}/{unit_txt}"


def fmt_pct(p):
    if p is None:
        return "—"
    return f"{p:+.2f}%"


def pct_color(p):
    if p is None:
        return "#8a93a0"
    return "#c62828" if p > 0 else ("#2e7d32" if p < 0 else "#8a93a0")


def lag_days(d, today):
    try:
        return (today - datetime.strptime(d, "%Y-%m-%d").date()).days
    except Exception:
        return None


def build_text(view, site_url, today):
    """纯文本兜底版。中文宽度是英文两倍，用空格对齐必定错行，
    所以改成单行一条的列表式，怎么显示都不歪。"""
    lines = [f"小麦价格周报 · {today.isoformat()}（周一）", ""]
    for v in view:
        if v["status"] == "need_review":
            lines.append(f"· {v['name']}：需人工核对（见当日截图存档）")
            continue
        usd = f"（≈{v['usd']:,.0f} USD/吨）" if v["usd"] else ""
        chg = fmt_pct(v["pct"])
        lines.append(f"· {v['name']}：{fmt_price(v['price'], v['currency'], v['unit'])}{usd}　{chg}　{v['date']}")
    lines += [
        "",
        "涨跌为与上一次记录相比。",
        "",
        f"完整看板（四张走势图 + 报价表 + 每日截图存档）：{site_url}",
    ]
    return "\n".join(lines)


def build_html(view, site_url, today, warn=""):
    """红涨绿跌，跟国内习惯一致；深色表头在邮件客户端里兼容性最好。"""
    warn_html = (
        f'<div style="margin-top:12px;padding:10px 12px;background:#fff4e5;border-left:3px solid #e8a33d;'
        f'border-radius:4px;font-size:12.5px;color:#8a5a00;line-height:1.6;">{warn}</div>'
        if warn
        else ""
    )
    trs = []
    for v in view:
        if v["status"] == "need_review":
            trs.append(
                f'<tr><td style="padding:9px 10px;border-bottom:1px solid #eceff3;">'
                f'<b style="color:#26303d;">{html.escape(v["name"])}</b>'
                f'<div style="font-size:11px;color:#8a93a0;margin-top:2px;">{html.escape(v["market"])}</div></td>'
                f'<td colspan="3" style="padding:9px 10px;border-bottom:1px solid #eceff3;'
                f'text-align:left;color:#b8860b;font-size:12.5px;">需人工核对 · 见当日截图</td></tr>'
            )
            continue

        lag = lag_days(v["date"], today)
        lag_txt = f'<div style="font-size:10.5px;color:#a2a9b4;margin-top:2px;">滞后 {lag} 天</div>' if lag and lag >= 9 else ""
        trs.append(
            "<tr>"
            f'<td style="padding:9px 10px;border-bottom:1px solid #eceff3;">'
            f'<b style="color:#26303d;">{html.escape(v["name"])}</b>'
            f'<div style="font-size:11px;color:#8a93a0;margin-top:2px;">{html.escape(v["market"])}</div></td>'
            f'<td style="padding:9px 10px;border-bottom:1px solid #eceff3;text-align:right;'
            f'font-variant-numeric:tabular-nums;color:#26303d;">{fmt_price(v["price"], v["currency"], v["unit"])}'
            f'<div style="font-size:10.5px;color:#a2a9b4;margin-top:2px;">≈ {v["usd"]:,.0f} USD/t</div></td>'
            f'<td style="padding:9px 10px;border-bottom:1px solid #eceff3;text-align:right;'
            f'font-variant-numeric:tabular-nums;font-weight:600;color:{pct_color(v["pct"])};">{fmt_pct(v["pct"])}</td>'
            f'<td style="padding:9px 10px;border-bottom:1px solid #eceff3;text-align:right;'
            f'font-size:12px;color:#5d6675;white-space:nowrap;">{v["date"]}{lag_txt}</td>'
            "</tr>"
        )

    # 一句摘要：CBOT 与美湾（最能代表盘面和大宗成交）
    bits = []
    for k, label in (("cbot_srw", "CBOT 软红冬麦"), ("us_gulf_srw", "美湾 SRW"), ("eu_rouen_soft", "鲁昂软麦")):
        for v in view:
            if v["key"] == k and v["pct"] is not None:
                bits.append(f'{label} <b style="color:{pct_color(v["pct"])};">{fmt_pct(v["pct"])}</b>')
                break
    summary = "　·　".join(bits) if bits else "本周数据见下表"

    return f"""<!doctype html><html><body style="margin:0;padding:0;background:#f4f6f9;">
<div style="max-width:640px;margin:0 auto;padding:22px 16px;font-family:-apple-system,'Segoe UI','Microsoft YaHei',sans-serif;color:#26303d;">

  <div style="font-size:12px;letter-spacing:.08em;color:#8a93a0;text-transform:uppercase;">Wheat Monitor</div>
  <h1 style="margin:6px 0 4px;font-size:21px;font-weight:600;letter-spacing:-.01em;">小麦价格周报</h1>
  <div style="font-size:13px;color:#5d6675;">{today.isoformat()}（周一）　·　{summary}</div>
  {warn_html}

  <div style="background:#fff;border:1px solid #e6eaf0;border-radius:10px;margin-top:16px;overflow:hidden;">
    <table style="width:100%;border-collapse:collapse;font-size:13px;">
      <thead><tr style="background:#f7f9fc;">
        <th style="padding:9px 10px;text-align:left;font-size:11.5px;font-weight:500;color:#8a93a0;">品种</th>
        <th style="padding:9px 10px;text-align:right;font-size:11.5px;font-weight:500;color:#8a93a0;">最新价</th>
        <th style="padding:9px 10px;text-align:right;font-size:11.5px;font-weight:500;color:#8a93a0;">涨跌</th>
        <th style="padding:9px 10px;text-align:right;font-size:11.5px;font-weight:500;color:#8a93a0;">数据日期</th>
      </tr></thead>
      <tbody>{''.join(trs)}</tbody>
    </table>
  </div>

  <div style="margin-top:18px;">
    <a href="{site_url}" style="display:inline-block;background:#1f6feb;color:#fff;text-decoration:none;
       padding:11px 20px;border-radius:8px;font-size:14px;font-weight:500;">打开完整看板 →</a>
  </div>
  <div style="margin-top:9px;font-size:12px;color:#8a93a0;word-break:break-all;">{site_url}</div>

  <div style="margin-top:20px;padding-top:14px;border-top:1px solid #e6eaf0;font-size:11.5px;color:#a2a9b4;line-height:1.7;">
    看板含软麦、杜兰麦、现货基差、杜兰/软麦比价四张走势图，以及每日页面截图存档。<br>
    意大利博洛尼亚与法国新港的官方报价为周/月度发布，滞后 1–2 月属正常，图上以虚线区分。<br>
    本邮件由 GitHub Actions 每周一自动发送。
  </div>
</div></body></html>"""


def send(subject, text, html_body, cfg):
    msg = EmailMessage()
    msg["Subject"] = subject
    msg["From"] = formataddr(("小麦价格监控", cfg["from"]))
    msg["To"] = ", ".join(cfg["to"])
    msg.set_content(text)
    msg.add_alternative(html_body, subtype="html")

    ctx = ssl.create_default_context()
    last_err = None
    for attempt in range(3):
        try:
            if cfg["port"] == 465:
                with smtplib.SMTP_SSL(cfg["host"], cfg["port"], context=ctx, timeout=30) as s:
                    s.login(cfg["user"], cfg["password"])
                    s.send_message(msg)
            else:
                with smtplib.SMTP(cfg["host"], cfg["port"], timeout=30) as s:
                    s.starttls(context=ctx)
                    s.login(cfg["user"], cfg["password"])
                    s.send_message(msg)
            return True
        except Exception as e:
            last_err = e
            print(f"第 {attempt + 1} 次发送失败：{e}", file=sys.stderr)
            time.sleep(5)
    print(f"发送最终失败：{last_err}", file=sys.stderr)
    return False


def main():
    today = date.today()
    rows = read_rows(CSV_PATH)
    view = build_view(rows)
    if not view:
        raise SystemExit("prices.csv 里没有可用数据")

    site_url = os.environ.get("SITE_URL", "").strip() or "https://github.com"

    # 采集失败也要发信，但必须在标题里说清楚，别让人把旧价格当新价格
    status = (os.environ.get("RUN_STATUS") or "success").strip()
    failed = status not in ("success", "")
    subject = f"小麦价格周报 · {today.isoformat()}"
    warn = ""
    if failed:
        subject = f"⚠️ 本周采集异常 · {subject}"
        warn = (
            f"本次自动采集没有正常完成（状态：{html.escape(status)}），"
            "下表是上一次成功抓到的数据，价格可能已过期。"
            "常见原因是欧盟官网限速超时，可稍后在 GitHub Actions 页面手动重跑。"
        )

    text = build_text(view, site_url, today)
    if failed:
        text = "[注意] 本次自动采集未正常完成，以下为上一次成功抓到的数据。\n\n" + text
    html_body = build_html(view, site_url, today, warn)

    if os.environ.get("DRY_RUN") == "1":
        print("=== SUBJECT ===")
        print(subject)
        print("=== TEXT ===")
        print(text)
        with open("/tmp/mail_preview.html", "w", encoding="utf-8") as f:
            f.write(html_body)
        print("=== HTML 已写入 /tmp/mail_preview.html ===")
        return 0

    cfg = {
        "host": os.environ.get("SMTP_HOST", "").strip(),
        "port": int(os.environ.get("SMTP_PORT", "465")),
        "user": os.environ.get("SMTP_USER", "").strip(),
        "password": os.environ.get("SMTP_PASS", ""),
        "from": os.environ.get("MAIL_FROM", "").strip() or os.environ.get("SMTP_USER", "").strip(),
        "to": [x.strip() for x in os.environ.get("MAIL_TO", "").split(",") if x.strip()],
    }
    missing = [k for k in ("host", "user", "password") if not cfg[k]]
    if missing or not cfg["to"]:
        raise SystemExit(f"缺少配置：{missing or 'MAIL_TO'}")

    ok = send(subject, text, html_body, cfg)
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
