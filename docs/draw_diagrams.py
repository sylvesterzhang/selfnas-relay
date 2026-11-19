# -*- coding: utf-8 -*-
"""
用 Pillow 绘制 selfnas-relay 文档配图，全部输出到本目录。
重生成：python docs/draw_diagrams.py
"""
import os
import re
import math
from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
FONT_PATH = "C:/Windows/Fonts/msyh.ttc"
FONT_BOLD = "C:/Windows/Fonts/msyhbd.ttc"

# 配色（浅色底，适配 GitHub / 任意 Markdown 渲染器）
BG = "#FFFFFF"
INK = "#1F2937"
INK2 = "#4B5563"
MUTED = "#6B7280"
LINE = "#9CA3AF"
BLUE = "#2563EB"
BLUE_BG = "#EFF6FF"
BLUE_LN = "#93C5FD"
GREEN = "#059669"
GREEN_BG = "#ECFDF5"
GREEN_LN = "#6EE7B7"
ORANGE = "#D97706"
ORANGE_BG = "#FFFBEB"
ORANGE_LN = "#FCD34D"
PURPLE = "#7C3AED"
PURPLE_BG = "#F5F3FF"
PURPLE_LN = "#C4B5FD"
RED = "#DC2626"
RED_BG = "#FEF2F2"
RED_LN = "#FCA5A5"
CYAN_BG = "#ECFEFF"
CYAN_LN = "#67E8F9"


def font(size, bold=False):
    return ImageFont.truetype(FONT_BOLD if bold else FONT_PATH, size)


def txt(d, xy, s, size=20, fill=INK, bold=False, anchor="la"):
    d.text(xy, s, font=font(size, bold), fill=fill, anchor=anchor)


def wrap(d, s, f, max_w):
    """按像素宽度换行：把 ASCII 单词/标识符当作整体，不在中间切断；逐行处理以支持 \\n 空行。"""
    result = []
    for raw_line in s.replace("\r", "").split("\n"):
        if raw_line == "":
            result.append("")
            continue
        tokens = re.findall(r"[A-Za-z0-9_./:{}=+&|$#@'-]+|\s+|.", raw_line)
        lines, cur = [], ""
        for tok in tokens:
            t = cur + tok
            if d.textlength(t, font=f) > max_w and cur:
                lines.append(cur.rstrip()); cur = tok.lstrip()
            else:
                cur = t
        if cur:
            lines.append(cur.rstrip())
        result.extend(lines)
    return result


def para(d, xy, s, size=18, fill=INK2, max_w=400, lh=27, bold=False, anchor="lt"):
    f = font(size, bold)
    x, y = xy
    lines = wrap(d, s, f, max_w)
    for i, ln in enumerate(lines):
        d.text((x, y + i * lh), ln, font=f, fill=fill, anchor="la" if anchor == "lt" else anchor)
    return y + len(lines) * lh


def box(d, rect, title, sub="", fill=BLUE_BG, ln=BLUE, title_color=None, radius=16,
        title_size=22, sub_size=16, title_bold=True, icon=None):
    x0, y0, x1, y1 = rect
    d.rounded_rectangle(rect, radius=radius, fill=fill, outline=ln, width=2)
    pad = 16
    tc = title_color or ln
    ty = y0 + pad + 4
    if icon:
        txt(d, (x0 + pad, ty), icon, sub_size, tc, True)
        tx = x0 + pad + d.textlength(icon + " ", font=font(sub_size, True))
        txt(d, (tx, ty), title, title_size, tc, title_bold)
    else:
        txt(d, (x0 + pad, ty), title, title_size, tc, title_bold)
    if sub:
        para(d, (x0 + pad, y0 + pad + 34), sub, sub_size, INK2, max_w=(x1 - x0 - 2 * pad), lh=sub_size + 8)


def arrow(d, p1, p2, color=BLUE, width=3, head=13, dash=False, label="", label_pos=None,
          label_size=16, label_color=None, label_bg=BG):
    x1, y1 = p1; x2, y2 = p2
    ang = math.atan2(y2 - y1, x2 - x1)
    if dash:
        seg, gap = 12, 8
        dist = math.hypot(x2 - x1, y2 - y1)
        steps = int(dist / (seg + gap)) + 1
        for i in range(steps):
            a = i * (seg + gap)
            b = min(a + seg, dist)
            sx = x1 + math.cos(ang) * a; sy = y1 + math.sin(ang) * a
            ex = x1 + math.cos(ang) * b; ey = y1 + math.sin(ang) * b
            d.line([sx, sy, ex, ey], fill=color, width=width)
    else:
        d.line([x1, y1, x2, y2], fill=color, width=width)
    # 箭头三角
    ax = x2 - math.cos(ang) * head
    ay = y2 - math.sin(ang) * head
    perp = ang + math.pi / 2
    p1x = ax + math.cos(perp) * head * 0.55; p1y = ay + math.sin(perp) * head * 0.55
    p2x = ax - math.cos(perp) * head * 0.55; p2y = ay - math.sin(perp) * head * 0.55
    d.polygon([(x2, y2), (p1x, p1y), (p2x, p2y)], fill=color)
    if label:
        lx, ly = label_pos if label_pos else ((x1 + x2) / 2, (y1 + y2) / 2)
        f = font(label_size, True)
        tw = d.textlength(label, font=f)
        d.rounded_rectangle([lx - tw / 2 - 8, ly - label_size / 2 - 5, lx + tw / 2 + 8, ly + label_size / 2 + 5],
                            radius=8, fill=label_bg)
        txt(d, (lx, ly), label, label_size, label_color or color, True, "mm")


def title_block(d, W, title, subtitle):
    txt(d, (48, 34), title, 40, INK, True)
    txt(d, (48, 92), subtitle, 20, MUTED)
    d.line([48, 132, W - 48, 132], fill="#E5E7EB", width=2)


def new(W, H, title, subtitle):
    img = Image.new("RGB", (W, H), BG)
    d = ImageDraw.Draw(img)
    title_block(d, W, title, subtitle)
    return img, d


def save(img, name):
    path = os.path.join(HERE, name)
    img.save(path, "PNG")
    print(f"[OK] {name}  {img.width}x{img.height}")


# ---------------------------------------------------------------- 图1 总体架构（纯转发视角）
def draw_overview():
    W, H = 1680, 900
    img, d = new(W, H, "转发是怎么走的",
                 "Relay 和家里 NAS 之间只有一条 WebSocket 长连接，请求和响应都在这条连接上跑，没有第二条通路")

    # 三列：访问方 / Relay / 中间通道空档 / 家里 NAS
    cols = [(56, 440, "访问方", BLUE, BLUE_BG, BLUE_LN),
            (560, 1040, "selfnas-relay（公网服务器）", GREEN, GREEN_BG, GREEN_LN),
            (1240, 1624, "家里 NAS（内网 / NAT 后）", ORANGE, ORANGE_BG, ORANGE_LN)]
    for x0, x1, name, c, bg, ln in cols:
        d.rounded_rectangle([x0, 168, x1, 820], radius=22, fill=bg, outline=ln, width=2)
        txt(d, ((x0 + x1) / 2, 196), name, 23, c, True, "mm")
        d.line([x0 + 24, 232, x1 - 24, 232], fill=ln, width=2)

    # 访问方：只有浏览器
    box(d, (92, 300, 404, 470), "浏览器 / 手机", "打开 Relay 给的地址\n装都不用装，只看网页、传文件",
        BLUE_BG, BLUE, BLUE, title_size=22, sub_size=17)
    txt(d, (248, 520), "对外只看到 Relay 一个地址", 17, BLUE, True, "mm")
    txt(d, (248, 554), "https://relay/n/{node_id}/files", 15, MUTED, False, "mm")

    # Relay：一个转发框，不提控制面/管理/SQLite
    box(d, (596, 300, 1004, 470), "收到访客请求，拆成带编号的帧", "给请求贴编号 → 拆成一串小帧\n沿那条长连接送进家里 NAS\n响应帧按编号送回对应访客",
        GREEN_BG, GREEN, GREEN, title_size=21, sub_size=17)
    d.rounded_rectangle([596, 510, 1004, 600], radius=12, fill="#F0FDFA", outline="#0D9488", width=2)
    txt(d, (800, 542), "它和 NAS 之间，就这一条连接", 19, "#0F766E", True, "mm")
    txt(d, (800, 574), "转发全程都走 WebSocket，没有别的通路", 15, INK2, False, "mm")

    # 家里 NAS：只有原生 handler
    box(d, (1276, 300, 1588, 470), "原来的 HTTP 服务", "文件 / 音乐 / 视频还是原来那套程序\n它只管处理请求，不用管外面怎么连进来",
        ORANGE_BG, ORANGE, ORANGE, title_size=22, sub_size=17)
    d.rounded_rectangle([1276, 510, 1588, 600], radius=12, fill=RED_BG, outline=RED, width=2)
    txt(d, (1432, 542), "不需要公网 IP", 19, RED, True, "mm")
    txt(d, (1432, 574), "不需要端口映射 / DMZ", 15, INK2, False, "mm")
    arrow(d, (1432, 470), (1432, 510), ORANGE, 3)

    # 访客 → Relay（去）与 Relay → 访客（回）
    arrow(d, (404, 360), (594, 360), BLUE, 4, label="请求", label_pos=(499, 338), label_color=BLUE)
    arrow(d, (594, 430), (404, 430), BLUE, 4, label="响应", label_pos=(499, 452), label_color=BLUE)

    # Relay ↔ NAS：唯一一条连接，放在两列之间的空档
    d.rounded_rectangle([1052, 320, 1228, 470], radius=14, fill="#F1EFE8", outline=ORANGE, width=3)
    txt(d, (1140, 306), "WebSocket 长连接", 16, ORANGE, True, "mm")
    txt(d, (1140, 330), "仅此一条", 15, ORANGE, True, "mm")
    for y, num, color, ln in [(362, "#101", ORANGE, ORANGE), (398, "#102", ORANGE, ORANGE), (434, "#103", GREEN, GREEN)]:
        d.rounded_rectangle([1082, y, 1198, y + 28], radius=8, fill=BG, outline=ln, width=2)
        txt(d, (1140, y + 14), num, 14, color, True, "mm")
    txt(d, (1140, 492), "请求帧往右", 14, ORANGE, False, "mm")
    txt(d, (1140, 514), "响应帧往左", 14, GREEN, False, "mm")

    # 底部结论
    d.rounded_rectangle([56, 838, 1624, 882], radius=12, fill=PURPLE_BG, outline=PURPLE_LN, width=2)
    txt(d, (840, 860),
        "一句话：访客请求到 Relay → 拆成带编号的帧、沿唯一一条长连接送进 NAS → NAS 用原程序处理 → 结果贴编号原路送回。就这么简单。",
        18, PURPLE, True, "mm")
    save(img, "01-architecture-overview.png")


# ---------------------------------------------------------------- 图2 申请鉴权时序
def draw_enroll():
    W, H = 1680, 1420
    img, d = new(W, H, "接入申请与鉴权时序",
                 "信任链：request_token 校验申请归属；node_token 校验节点身份；secret 稳定持久化，支撑重启后自动重连")

    # 四条泳道（参与者列）
    lanes = [(56, 380, "SelfNAS 客户端", BLUE_BG, BLUE),
             (410, 760, "Relay 服务（HTTP API）", GREEN_BG, GREEN),
             (790, 1080, "SQLite", PURPLE_BG, PURPLE),
             (1110, 1624, "Relay 管理员（管理台）", ORANGE_BG, ORANGE)]
    for x0, x1, name, bg, c in lanes:
        d.rounded_rectangle([x0, 164, x1, 1370], radius=18, fill=bg, outline=c, width=2)
        txt(d, ((x0 + x1) / 2, 194), name, 22, c, True, "mm")
        d.line([x0 + 20, 226, x1 - 20, 226], fill=c, width=2)

    # 每个步骤：标题+说明放在箭头上方，箭头独占一行，避免文字压线
    # (arrow_y, from_x, to_x, text_x, label, note, color)
    steps = [
        (300, 218, 585, 80, "① POST /api/enrollment/requests", "提交设备名 / 主机名 / 平台 / 版本", BLUE),
        (434, 585, 935, 810, "② 生成 request_id + request_token", "明文 token 只在此刻返回一次；库中只存 SHA256", PURPLE),
        (568, 585, 218, 424, "③ 返回申请 ID、token 与 pending", "客户端把凭据保存到本机 config", GREEN),
        (702, 1360, 585, 1124, "④ 管理员在管理台点「批准 / 拒绝 / 删除」", "操作面需登录 Cookie；终态与失败语义见下方", ORANGE),
        (836, 585, 935, 810, "⑤ 批准：生成 node_id 与 node_token", "node_token = HMAC-SHA256(secret, node_id)，写入 relay_nodes", PURPLE),
        (970, 585, 218, 424, "⑥ GET /api/enrollment/requests/{id}", "带 X-Request-Token 主动查结果；拒绝或查无记录都判失败", GREEN),
        (1104, 585, 218, 424, "⑦ approved → 返回 node_id + node_token", "route_path = /n/{node_id}/，随后自动建立长连接", GREEN),
        (1238, 218, 585, 80, "⑧ WS /v1/node/connect?node_id=...", "Authorization: Bearer node_token → 校验通过置 online", BLUE),
    ]
    for ay, fx, tx_, lx, label, note, c in steps:
        txt(d, (lx, ay - 62), label, 19, c, True)
        para(d, (lx, ay - 34), note, 15, INK2, max_w=520, lh=20)
        arrow(d, (fx, ay), (tx_, ay), c, 3, head=12)

    # 提示卡放到最底部、跨整幅的说明条（不与任何箭头交叉）
    d.rounded_rectangle([56, 1300, 1624, 1370], radius=12, fill="#FFFBEB", outline=ORANGE_LN, width=2)
    txt(d, (80, 1335),
        "终态语义：pending=服务端确认有记录且未审批；rejected=服务端拒绝；查无记录/token 不匹配=404 申请不存在；expired=pending 超过 30 分钟。后三者均为失败终态。",
        17, INK2, False, "lm")

    save(img, "02-enrollment-auth-sequence.png")


# ---------------------------------------------------------------- 图3 数据面转发
def draw_dataplane():
    W, H = 1720, 1120
    img, d = new(W, H, "数据面转发流程（WebSocket 帧协议）",
                 "一个公网 HTTP 请求对应一对 request_* / response_* 帧序列；响应边收边发，Range / 206 / Content-Range 原样透传")

    # 左右两列
    d.rounded_rectangle([56, 168, 520, 1060], radius=20, fill=BLUE_BG, outline=BLUE, width=2)
    txt(d, (288, 198), "访问方 → Relay", 24, BLUE, True, "mm")
    d.rounded_rectangle([1200, 168, 1664, 1060], radius=20, fill=RED_BG, outline=RED, width=2)
    txt(d, (1432, 198), "SelfNAS mainHandler", 24, RED, True, "mm")

    # 中间帧通道
    d.rounded_rectangle([556, 168, 1164, 1060], radius=20, fill="#F0FDFA", outline="#0D9488", width=2)
    txt(d, (860, 198), "Relay 数据面（WebSocket 长连接）", 23, "#0F766E", True, "mm")

    # 左侧处理盒
    box(d, (88, 250, 488, 420), "HTTP 入口 /n/{node_id}/{path}", "解析 node_id → 查在线节点（离线返回 502）\n并发闸门：active >= maxConnections → 503\n每个请求生成随机 id，挂入 pending map",
        BLUE_BG, BLUE, BLUE, title_size=21, sub_size=16)
    box(d, (88, 470, 488, 700), "限速点 ① 上传", "r.Body 按 48KB 分片读取 → base64 → request_body\n每片按节点 bandwidth 计算等待时间（token bucket 变体）\n限的是「字节速率」，与配置值单位一致",
        ORANGE_BG, ORANGE, ORANGE, title_size=21, sub_size=16)
    box(d, (88, 750, 488, 1020), "限速点 ② 下载 + 流式回传", "response_body 解码后 throttle → w.Write → Flush\n边收边发，不等整份响应缓冲完\nHTML 特殊处理：重写根路径资源、Location 加 /n/{node_id}",
        GREEN_BG, GREEN, GREEN, title_size=21, sub_size=16)

    # 中间帧序列（请求方向）
    req = [(280, "request_start  {method, path, headers}"),
           (346, "request_body   {base64(chunk)}  × N"),
           (412, "request_end")]
    for y, s in req:
        d.rounded_rectangle([648, y, 1072, y + 52], radius=12, fill=BLUE_BG, outline=BLUE_LN, width=2)
        txt(d, (860, y + 26), s, 17, BLUE, True, "mm")
    txt(d, (860, 246), "请求方向 →", 19, BLUE, True, "mm")

    # 中间帧序列（响应方向）
    resp = [(600, "response_start  {status, headers}"),
            (666, "response_body   {base64(chunk)}  × N"),
            (732, "response_end")]
    for y, s in resp:
        d.rounded_rectangle([648, y, 1072, y + 52], radius=12, fill=GREEN_BG, outline=GREEN_LN, width=2)
        txt(d, (860, y + 26), s, 17, GREEN, True, "mm")
    txt(d, (860, 566), "← 响应方向", 19, GREEN, True, "mm")

    d.rounded_rectangle([648, 830, 1072, 1010], radius=12, fill=PURPLE_BG, outline=PURPLE_LN, width=2)
    txt(d, (860, 856), "关键不变量", 19, PURPLE, True, "mm")
    para(d, (676, 890),
         "• id 关联同一请求的收/发两路帧\n• response_end 到达后才关 done，避免 body 被截断\n• 2 小时超时 / 客户端断开即取消\n• 并发、限速对所有在线节点即时生效",
         15, INK2, max_w=380, lh=24)

    # 右侧处理盒
    box(d, (1232, 250, 1632, 470), "Relay 客户端（内置）", "收到 request_* → 还原成 http.Request\n交给既有 mainHandler.ServeHTTP\n请求体用 io.Pipe 流式灌入，不落地缓冲",
        ORANGE_BG, ORANGE, ORANGE, title_size=21, sub_size=16)
    box(d, (1232, 510, 1632, 760), "既有业务 handler 原样工作", "文件站 / 音乐 / 视频 / 管理 API\nRange 请求由它解析 → 返回 206 + Content-Range\n音频按文件头识别 Content-Type（如 .mp3 实为 FLAC）",
        RED_BG, RED, RED, title_size=21, sub_size=16)
    box(d, (1232, 800, 1632, 1020), "响应帧化回传", "ResponseWriter 拦截 status/header/body\nresponse_start 带原样 header 与 status\n每个 Write 分片 base64 后发 response_body",
        PURPLE_BG, PURPLE, PURPLE, title_size=21, sub_size=16)

    # 跨列箭头
    arrow(d, (488, 330), (648, 306), BLUE, 3)
    arrow(d, (1072, 306), (1232, 330), BLUE, 3)
    arrow(d, (1232, 620), (1072, 626), GREEN, 3)
    arrow(d, (648, 626), (488, 830), GREEN, 3)

    save(img, "03-data-plane-flow.png")


# ---------------------------------------------------------------- 图4 节点状态机
def draw_states():
    W, H = 1400, 700
    img, d = new(W, H, "节点状态机",
                 "节点状态持久化在 relay_nodes.status；只有 online 才承载公网请求，撤销后长连接被立即关闭")

    # 三状态
    box(d, (120, 260, 440, 470), "approved（已批准）", "管理员批准申请后创建\n等待客户端连上，或断线后的回落态\n可承载重新连接的凭证校验",
        GREEN_BG, GREEN, GREEN, title_size=24, sub_size=17)
    box(d, (520, 260, 880, 470), "online（在线）", "WS 长连接建立且 token 校验通过\n进入 d.nodes，可被 /n/{node_id}/ 命中\n记录 last_seen_at",
        BLUE_BG, BLUE, BLUE, title_size=24, sub_size=17)
    box(d, (980, 260, 1300, 470), "revoked（已撤销）", "管理员撤销节点或删除已批准申请\n长连接立即关闭、pending 请求全部结束\ntoken 永不再被校验通过",
        RED_BG, RED, RED, title_size=24, sub_size=17)

    arrow(d, (440, 330), (520, 330), BLUE, 4, label="WS 连接校验通过", label_pos=(480, 246), label_color=BLUE, label_size=15)
    arrow(d, (520, 410), (440, 410), ORANGE, 4, label="连接断开", label_pos=(480, 518), label_color=ORANGE, label_size=15)
    arrow(d, (880, 330), (980, 330), RED, 4, label="撤销节点 / 删除申请", label_pos=(930, 246), label_color=RED, label_size=14)
    d.rounded_rectangle([120, 600, 1300, 662], radius=12, fill="#F9FAFB", outline="#E5E7EB", width=2)
    txt(d, (710, 630),
        "重启恢复：节点在 sqlite 中仍是 approved/online，secret 不变 → node_token 可重新派生 → 客户端重连后回到 online。撤销是唯一让凭证彻底失效的操作。",
        17, INK2, False, "mm")
    save(img, "04-node-state-machine.png")


# ---------------------------------------------------------------- 图5 凭证信任链
def draw_trust():
    W, H = 1620, 940
    img, d = new(W, H, "凭证与信任链",
                 "两类 token 都只以哈希落库；secret 是首次启动随机生成并持久化的稳定根密钥，决定节点凭证能否跨重启复用")

    # 上半：request_token
    d.rounded_rectangle([56, 168, 1564, 500], radius=18, fill=BLUE_BG, outline=BLUE, width=2)
    txt(d, (810, 198), "申请凭证链（确认「这条申请属于谁」）", 23, BLUE, True, "mm")
    steps1 = [
        (110, 250, 470, 420, "① 客户端提交申请", "Relay 生成 request_id（18B）+ request_token（32B 随机）", BLUE),
        (500, 250, 860, 420, "② 明文只返回一次", "创建响应里回传 request_token；此后服务端的任何查询接口都不再返回它", PURPLE),
        (890, 250, 1250, 420, "③ 库中只存 SHA256", "relay_enrollment_requests.request_token_hash", ORANGE),
        (1280, 250, 1520, 420, "④ 查询校验", "GET 带 X-Request-Token\nsha(提供值) == 库中 hash", GREEN),
    ]
    for x0, y0, x1, y1, t, s, c in steps1:
        box(d, (x0, y0, x1, y1), t, s, BG, c, c, title_size=18, sub_size=15, radius=12)
    arrow(d, (470, 330), (500, 330), BLUE, 3)
    arrow(d, (860, 330), (890, 330), BLUE, 3)
    arrow(d, (1250, 330), (1280, 330), BLUE, 3)

    # 下半：node_token
    d.rounded_rectangle([56, 530, 1564, 900], radius=18, fill=GREEN_BG, outline=GREEN, width=2)
    txt(d, (810, 560), "节点凭证链（确认「这个节点有资格连入」）", 23, GREEN, True, "mm")
    steps2 = [
        (110, 612, 470, 782, "① 稳定 secret", "首次启动随机 32B，base64 存入 relay_meta.token_secret，重启不变", PURPLE),
        (500, 612, 860, 782, "② 批准时派生", "node_id = 18B 随机；node_token = HMAC-SHA256(secret, node_id)", GREEN),
        (890, 612, 1250, 782, "③ 只存 token 哈希", "relay_nodes.token_hash = sha(node_token)；明文交给客户端", ORANGE),
        (1280, 612, 1520, 782, "④ 连接校验", "WS Authorization: Bearer node_token\n恒定时间比较 + status != revoked", BLUE),
    ]
    for x0, y0, x1, y1, t, s, c in steps2:
        box(d, (x0, y0, x1, y1), t, s, BG, c, c, title_size=18, sub_size=15, radius=12)
    arrow(d, (470, 690), (500, 690), GREEN, 3)
    arrow(d, (860, 690), (890, 690), GREEN, 3)
    arrow(d, (1250, 690), (1280, 690), GREEN, 3)

    txt(d, (810, 856),
        "结论：只要 SQLite 和 secret 保留、节点未撤销，客户端保存的 node_id + node_token 在双方重启后仍然有效，可自动恢复连接。",
        19, INK2, True, "mm")
    save(img, "05-credential-trust-chain.png")


# ---------------------------------------------------------------- 图6 高速公路比喻
def draw_highway():
    W, H = 1680, 660
    img, d = new(W, H, "一条长连接，就是一条高速公路",
                 "这张图只画两样东西：两端是谁，以及它们之间那条唯一的长连接；贴单号等细节放到正文里讲")

    # 左端主体：Relay 服务端
    box(d, (56, 200, 470, 452), "Relay 服务端（有公网 IP）",
        "外面访客的请求先到这里\n由它把请求送上这条高速公路\n回来的结果也由它交还给访客",
        GREEN_BG, GREEN, GREEN, title_size=25, sub_size=18, radius=18)
    txt(d, (263, 486), "公网服务器，谁都访问得到", 17, GREEN, True, "mm")

    # 右端主体：家里 NAS
    box(d, (1210, 200, 1624, 452), "家里 NAS（内网 / NAT 后）",
        "请求顺着这条路送进来\n用它原来的程序处理\n结果再顺着这条路送回去",
        ORANGE_BG, ORANGE, ORANGE, title_size=25, sub_size=18, radius=18)
    txt(d, (1417, 486), "没有公网 IP，也不用端口映射", 17, ORANGE, True, "mm")

    # 中间：唯一一条高速公路（WebSocket 长连接）
    road_y0, road_y1 = 262, 392
    d.rounded_rectangle([502, road_y0, 1178, road_y1], radius=30, fill="#F1EFE8", outline="#B4B2A9", width=3)
    mid = (road_y0 + road_y1) // 2
    for x in range(540, 1150, 92):
        d.line([x, mid - 4, x + 46, mid - 4], fill="#B4B2A9", width=3)
        d.line([x, mid + 24, x + 46, mid + 24], fill="#B4B2A9", width=3)
    txt(d, (840, 232), "WebSocket 长连接 = 这条高速公路（就这一条，一直连着不断）", 20, INK, True, "mm")

    # 两端主体与路连起来
    d.line([470, 327, 502, 327], fill=GREEN, width=3)
    d.line([1178, 327, 1210, 327], fill=ORANGE, width=3)

    # 路上只表达方向，不堆编号/步骤卡片
    arrow(d, (520, 300), (1160, 300), BLUE, 4, label="访客请求送进去 →",
          label_pos=(840, 300), label_color=BLUE, label_size=18, label_bg="#F1EFE8")
    arrow(d, (1160, 356), (520, 356), GREEN, 4, label="← 处理结果送回来",
          label_pos=(840, 356), label_color=GREEN, label_size=18, label_bg="#F1EFE8")

    # 底部一句话
    d.rounded_rectangle([56, 540, 1624, 614], radius=14, fill=PURPLE_BG, outline=PURPLE_LN, width=2)
    txt(d, (840, 577),
        "关键点：NAS 在 NAT 后面、没有公网 IP，靠的是它自己主动连出来这条路；路是双向的，请求能进去，结果能出来。",
        19, PURPLE, True, "mm")
    save(img, "06-highway-metaphor.png")


# ---------------------------------------------------------------- 图7 拆包组包对照
def draw_packet():
    W, H = 1680, 940
    img, d = new(W, H, "拆包、组包、贴单号",
                 "请求和响应都不是整包传的，而是拆成一片片、带着同一个编号，在长连接上流过")

    # 左：原始 HTTP 请求
    box(d, (56, 180, 470, 430), "访客发来的原始请求", "GET /files/电影.mp4 HTTP/1.1\nRange: bytes=0-1048575\nHost: relay.example.com\n（可能还有上传的 body）",
        BLUE_BG, BLUE, BLUE, title_size=21, sub_size=17)
    txt(d, (263, 462), "Relay 把它「拆成包」↓", 18, BLUE, True, "mm")

    # 中上：请求帧
    d.rounded_rectangle([520, 180, 1160, 430], radius=16, fill=GREEN_BG, outline=GREEN, width=2)
    txt(d, (840, 206), "请求方向的包（都贴 #101）", 20, GREEN, True, "mm")
    req_frames = [
        (230, "request_start：方法、网址、请求头"),
        (292, "request_body：数据切片（每片 48KB）× N"),
        (354, "request_end：请求体发完了"),
    ]
    for y, s in req_frames:
        d.rounded_rectangle([548, y, 1132, y + 50], radius=10, fill=BG, outline=GREEN_LN, width=2)
        txt(d, (840, y + 25), s, 17, INK, False, "mm")

    # 中下：响应帧
    d.rounded_rectangle([520, 490, 1160, 740], radius=16, fill=GREEN_BG, outline=GREEN, width=2)
    txt(d, (840, 516), "响应方向的包（还是贴 #101）", 20, GREEN, True, "mm")
    resp_frames = [
        (540, "response_start：状态码（如 206）、响应头"),
        (602, "response_body：数据切片 × N"),
        (664, "response_end：响应发完了"),
    ]
    for y, s in resp_frames:
        d.rounded_rectangle([548, y, 1132, y + 50], radius=10, fill=BG, outline=GREEN_LN, width=2)
        txt(d, (840, y + 25), s, 17, INK, False, "mm")

    # 右：关键点
    box(d, (1190, 180, 1624, 740), "为什么要这么拆？",
        "• 长连接一次只能「写一串字节」，\n  所以要把请求变成一串带编号的小消息。\n\n"
        "• 二进制数据用 base64 装进 JSON。\n\n"
        "• 状态码、响应头原样带回来，\n  所以浏览器要的 Range / 206 不会丢。\n\n"
        "• 边收边转发，不等整部电影下完，\n  所以拖进度条、听歌都能边下边播。",
        ORANGE_BG, ORANGE, ORANGE, title_size=21, sub_size=17)

    d.rounded_rectangle([56, 770, 1624, 900], radius=12, fill=PURPLE_BG, outline=PURPLE_LN, width=2)
    txt(d, (840, 800), "NAS 收到这些包后，按 #101 拼回一个完整请求 → 交给它原来的处理程序 →", 19, INK2, False, "mm")
    txt(d, (840, 836), "把结果同样拆成包、贴回 #101 送回来 → Relay 按 #101 找到当初的访客 → 把响应写给他。", 19, INK2, False, "mm")
    txt(d, (840, 874), "请求头、Range、状态码都原样透传 —— 这就是视频能拖动、音乐能边下边播的原因。", 17, PURPLE, True, "mm")
    save(img, "07-packet-legend.png")


# ---------------------------------------------------------------- 图8 一条公路多个等待槽位
def draw_slots():
    W, H = 1680, 780
    img, d = new(W, H, "一条公路，很多个等待窗口",
                 "最容易被搞混的一点：编号对应的是「等待哪个请求」的小格子，不是「连接」。连接永远只有一条")

    # 左：访客请求
    for y, name, num in [(220, "访客请求 A", "#101"), (350, "访客请求 B", "#102"), (480, "访客请求 C", "#103")]:
        box(d, (56, y, 300, y + 90), name, "编号 " + num, BLUE_BG, BLUE, BLUE,
            title_size=19, sub_size=16, radius=12)

    # 中：等待槽位
    for y, num, who in [(220, "#101", "等 A 的响应"), (350, "#102", "等 B 的响应"), (480, "#103", "等 C 的响应")]:
        box(d, (400, y, 700, y + 90), "等待槽位 " + num, who, GREEN_BG, GREEN, GREEN,
            title_size=19, sub_size=16, radius=12)

    for y in [265, 395, 525]:
        arrow(d, (300, y), (398, y), BLUE, 3)

    # 汇聚到一条公路
    d.rounded_rectangle([760, 210, 1120, 590], radius=20, fill="#F1EFE8", outline="#B4B2A9", width=2)
    txt(d, (940, 244), "WebSocket 长连接", 21, INK, True, "mm")
    txt(d, (940, 276), "全程只有这一条", 17, INK2, False, "mm")
    for y in [330, 400, 470]:
        d.rounded_rectangle([796, y - 18, 1084, y + 18], radius=10, fill=BG, outline=ORANGE_LN, width=2)
    txt(d, (940, 330), "#101 的包", 16, ORANGE, True, "mm")
    txt(d, (940, 400), "#102 的包", 16, ORANGE, True, "mm")
    txt(d, (940, 470), "#103 的包", 16, ORANGE, True, "mm")
    for y in [265, 395, 525]:
        arrow(d, (700, y), (758, y), GREEN, 3)

    # 右：NAS
    box(d, (1200, 290, 1624, 510), "家里 NAS", "拆包 → 按网址交给对应处理程序\n→ 结果贴同一个编号送回来",
        PURPLE_BG, PURPLE, PURPLE, title_size=22, sub_size=18, radius=16)
    arrow(d, (1120, 400), (1198, 400), ORANGE, 3)

    # 底部锁定
    d.rounded_rectangle([56, 620, 1624, 742], radius=12, fill=PURPLE_BG, outline=PURPLE_LN, width=2)
    txt(d, (840, 656), "一句话锁定：编号对应的是「内存里等哪个请求」的小格子（等待队列），不是「连接」。", 20, PURPLE, True, "mm")
    txt(d, (840, 696), "连接只有一条；每个访客请求各占一个小格子。响应回来按编号找到自己的格子，数据就被投进对应请求，交还给那个人。", 17, INK2, False, "mm")
    txt(d, (840, 726), "别再说「连接池里找连接」——准确说法是「按编号找到那个请求的等待队列」。", 17, INK2, False, "mm")
    save(img, "08-one-connection-many-slots.png")


if __name__ == "__main__":
    print("start drawing selfnas-relay diagrams ...")
    draw_overview()
    draw_enroll()
    draw_dataplane()
    draw_states()
    draw_trust()
    draw_highway()
    draw_packet()
    draw_slots()
    print("done.")
