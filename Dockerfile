ARG NODE_IMAGE=node:20-alpine
ARG GO_IMAGE=golang:1.25-alpine

FROM ${NODE_IMAGE} AS frontend-builder

WORKDIR /src/assets

ENV NODE_OPTIONS=--max-old-space-size=8192

RUN apk add --no-cache git

COPY assets/package.json assets/package-lock.json ./
RUN HUSKY=0 npm install --legacy-peer-deps

COPY assets/ ./

ARG VERSION=0.0.0-dev
RUN npm pkg set version="${VERSION}"
RUN npm run build


FROM ${GO_IMAGE} AS backend-builder

WORKDIR /src

RUN apk add --no-cache zip git

COPY go.mod go.sum ./
RUN GODEBUG=http2client=0 GOPROXY=https://goproxy.cn,direct go mod download

COPY . ./
RUN rm -rf assets/build
COPY --from=frontend-builder /src/assets/build ./assets/build

RUN zip -r - assets/build > application/statics/assets.zip

ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X github.com/cloudreve/Cloudreve/v4/application/constants.BackendVersion=${VERSION} -X github.com/cloudreve/Cloudreve/v4/application/constants.LastCommit=${COMMIT}" \
    -o cloudreve \
    .


FROM alpine:latest

WORKDIR /cloudreve

RUN apk update \
    && apk add --no-cache tzdata vips-tools ffmpeg libreoffice aria2 supervisor font-noto font-noto-cjk libheif libraw-tools\
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone \
    && mkdir -p ./data/temp/aria2 \
    && chmod -R 766 ./data/temp/aria2

ARG VERSION=0.0.0-dev
ARG COMMIT=unknown

LABEL org.opencontainers.image.source="https://github.com/lyyQwQ/cloudreve" \
    org.opencontainers.image.revision="${COMMIT}" \
    org.opencontainers.image.version="${VERSION}"

ENV CR_ENABLE_ARIA2=1 \
    CR_SETTING_DEFAULT_thumb_ffmpeg_enabled=1 \
    CR_SETTING_DEFAULT_thumb_vips_enabled=1 \
    CR_SETTING_DEFAULT_thumb_libreoffice_enabled=1 \
    CR_SETTING_DEFAULT_media_meta_ffprobe=1  \
    CR_SETTING_DEFAULT_thumb_libraw_enabled=1

COPY .build/aria2.supervisor.conf .build/entrypoint.sh ./
COPY --from=backend-builder /src/cloudreve ./cloudreve

RUN chmod +x ./cloudreve \
    && chmod +x ./entrypoint.sh

EXPOSE 5212 443

VOLUME ["/cloudreve/data"]

ENTRYPOINT ["sh", "./entrypoint.sh"]
