# phonebook-server 编译全流程：管理页打包 → vet → 本机 exe / Linux 发布二进制 → 自动安装包。
#
# 在 phonebook-server/ 目录下执行：
#   make version    # 查看将注入的 版本号/构建时间
#   make web        # 只重编前端（web-admin npm run build → ../web，go:embed 用）
#   make exe        # 本机二进制（Windows 下即 phonebook-server.exe），注入版本号
#   make linux      # 交叉编译 Linux amd64 静态二进制 → ../deploy/{installer,phonebook-server}
#   make package    # 重打自动安装包（先 make linux）→ ../deploy/phonebook-server_auto-install_linux-amd64.tar.gz
#   make release    # 一键：vet + web + exe + linux + package
#   make clean      # 删除编译产物（不删 web/，那是前端产物源）
#
# 版本自动注入：BIN_VERSION=git 短 commit（非 git 目录回退 dev），BIN_TIME=构建时刻(UTC)。
# 可覆盖，例如：make release BIN_VERSION=v1.2.0
#
# 用 '>' 作 recipe 前缀（.RECIPEPREFIX），避免编辑器把制表符写成空格导致 make 报
# “missing separator”；需 GNU make >= 3.82。
.RECIPEPREFIX = >
SHELL := bash
GO := go

DEPLOY := ../deploy
PKG_DIR := $(DEPLOY)/installer
PKG_NAME := phonebook-server_auto-install_linux-amd64.tar.gz
PKG_STAGE := $(DEPLOY)/.pkg_tmp/phonebook-server_installer

# 版本注入目标路径（Go 包全名），见 internal/api/server.go 的 BuildVersion/BuildTime。
HASH := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BIN_VERSION ?= $(HASH)
BIN_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X phonebook-server/internal/api.BuildVersion=$(BIN_VERSION) -X phonebook-server/internal/api.BuildTime=$(BIN_TIME)

.PHONY: all release web vet exe linux package version clean

all: release

version:
> @echo "version=$(BIN_VERSION)  buildTime=$(BIN_TIME)"
> @echo "ldflags: $(LDFLAGS)"

web:
> cd web-admin && npm run build

vet:
> $(GO) vet ./...

# 本机二进制（Windows 下为 phonebook-server.exe；Linux/macOS 直接是本机平台 ELF/Mach-O）
exe:
> $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o phonebook-server$(shell go env GOEXE) .

# 交叉编译 Linux amd64 静态二进制（modernc/sqlite 纯 Go，无需 CGO），同步一份到 deploy/
linux:
> mkdir -p $(PKG_DIR)
> CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(PKG_DIR)/phonebook-server .
> chmod +x $(PKG_DIR)/phonebook-server
> cp $(PKG_DIR)/phonebook-server $(DEPLOY)/phonebook-server

# 自动安装包 = installer/ 目录内容 + 新二进制，打 tar.gz（先确保 linux 最新）
package: linux
> rm -rf $(DEPLOY)/.pkg_tmp
> mkdir -p $(PKG_STAGE)
> cp -p $(PKG_DIR)/INSTALL.md $(PKG_DIR)/install.sh \
>   $(PKG_DIR)/phonebook-backup.service $(PKG_DIR)/phonebook-backup.sh \
>   $(PKG_DIR)/phonebook-backup.timer $(PKG_DIR)/phonebook-server.service \
>   $(PKG_DIR)/phonebook-server $(PKG_STAGE)/
> tar -czf $(DEPLOY)/$(PKG_NAME) -C $(DEPLOY)/.pkg_tmp phonebook-server_installer
> rm -rf $(DEPLOY)/.pkg_tmp

release: vet web exe linux package
> @echo "OK 编译产物："
> @echo "  $(DEPLOY)/$(PKG_NAME)（Linux 自动安装包，含版本 $(BIN_VERSION) / 构建 $(BIN_TIME)）"
> @echo "  phonebook-server$(shell go env GOEXE)（本机二进制）"

clean:
> rm -f phonebook-server$(shell go env GOEXE)
> rm -f $(PKG_DIR)/phonebook-server $(DEPLOY)/phonebook-server $(DEPLOY)/$(PKG_NAME)
