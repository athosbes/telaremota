#!/usr/bin/env bash
# Monta os pacotes de uma versão do TelaRemota em dist/release/.
#
#   scripts/release.sh v0.5.0        (ou 0.5.0)
#
# Gera, para cada alvo, um pacote com o executável, README, LICENSE e
# THIRD_PARTY_LICENSES.txt (licenças das bibliotecas embutidas), além de
# SHA256SUMS.txt. Roda no Linux (CI) e no Git Bash do Windows.
set -euo pipefail

tag="${1:?use: scripts/release.sh vX.Y.Z}"
version="${tag#v}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
	echo "versão inválida: $tag (esperado vX.Y.Z)" >&2
	exit 1
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
out="dist/release"
work="dist/release-work"
rm -rf "$out" "$work"
mkdir -p "$out" "$work"
ldflags="-s -w -X main.version=$version"

# third_party <goos> <goarch> <pacote> <arquivo>: licenças de todos os módulos
# que entram no executável daquele alvo.
third_party() {
	local goos=$1 goarch=$2 pkg=$3 dest=$4 missing=0
	local path ver dir f files # locais: não sobrescrever as variáveis de quem chama
	{
		echo "TelaRemota $version — licenças de terceiros ($goos/$goarch)"
		echo "O TelaRemota é distribuído sob a GNU AGPL-3.0 (arquivo LICENSE)."
		echo "Este executável inclui as bibliotecas abaixo, cada uma sob a sua licença."
	} >"$dest"
	while read -r path ver dir; do
		[ -n "$path" ] || continue
		files=$(find "$dir" -maxdepth 1 -type f \( -iname 'LICENSE*' -o -iname 'LICENCE*' -o -iname 'COPYING*' -o -iname 'NOTICE*' -o -iname 'PATENTS*' \) | sort)
		if [ -z "$files" ]; then
			echo "aviso: sem arquivo de licença em $path@$ver" >&2
			missing=$((missing + 1))
			continue
		fi
		for f in $files; do
			printf '\n\n================================================================================\n%s %s — %s\n================================================================================\n\n' "$path" "$ver" "$(basename "$f")" >>"$dest"
			tr -d '\r' <"$f" >>"$dest"
		done
	done < <(GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' "$pkg" | sort -u)
	if [ "$missing" -gt 0 ]; then
		echo "erro: $missing módulo(s) sem licença encontrada" >&2
		return 1
	fi
}

# pack <dir> <arquivo>: .zip para Windows, .tar.gz para Linux.
pack() {
	local dir=$1 file=$2
	[ -n "$dir" ] && [ -d "$dir" ] || { echo "pack: diretório inválido: '$dir'" >&2; return 1; }
	case "$file" in
	*.zip)
		if command -v zip >/dev/null; then
			(cd "$(dirname "$dir")" && zip -qr9 "$root/$file" "$(basename "$dir")")
		else # Git Bash no Windows não traz zip: usa o Python
			local zf="$root/$file" base="$root/$(dirname "$dir")"
			if command -v cygpath >/dev/null; then zf=$(cygpath -w "$zf"); base=$(cygpath -w "$base"); fi
			python -c 'import sys,shutil; shutil.make_archive(sys.argv[1][:-4],"zip",sys.argv[2],sys.argv[3])' "$zf" "$base" "$(basename "$dir")"
		fi
		;;
	*.tar.gz) tar -C "$(dirname "$dir")" -czf "$file" "$(basename "$dir")" ;;
	esac
}

# build <goos> <goarch> <pacote> <executável> <nome do pacote> <extensão>
build() {
	local goos=$1 goarch=$2 pkg=$3 bin=$4 name=$5 ext=$6
	local dir="$work/$name"
	mkdir -p "$dir"
	echo "→ $name"
	GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$dir/$bin" "$pkg"
	cp README.md LICENSE "$dir/"
	third_party "$goos" "$goarch" "$pkg" "$dir/THIRD_PARTY_LICENSES.txt"
	pack "$dir" "$out/$name.$ext"
}

build windows amd64 ./cmd/telaremota telaremota.exe "telaremota-$version-windows-amd64" zip
build linux amd64 ./cmd/telaremota-hub telaremota-hub "telaremota-hub-$version-linux-amd64" tar.gz
build linux arm64 ./cmd/telaremota-hub telaremota-hub "telaremota-hub-$version-linux-arm64" tar.gz

(cd "$out" && sha256sum -- * >SHA256SUMS.txt)
rm -rf "$work"
echo
echo "Pacotes em $out:"
ls -l "$out"
