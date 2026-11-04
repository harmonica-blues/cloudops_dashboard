#!/bin/bash
# EC2（Amazon Linux 2023）の初期設定。インスタンスの起動時に「ユーザーデータ」として 1 回だけ実行される。
#   - スワップ 2GB（t3.small などメモリ 2GB のインスタンスで、イメージのビルド中にメモリが不足しないようにする）
#   - Docker、Docker Compose プラグイン、buildx プラグイン
#   - SSM エージェント（Session Manager で接続するため。AL2023 には最初から入っている）
#   - DEPLOY_S3_URI を指定した場合は、S3 の ec2-dash.tgz を展開してコンテナを起動する
# 実行結果は /var/log/user-data.log に出力される。x86_64（t3 など）と arm64（t4g など）の両方に対応。
set -euxo pipefail
exec > >(tee /var/log/user-data.log) 2>&1

# 例: s3://your-bucket/deploy/ec2-dash.tgz（空の場合はインストールだけ行い、展開と起動はしない）。
# 使う場合は、インスタンスの IAM ロールにこのオブジェクトの s3:GetObject 権限が必要。
DEPLOY_S3_URI=""

# スワップ 2GB
if [ ! -f /swapfile ]; then
  dd if=/dev/zero of=/swapfile bs=1M count=2048
  chmod 600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

# Docker（インスタンスの起動時に自動で起動する）。ec2-user は sudo なしで docker を使えるようにする
dnf install -y docker tar gzip
systemctl enable --now docker
usermod -aG docker ec2-user

# Docker Compose と buildx のプラグイン（AL2023 の docker パッケージには含まれていないため、GitHub の最新版を入れる）
case "$(uname -m)" in
  x86_64) COMPOSE_ARCH=x86_64; BUILDX_ARCH=amd64 ;;
  aarch64) COMPOSE_ARCH=aarch64; BUILDX_ARCH=arm64 ;;
  *) echo "未対応のアーキテクチャです: $(uname -m)"; exit 1 ;;
esac
PLUGINS=/usr/local/lib/docker/cli-plugins
mkdir -p "$PLUGINS"
curl -fsSL "https://github.com/docker/compose/releases/latest/download/docker-compose-linux-${COMPOSE_ARCH}" -o "$PLUGINS/docker-compose"
BUILDX_VERSION=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/docker/buildx/releases/latest | sed 's|.*/tag/||')
curl -fsSL "https://github.com/docker/buildx/releases/download/${BUILDX_VERSION}/buildx-${BUILDX_VERSION}.linux-${BUILDX_ARCH}" -o "$PLUGINS/docker-buildx"
chmod +x "$PLUGINS"/*
docker compose version
docker buildx version

# Session Manager 用（AL2023 では既定で有効。念のため有効化する）
systemctl enable --now amazon-ssm-agent || true

# ダッシュボードの展開と起動（任意）
if [ -n "$DEPLOY_S3_URI" ]; then
  cd /home/ec2-user
  aws s3 cp "$DEPLOY_S3_URI" ec2-dash.tgz
  tar -xzf ec2-dash.tgz
  # コンテナは UID 65532 で動作し、マウントした config.json と queries/ を読み取るため、全員に読み取りを許可する
  chmod -R a+rX ec2-dash
  chown -R ec2-user:ec2-user ec2-dash ec2-dash.tgz
  cd ec2-dash
  docker compose up -d --build
fi

echo "user-data の処理が完了しました"
