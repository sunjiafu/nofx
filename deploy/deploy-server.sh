#!/bin/bash

# ═══════════════════════════════════════════════════════════════
# NOFX 服务器快速部署脚本
# 在服务器上运行此脚本，自动完成部署配置
# ═══════════════════════════════════════════════════════════════

set -e

# 颜色
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

# 配置
DEPLOY_DIR="/opt/nofx"
SERVICE_NAME="nofx"

echo "╔════════════════════════════════════════════════════════════╗"
echo "║    🚀 NOFX 服务器快速部署工具                             ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo ""

# 检查是否为root
if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}✗ 请使用sudo运行此脚本${NC}"
    echo "  sudo $0"
    exit 1
fi

echo -e "${BLUE}[INFO]${NC} 部署目录: $DEPLOY_DIR"
echo ""

# 询问部署方式
echo "请选择部署方式："
echo "  1) Docker部署（推荐 - 简单、隔离）"
echo "  2) 直接部署（高性能 - 需手动安装依赖）"
echo ""
read -p "请选择 [1-2]: " deploy_choice

if [ "$deploy_choice" == "1" ]; then
    echo ""
    echo "═══════════════════════════════════════════════════════════"
    echo "  🐳 Docker部署模式"
    echo "═══════════════════════════════════════════════════════════"
    echo ""

    # 检查Docker
    if ! command -v docker &> /dev/null; then
        echo -e "${YELLOW}[1/6]${NC} 安装Docker..."
        curl -fsSL https://get.docker.com | sh
        systemctl enable docker
        systemctl start docker
    else
        echo -e "${GREEN}[1/6]${NC} Docker已安装 ✓"
    fi

    # 检查Docker Compose
    if ! command -v docker-compose &> /dev/null; then
        echo -e "${YELLOW}[2/6]${NC} 安装Docker Compose..."
        curl -L "https://github.com/docker/compose/releases/latest/download/docker-compose-$(uname -s)-$(uname -m)" -o /usr/local/bin/docker-compose
        chmod +x /usr/local/bin/docker-compose
    else
        echo -e "${GREEN}[2/6]${NC} Docker Compose已安装 ✓"
    fi

    # 创建部署目录
    echo -e "${GREEN}[3/6]${NC} 创建部署目录..."
    mkdir -p "$DEPLOY_DIR"
    cd "$DEPLOY_DIR"

    # 配置环境变量
    echo -e "${GREEN}[4/6]${NC} 配置环境变量..."
    if [ ! -f .env ]; then
        echo ""
        echo "  请选择Go模块代理来源（Docker构建会使用该地址下载依赖）"
        echo "    1) 国际/香港服务器（默认）: https://proxy.golang.org,direct"
        echo "    2) 中国大陆服务器: https://goproxy.cn,direct"
        read -p "  请选择 [1-2，回车默认1]: " go_proxy_choice
        if [ "$go_proxy_choice" == "2" ]; then
            GO_PROXY_URL="https://goproxy.cn,direct"
        else
            GO_PROXY_URL="https://proxy.golang.org,direct"
        fi

        cat > .env << EOF
NOFX_BACKEND_PORT=8080
NOFX_FRONTEND_PORT=3000
NOFX_TIMEZONE=Asia/Shanghai
GOPROXY_URL=$GO_PROXY_URL
EOF
        echo -e "  ${GREEN}✓${NC} 已创建 .env 文件（GOPROXY_URL=$GO_PROXY_URL）"
    else
        echo -e "  ${YELLOW}⚠${NC}  .env 已存在，如需修改Go代理请编辑 GOPROXY_URL"
    fi

    # 配置config.json
    echo -e "${GREEN}[5/6]${NC} 配置交易参数..."
    if [ ! -f config.json ]; then
        if [ -f config.json.example ]; then
            cp config.json.example config.json
            echo -e "  ${YELLOW}⚠${NC}  请编辑 $DEPLOY_DIR/config.json 填入你的API密钥"
            echo -e "  ${BLUE}nano $DEPLOY_DIR/config.json${NC}"
        else
            echo -e "  ${RED}✗${NC} 缺少 config.json.example 文件"
            exit 1
        fi
    else
        echo -e "  ${GREEN}✓${NC} config.json 已存在"
    fi

    # 启动服务
    echo -e "${GREEN}[6/6]${NC} 启动服务..."
    echo ""
    read -p "是否现在启动服务？[y/N]: " start_now
    if [ "$start_now" == "y" ] || [ "$start_now" == "Y" ]; then
        docker-compose up -d
        echo ""
        echo -e "${GREEN}✓ 服务启动成功！${NC}"
        echo ""
        echo "查看状态: docker-compose ps"
        echo "查看日志: docker-compose logs -f nofx"
        echo "Web界面: http://$(hostname -I | awk '{print $1}'):3000"
    else
        echo ""
        echo -e "${YELLOW}跳过启动，稍后手动启动：${NC}"
        echo "  cd $DEPLOY_DIR"
        echo "  docker-compose up -d"
    fi

elif [ "$deploy_choice" == "2" ]; then
    echo ""
    echo "═══════════════════════════════════════════════════════════"
    echo "  ⚡ 直接部署模式"
    echo "═══════════════════════════════════════════════════════════"
    echo ""

    # 检测系统
    if [ -f /etc/os-release ]; then
        . /etc/os-release
        OS=$ID
    else
        echo -e "${RED}✗ 无法检测系统类型${NC}"
        exit 1
    fi

    # 安装依赖
    echo -e "${GREEN}[1/6]${NC} 安装系统依赖..."
    if [ "$OS" == "ubuntu" ] || [ "$OS" == "debian" ]; then
        apt update
        apt install -y wget tar build-essential curl
    elif [ "$OS" == "centos" ] || [ "$OS" == "rhel" ]; then
        yum install -y wget tar gcc gcc-c++ make curl
    else
        echo -e "${YELLOW}⚠  未知系统，请手动安装: wget, tar, gcc, make${NC}"
    fi

    # 安装TA-Lib
    echo -e "${GREEN}[2/6]${NC} 安装TA-Lib..."
    if [ ! -f /usr/local/lib/libta_lib.so ]; then
        cd /tmp
        wget -q http://prdownloads.sourceforge.net/ta-lib/ta-lib-0.4.0-src.tar.gz
        tar -xzf ta-lib-0.4.0-src.tar.gz
        cd ta-lib/
        ./configure --prefix=/usr/local
        make
        make install
        ldconfig
        cd /tmp
        rm -rf ta-lib ta-lib-0.4.0-src.tar.gz
        echo -e "  ${GREEN}✓${NC} TA-Lib安装完成"
    else
        echo -e "  ${GREEN}✓${NC} TA-Lib已安装"
    fi

    # 检查Go
    echo -e "${GREEN}[3/6]${NC} 检查Go环境..."
    if ! command -v go &> /dev/null; then
        echo -e "  ${YELLOW}⚠${NC}  Go未安装，正在安装..."
        cd /tmp
        wget -q https://go.dev/dl/go1.21.5.linux-amd64.tar.gz
        tar -C /usr/local -xzf go1.21.5.linux-amd64.tar.gz
        echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile
        source /etc/profile
        rm go1.21.5.linux-amd64.tar.gz
    fi
    GO_VERSION=$(go version 2>/dev/null || echo "Go未安装")
    echo -e "  ${GREEN}✓${NC} $GO_VERSION"

    # 创建部署目录
    echo -e "${GREEN}[4/6]${NC} 准备部署目录..."
    mkdir -p "$DEPLOY_DIR"
    cd "$DEPLOY_DIR"
    mkdir -p logs decision_logs prediction_logs trader_memory

    # 编译
    if [ -f "main.go" ]; then
        echo -e "${GREEN}[5/6]${NC} 编译应用..."
        CGO_ENABLED=1 go build -o nofx main.go
        chmod +x nofx
        echo -e "  ${GREEN}✓${NC} 编译完成"
    else
        echo -e "${YELLOW}[5/6]${NC} 跳过编译（缺少源代码）"
        echo -e "  请先上传代码到 $DEPLOY_DIR"
    fi

    # 配置systemd服务
    echo -e "${GREEN}[6/6]${NC} 配置systemd服务..."
    cat > /etc/systemd/system/$SERVICE_NAME.service << EOF
[Unit]
Description=NOFX AI Trading System
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$DEPLOY_DIR
ExecStart=$DEPLOY_DIR/nofx
Restart=always
RestartSec=10s
StandardOutput=append:$DEPLOY_DIR/logs/nofx.log
StandardError=append:$DEPLOY_DIR/logs/nofx.log

Environment="PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/go/bin"
Environment="LD_LIBRARY_PATH=/usr/local/lib"

LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable $SERVICE_NAME
    echo -e "  ${GREEN}✓${NC} systemd服务已配置"

    # 配置config.json
    if [ ! -f config.json ]; then
        if [ -f config.json.example ]; then
            cp config.json.example config.json
            echo -e "  ${YELLOW}⚠${NC}  请编辑 $DEPLOY_DIR/config.json 填入你的API密钥"
        fi
    fi

    # 启动服务
    echo ""
    read -p "是否现在启动服务？[y/N]: " start_now
    if [ "$start_now" == "y" ] || [ "$start_now" == "Y" ]; then
        systemctl start $SERVICE_NAME
        sleep 2
        systemctl status $SERVICE_NAME --no-pager
        echo ""
        echo -e "${GREEN}✓ 服务启动成功！${NC}"
        echo ""
        echo "管理命令:"
        echo "  启动: systemctl start $SERVICE_NAME"
        echo "  停止: systemctl stop $SERVICE_NAME"
        echo "  重启: systemctl restart $SERVICE_NAME"
        echo "  状态: systemctl status $SERVICE_NAME"
        echo "  日志: journalctl -u $SERVICE_NAME -f"
    else
        echo ""
        echo -e "${YELLOW}跳过启动，稍后手动启动：${NC}"
        echo "  systemctl start $SERVICE_NAME"
    fi

else
    echo -e "${RED}✗ 无效的选择${NC}"
    exit 1
fi

echo ""
echo "═══════════════════════════════════════════════════════════"
echo -e "  ${GREEN}✓ 部署完成！${NC}"
echo "═══════════════════════════════════════════════════════════"
echo ""
echo "📖 详细文档: $DEPLOY_DIR/SERVER_DEPLOY.md"
echo "⚙️  配置文件: $DEPLOY_DIR/config.json"
echo "📊 日志目录: $DEPLOY_DIR/logs"
echo ""
