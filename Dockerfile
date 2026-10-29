# rajikura のコンテナイメージ（x64）。常駐はコンテナを主にする（ADR 0005）。
# 置き場所: データのフォルダ（DB・秘密の鍵のファイル・作業用フォルダ、Web UI のパスワードが決まるまでの間だけセットアップコードのファイル setup-code.txt）は /data、ライブラリは /library。
# 足したら直す: データのフォルダ
# 設定ファイルは無く、設定は DB に置く（ADR 0015）。起動の設定は serve のフラグ -data・-listen・-allowed-hosts だけ。
#
# このファイル 1 つで、イメージと Release の配布物（Linux・Windows のバイナリ。Windows のバイナリにはトレイを埋め込む）の両方をビルドする。
# docker-bake.hcl の 1 回の呼び出しで web と build の段を 1 回だけ作り、イメージの中のバイナリと配布物の Linux のバイナリが同じビルドから来るようにする
# （Web UI のビルドと Go のツールチェーンの版を、この Dockerfile の 1 か所で決めるため）。release.yml が sha256 の一致も確かめる。
#
# 含むソフトウェアのライセンスの一覧（rajikura licenses が出す。cmd/rajikura/licenses.go）は、ビルドのたびにここで作って埋め込み、repo には置かない。
# - Web UI の分: web の段の pnpm build が、Vite の build.license で web/dist/.vite/license.md に出す（web/vite.config.ts）。web/dist ごと埋め込まれる
# - Go の分: build と build-windows の段が、バイナリをビルドする前に scripts/gen-licenses.sh で cmd/rajikura/licenses/licenses.txt に作る。
#   数える範囲は OS ごとのビルドの対象と揃える（Linux は ./cmd/rajikura、Windows はトレイを埋め込むので ./cmd/rajikura と ./cmd/rajikura-tray）。
#   build-windows の段は build の段の Linux の一覧を引き継ぐので、rajikura.exe をビルドする前に Windows の範囲で作り直す
# イメージの中のバイナリは配布物の Linux のバイナリと同じなので、イメージにも同じ一覧が入る（別のファイルは置かない）。

# Web UI（web/）を Node でビルドする。pnpm の版は mise.toml から読む（Node の版は、このイメージの tag を mise.toml に揃える。ずれると CI の web の job が落ちる）。
FROM node:24.22.1-alpine@sha256:06d7adf3e0ef61a13f1d28682726366f97a15aa1888c934d3ecec86c33918b53 AS web
WORKDIR /app
COPY mise.toml ./
RUN npm install -g "pnpm@$(sed -n 's/^pnpm = "\(.*\)"/\1/p' mise.toml)"
WORKDIR /app/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.27-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# go:embed が埋め込む web/dist に、Node の段のビルドの結果を置く
COPY --from=web /app/web/dist ./web/dist
# Linux のバイナリ（とイメージ）に埋め込む、Go の分のライセンスの一覧を作る（ビルドと同じ引数の環境で、./cmd/rajikura の範囲を数える）
RUN CGO_ENABLED=0 sh scripts/gen-licenses.sh cmd/rajikura/licenses/licenses.txt ./cmd/rajikura
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/rajikura ./cmd/rajikura

# Windows（x64）のバイナリ。rajikura.exe は、Linux と同じ web/dist・同じ引数で、GOOS だけを変えてビルドする（macOS など OS を足すときは、この段と同じ形の段を足して artifacts に集める）。
# その前に、rajikura.exe に埋め込むトレイ（cmd/rajikura-tray）を、別の引数（-H windowsgui を足し、-X main.version は付けない）で先にビルドして
# cmd/rajikura/traybin に置く（cmd/rajikura/tray_windows.go の go:embed）。Linux のバイナリとイメージには入れない。
# ライセンスの一覧は、build の段の Linux の範囲のものを、トレイの依存も入れた Windows の範囲（./cmd/rajikura と ./cmd/rajikura-tray）で作り直す。
FROM build AS build-windows
ARG VERSION=dev
RUN GOOS=windows GOARCH=amd64 CGO_ENABLED=0 sh scripts/gen-licenses.sh cmd/rajikura/licenses/licenses.txt ./cmd/rajikura ./cmd/rajikura-tray
RUN GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o cmd/rajikura/traybin/rajikura-tray.exe ./cmd/rajikura-tray \
    && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/rajikura.exe ./cmd/rajikura

# Release の配布物（docker-bake.hcl の artifacts）。全部の OS のバイナリを、配布物の名前で集める。
# 実行用の段より前に置く（--target なしの docker build は最後の段だけを作るので、この段と Windows の段は作られない）。
FROM scratch AS artifacts
ARG VERSION=dev
COPY --from=build /out/rajikura /rajikura_${VERSION}_linux_amd64
COPY --from=build-windows /out/rajikura.exe /rajikura_${VERSION}_windows_amd64.exe

# 実行用のイメージ（最後の段）
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
# OCI のラベル。版のラベルは付けない（イメージのタグが版を表す）。licenses は rajikura 自身のライセンス（依存のものは rajikura licenses で出す）
LABEL org.opencontainers.image.source="https://github.com/t-seki/rajikura" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.description="radiko の放送を録音し、Jellyfin などのライブラリへ自動で保存するサービス。"
# ffmpeg は入れない（m4a への格納の既定は builtin。ADR 0013）
RUN addgroup -g 1000 rajikura \
    && adduser -D -u 1000 -G rajikura rajikura \
    && mkdir -p /data /library \
    && chown rajikura:rajikura /data /library
COPY --from=build /out/rajikura /usr/local/bin/rajikura
USER 1000:1000
VOLUME ["/data", "/library"]
ENTRYPOINT ["/usr/local/bin/rajikura"]
CMD ["-data", "/data", "serve"]
