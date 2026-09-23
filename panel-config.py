#!/usr/bin/env python3
"""Local root-only management helper. No private key is copied or uploaded."""
import fcntl
import ipaddress
import json
import os
from pathlib import Path
import re
import socket
import ssl
import subprocess
import sys
import time
import urllib.request

DATA = Path('/var/lib/fanout-integrated')
UNIT = 'fanout-integrated.service'

def run(*args):
    return subprocess.check_output(args, stderr=subprocess.STDOUT).decode().strip()

def load(name, default=None):
    p = DATA / name
    return json.loads(p.read_text()) if p.exists() else (default or {})

def atomic(path, content):
    tmp = path.with_name(path.name + '.tmp')
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'wb') as f:
        f.write(content)
    os.replace(tmp, path)

def valid_domain(domain):
    domain = domain.strip().lower().rstrip('.')
    if len(domain) > 253 or '.' not in domain or any(not re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', x) for x in domain.split('.')):
        raise ValueError('请填写纯域名，例如 panel.example.com，不带 https://、端口或路径')
    try:
        ipaddress.ip_address(domain)
    except ValueError:
        return domain
    raise ValueError('请填写域名，不是 IP')

def check(cfg):
    domain = valid_domain(cfg['domain'])
    cert, key = cfg['cert'], cfg['key']
    if not Path(cert).is_absolute() or not Path(key).is_absolute():
        raise ValueError('证书和私钥必须填写绝对路径')
    # SSLContext verifies PEM readability and that the key matches the certificate.
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(cert, key)
    san = run('openssl', 'x509', '-in', cert, '-noout', '-ext', 'subjectAltName')
    if 'DNS:' not in san:
        raise ValueError('证书需要包含域名 SAN 扩展，不能只设置 CN')
    out = run('openssl', 'x509', '-in', cert, '-noout', '-checkhost', domain)
    if 'does match certificate' not in out:
        raise ValueError('证书与域名不匹配')
    run('openssl', 'x509', '-in', cert, '-noout', '-checkend', '0')
    dates = run('openssl', 'x509', '-in', cert, '-noout', '-dates')
    start = next(x.split('=', 1)[1] for x in dates.splitlines() if x.startswith('notBefore='))
    if ssl.cert_time_to_seconds(start) > time.time():
        raise ValueError('证书尚未生效，请检查证书和系统时间')
    return dates

def health():
    settings = load('settings.json', {'port': 8899})
    addr = settings.get('listen_addr') or '127.0.0.1'
    if addr in ('0.0.0.0', '::'):
        addr = '127.0.0.1' if addr == '0.0.0.0' else '::1'
    if ':' in addr:
        addr = '[' + addr + ']'
    base = (DATA / 'basepath').read_text().strip()
    # Local readiness only; no password, cookie or external request is sent.
    context = ssl._create_unverified_context()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
    with opener.open(f'https://{addr}:{settings["port"]}{base}/', timeout=2) as r:
        return r.status == 200

def info():
    settings = load('settings.json', {'port': 8899})
    cfg = load('tls.json')
    host = cfg.get('domain')
    if not host:
        try:
            host = run('curl', '-4fsS', '--max-time', '5', 'https://api.ipify.org')
            ipaddress.ip_address(host)
        except Exception:
            host = '<VPS公网IP>'
    base = (DATA / 'basepath').read_text().strip()
    port = settings.get('port', 8899)
    print(f'管理地址：https://{host}' + (f':{port}' if port != 443 else '') + base + '/')
    print('管理口令：' + (DATA / 'password').read_text().strip())
    if cfg:
        print(check(cfg))
        print('证书续期后自动读取原路径；若路径改变，请重新执行 fanoutctl domain。')
    else:
        print(run('openssl', 'x509', '-in', str(DATA / 'web.crt'), '-noout', '-fingerprint', '-sha256'))
        print('当前为自签证书。使用已有域名证书：sudo fanoutctl domain')

def configure(args):
    if len(args) not in (0, 3, 4):
        raise ValueError('用法：fanoutctl domain 域名 证书完整链路径 私钥路径 [端口]；不带参数进入引导')
    if not args:
        args = [input('面板域名：'), input('证书完整链路径（fullchain.pem）：'), input('私钥路径（privkey.pem）：')]
    domain, cert, key = args[:3]
    cfg = {'domain': valid_domain(domain), 'cert': cert, 'key': key}
    print(check(cfg))
    # Environment overrides would otherwise silently bypass this configuration.
    env = Path('/etc/default/fanout-integrated')
    if env.exists() and re.search(r'^\s*FANOUT_TLS_(CERT|KEY|DOMAIN)\s*=', env.read_text(), re.M):
        raise ValueError('请先移除 /etc/default/fanout-integrated 内旧的 FANOUT_TLS_* 设置，再使用域名引导')
    settings = load('settings.json', {'port': 8899, 'listen_addr': ''})
    if len(args) == 4:
        port = int(args[3])
        if not 1 <= port <= 65535:
            raise ValueError('端口范围 1–65535')
        if port != settings['port']:
            with socket.socket() as sock:
                sock.bind((settings.get('listen_addr') or '0.0.0.0', port))
        settings['port'] = port
    old = {name: (DATA / name).read_bytes() if (DATA / name).exists() else None for name in ('tls.json', 'settings.json')}
    try:
        atomic(DATA / 'tls.json', json.dumps(cfg).encode())
        atomic(DATA / 'settings.json', json.dumps(settings).encode())
        subprocess.run(['systemctl', 'restart', UNIT], check=True)
        for _ in range(75):
            try:
                if health():
                    break
            except Exception:
                pass
            time.sleep(1)
        else:
            raise RuntimeError('面板未能正常启动')
    except Exception:
        for name, content in old.items():
            if content is None:
                (DATA / name).unlink(missing_ok=True)
            else:
                atomic(DATA / name, content)
        subprocess.run(['systemctl', 'restart', UNIT], check=False)
        raise RuntimeError('配置失败，已恢复原设置。请检查 fanoutctl log')
    print('域名配置完成。请确保 DNS A 记录指向 VPS，并放行面板端口。')
    info()

def proxy():
    cfg = load('tls.json')
    if not cfg:
        raise ValueError('先运行 fanoutctl domain 配置证书，再生成反代示例')
    check(cfg)
    port = load('settings.json')['port']
    if port == 443:
        raise ValueError('面板当前已占用 443。反代部署请先把面板改为 8899 等空闲端口')
    domain = cfg['domain']
    print('以下为示例，不会修改现有网站。请选择 Nginx 或 Caddy 其中一种，合并到现有配置后检查再加载。')
    print('前提：面板监听 127.0.0.1 或所有 IPv4 网卡；使用公网受信任的完整证书链。保留原随机访问路径。')
    print(f'''\n# Nginx（填入证书路径）
server {{
    listen 443 ssl;
    server_name {domain};
    ssl_certificate /替换为/fullchain.pem;
    ssl_certificate_key /替换为/privkey.pem;
    location / {{
        proxy_pass https://127.0.0.1:{port};
        proxy_set_header Host $http_host;
        proxy_ssl_server_name on;
        proxy_ssl_name {domain};
        proxy_ssl_verify on;
        proxy_ssl_trusted_certificate /etc/ssl/certs/ca-certificates.crt;
    }}
}}
\n# Caddy（填入已有证书路径）
{domain} {{
    tls /替换为/fullchain.pem /替换为/privkey.pem
    reverse_proxy https://127.0.0.1:{port} {{
        transport http {{
            tls_server_name {domain}
        }}
    }}
}}''')
    print('反代验证成功后，可在面板高级设置把监听地址改为 127.0.0.1，关闭外部对面板内部端口的访问。')

def main():
    if os.geteuid() != 0:
        raise ValueError('请使用 sudo fanoutctl 执行此命令')
    cmd = sys.argv[1] if len(sys.argv) > 1 else 'info'
    if cmd == 'health':
        if not health():
            raise RuntimeError('not ready')
    elif cmd == 'info':
        info()
    elif cmd == 'domain':
        with open('/run/fanout-integrated-install.lock', 'w') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            configure(sys.argv[2:])
    elif cmd == 'cert-check':
        cfg = load('tls.json')
        if not cfg:
            raise ValueError('尚未配置域名证书，请运行 sudo fanoutctl domain')
        print(check(cfg))
        print('证书与私钥匹配，域名和有效期检查通过。')
    elif cmd == 'proxy':
        proxy()
    else:
        raise ValueError('不支持的管理命令')

if __name__ == '__main__':
    try:
        main()
    except Exception as e:
        if len(sys.argv) < 2 or sys.argv[1] != 'health':
            print('错误：' + str(e), file=sys.stderr)
        sys.exit(1)
