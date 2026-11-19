#!/usr/bin/env bash
# Regenera o ícone do aplicativo a partir de web/static/icon.svg (a fonte única):
#   - cmd/telaremota/telaremota.ico          (16 a 256 px, para o Explorer e atalhos)
#   - cmd/telaremota/rsrc_windows_amd64.syso (ícone e detalhes embutidos no .exe;
#     o "go build" inclui o .syso sozinho, sem nenhuma ferramenta extra)
#   - internal/winui/icon.png                (ícone das janelas)
#
# Precisa do ImageMagick (magick), do Python e do windres (MinGW/binutils). Uso:
#   bash scripts/icone.sh
# Os arquivos gerados vão para o repositório, então só é preciso rodar ao mudar o ícone.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
svg=web/static/icon.svg
tmp=$(mktemp -d .icone.XXXXXX) # relativo: o magick do Windows não entende /tmp
trap 'rm -rf "$tmp"' EXIT

pngs=()
for s in 16 20 24 32 40 48 64 256; do
	# Rasteriza com folga (4x) e reduz: bordas mais nítidas nos tamanhos pequenos.
	magick -background none -density $((s * 96 / 64 * 4)) "$svg" -resize "${s}x${s}" "PNG32:$tmp/$s.png"
	pngs+=("$tmp/$s.png")
done
# .ico com cada tamanho em PNG (o magick grava sem compressão: ~300 KB a mais no .exe).
python - cmd/telaremota/telaremota.ico "${pngs[@]}" <<'PY'
import struct, sys
out, files = sys.argv[1], sys.argv[2:]
data = [open(f, "rb").read() for f in files]
sizes = [struct.unpack(">II", d[16:24])[0] for d in data]
ico = struct.pack("<HHH", 0, 1, len(data))
off = 6 + 16 * len(data)
for s, d in zip(sizes, data):
    ico += struct.pack("<BBBBHHII", s % 256, s % 256, 0, 0, 1, 32, len(d), off)
    off += len(d)
open(out, "wb").write(ico + b"".join(data))
PY
cp "$tmp/256.png" internal/winui/icon.png

windres -F pe-x86-64 -O coff -I cmd/telaremota \
	-i cmd/telaremota/telaremota.rc -o cmd/telaremota/rsrc_windows_amd64.syso

echo "ícone regenerado:"
ls -l cmd/telaremota/telaremota.ico cmd/telaremota/rsrc_windows_amd64.syso internal/winui/icon.png
