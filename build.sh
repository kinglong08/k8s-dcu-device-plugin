#!/bin/bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Hygon Information Technology Co., Ltd.

: <<'EOF'
SPDX-License-Identifier: Apache-2.0
Copyright 2026 Hygon Information Technology Co., Ltd.
EOF

# 编译
export GOPROXY=https://goproxy.cn
export CGO_ENABLED=1
go mod tidy
go build  -ldflags "-X 'main.version=v2.4.2'" -o k8s-device-plugin cmd/main.go

# 制作docker镜像
docker build --target dp -t harbor.sourcefind.cn:5443/dcu/admin/base/dcu-device-plugin:v2.4.2 .
docker save -o dcu-device-plugin-v2.4.2.tar harbor.sourcefind.cn:5443/dcu/admin/base/dcu-device-plugin:v2.4.2
