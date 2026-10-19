#!/usr/bin/env python3
# upscayl-bin 흉내: 같은 인수를 받고, 같은 형식으로 진행률을 찍고, 실제로 이미지를 키워 저장한다.
import sys, os, time
from PIL import Image, ImageFilter
args = sys.argv[1:]
opt = {}
i = 0
flags = set()
while i < len(args):
    a = args[i]
    if a in ('-x', '-v', '-h'):
        flags.add(a); i += 1; continue
    opt[a] = args[i + 1]; i += 2
with open(os.environ.get('FAKE_LOG', os.path.join(os.path.dirname(os.path.abspath(__file__)), 'fake-engine.log')), 'a') as lf:
    lf.write(f"cwd={os.getcwd()} args={args}\n")
def err(msg):
    sys.stderr.write(msg + "\n"); sys.stderr.flush()
inp, out = opt.get('-i'), opt.get('-o')
mdir, name = opt.get('-m', 'models'), opt.get('-n', 'realesrgan-x4plus')
z = int(opt.get('-z', '4')); s = int(opt.get('-s', z)); fmt = opt.get('-f', 'png'); comp = int(opt.get('-c', '0'))
if 'models' not in mdir:
    err("🚨 Error: Unknown model dir type."); sys.exit(255)
if not (os.path.exists(os.path.join(mdir, name + '.param')) and os.path.exists(os.path.join(mdir, name + '.bin'))):
    err(f"fopen {mdir}/{name}.param failed"); sys.exit(0)
err("[0 Fake GeForce RTX 4060]  queueC=2[8]  queueG=0[16]  queueT=1[2]")
err("[0 Fake GeForce RTX 4060]  bugsbn1=0  bugbilz=0  bugcopc=0  bugihfa=0")
err("[1 Fake Intel UHD 770]  queueC=0[1]  queueG=0[1]  queueT=0[1]")
g = opt.get('-g')
if g is not None and g not in ('0', '1'):
    err("🚨 Error: Invalid GPU Device"); sys.exit(255)
base = os.path.basename(inp).lower()
if 'fail' in base:
    err(f"🚨 Error: Couldn't read the image '{inp}'! (channels: 0)"); sys.exit(0)
if 'crash' in base:
    sys.exit(3)
try:
    im = Image.open(inp); im.load()
except Exception as e:
    err(f"🚨 Error: Couldn't read the image '{inp}'! (channels: 0)"); sys.exit(0)
tiles = 12
delay = 0.25 if 'slow' in base else 0.06
if '-x' in flags: delay *= 2
for t in range(tiles):
    err(f"{t / tiles * 100:.2f}%"); time.sleep(delay)
w, h = im.size
mode = 'RGBA' if im.mode in ('RGBA', 'LA', 'P') else 'RGB'
big = im.convert(mode).resize((w * z, h * z), Image.LANCZOS).filter(ImageFilter.UnsharpMask(2, 120, 2))
if s != z:
    big = big.resize((w * s, h * s), Image.LANCZOS)
ext = os.path.splitext(out)[1].lower().lstrip('.')
if ext in ('jpg', 'jpeg'):
    big.convert('RGB').save(out, 'JPEG', quality=100 - comp)
elif ext == 'webp':
    if comp == 0: big.save(out, 'WEBP', lossless=True)
    else: big.save(out, 'WEBP', quality=100 - comp)
else:
    big.save(out, 'PNG')
err("100.00%")
err("\n🙌 Upscayled Successfully!")
