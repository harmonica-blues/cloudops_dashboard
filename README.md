# CloudOps

**AWS WAF・EC2・RDS の状態とメトリクスを、ひとつのダッシュボードで。**

**日本語** | [English](README.en.md)

CloudOps は、Go と HTML / CSS / JavaScript で作ったセルフホスト型の AWS 監視ダッシュボードです。インフラの状態、リクエストの傾向、前期間からの変化を確認し、必要に応じて Athena で WAF ログを調査できます。

画面は日本語で、ライト・ダークの両テーマに対応しています。Docker Compose で起動し、EC2 と ALB を使った HTTPS 環境にも配置できます。

## 主な機能

| 画面 | 確認できること |
| --- | --- |
| 概要 | WAF の許可・ブロック数、EC2 の CPU・メモリ、RDS の接続数などを一覧表示 |
| WAF | アクション別の推移、ルール別ブロック数、直近 3 時間のサンプルリクエスト |
| EC2 | インスタンスの状態、CPU・メモリの平均値と最大値、時系列グラフ |
| RDS | CPU、接続数、書き込みレイテンシ、空きストレージ |
| クエリ | WAF ログへの Athena クエリ、保存済み SQL、実行履歴、結果表示、CSV ダウンロード |

- 過去 1 日・7 日・30 日と、最大 90 日のカスタム期間を選択できます。
- ページと適用済み期間を URL に保存し、再読み込み、リンク共有、ブラウザの戻る・進むで復元します。カスタム期間の開始・終了日時も引き継ぎます。
- 前日・前週・前月など、直前の同じ長さの期間と比較できます。
- グラフは左から右へ、円形の指標は値まで徐々に描画します（自動更新時と動きを減らす設定時を除く）。
- ログイン、ユーザーごとのセッション、操作ログ、クエリ結果の共有制御を備えています。
- SQLite にユーザー、セッション、操作ログ、クエリ履歴と利用額を保存します。

WAF のサンプルリクエストは全件ログではありません。全件を対象に調査する場合は Athena を使います。EC2 のメモリ表示には CloudWatch Agent などによるメトリクス送信が必要です。

各画面の説明とスクリーンショットは[機能紹介](docs/features.ja.md)をご覧ください。

## コストを意識した設計

収集したメトリクスはキャッシュし、画面を見ていない間は定期収集を一時停止します。画面の自動更新はキャッシュを読み直します。Athena は利用者が実行ボタンを押した場合だけ使用します。

Athena には 1 日の推定使用額の上限、必須のパーティション範囲フィルター、同時実行 1 件の制限があります。上限による停止は永続化され、自動では解除されません。使用額は推定値で、実行中のクエリ 1 件分だけ上限を超える場合があります。

## AWS なしで試す

Docker と Docker Compose プラグインが必要です。リポジトリのルートで実行してください。以下は Linux / macOS の例です。

```bash
docker compose --env-file /dev/null -f docker-compose.screenshot.yml up -d --build
docker compose --env-file /dev/null -f docker-compose.screenshot.yml exec cloudops /app/cloudops user add demo
```

[http://127.0.0.1:8083](http://127.0.0.1:8083) を開き、`demo` とコマンドに表示されたパスワードでログインします。Windows では `/dev/null` の代わりに空の環境変数ファイルを指定してください。

この `screenshot` モードでは、グラフ・イベント・クエリ結果に**架空データ**を表示します。AWS の認証情報は不要で、AWS への接続、クエリ実行、CSV ダウンロードは行いません。撮影用のデータベースは通常の環境から分離されています。

停止するには:

```bash
docker compose --env-file /dev/null -f docker-compose.screenshot.yml down
```

## 自分の AWS 環境で使う

1. `config.example.json` を `config.json` に、`.env.local.example` を `.env.local` にコピーします。既存の設定は上書きしないでください。
2. リージョン、監視対象の EC2 / RDS、WAF と AWS プロファイルを設定します。Athena を使う場合は、ログ用テーブル、ワークグループ、S3 の結果出力先も用意します。
3. ホスト側で AWS 認証を済ませ、以下を実行します。

```bash
docker compose --env-file .env.local up -d --build
docker compose --env-file .env.local exec cloudops /app/cloudops user add your-user
```

ローカル設定例の接続先は [http://127.0.0.1:8082](http://127.0.0.1:8082) です。Windows 用の設定、IAM 権限、EC2 + ALB への配置、ユーザー管理、バックアップは[設定・運用ガイド](docs/operations.ja.md)を参照してください。

実データを扱う `aws` モードでは、取得できないメトリクスを架空値で補いません。空の画面を確認するための `demo` モードもあります。

## 技術構成

| 項目 | 使用技術 |
| --- | --- |
| バックエンド | Go 1.24、標準 HTTP サーバー、AWS SDK for Go v2 |
| フロントエンド | HTML、CSS、JavaScript。フレームワーク・ビルド工程なし |
| データ保存 | SQLite（Go 実装、CGO 不要） |
| 実行環境 | Docker Compose、distroless nonroot イメージ |
| AWS 連携 | CloudWatch、WAF、EC2、RDS、Athena、S3、STS |

```text
app/         Go のコード・テスト・埋め込みフロントエンド
queries/     WAF ログ調査用の SQL テンプレート
deploy/      IAM ポリシー例・EC2 初期設定
docs/        設定・運用ガイド
```

## 運用上のポイント

- AWS モードでは CloudWatch や Athena などの利用料金が発生します。
- 外部へ公開する場合は HTTPS とアクセス制限を設定してください。EC2 では IAM ロールを使用します。
- ユーザーには同じ機能が提供されます。ユーザー管理はコンテナ内の CLI で行います。
- `.env`、`config.json`、認証情報、データベースは公開しないでください。
- 日時の集計と Athena パーティションは UTC、表示は設定したタイムゾーンです。カスタム期間の入力は JST です。

## 開発・ドキュメント

Linux / macOS でのテスト:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.24-alpine go test ./...
```

- [機能紹介・スクリーンショット](docs/features.ja.md)
- [設定・運用ガイド](docs/operations.ja.md)
- [保存済みクエリの書き方](queries/README.md)
- [English README](README.en.md)
