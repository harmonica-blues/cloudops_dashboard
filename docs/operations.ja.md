# CloudOps — 設定・運用ガイド

[プロジェクト紹介](../README.md) · **日本語** | [English](operations.en.md)

このガイドのコマンドは、リポジトリのルートで実行してください。

WAF、EC2、RDS、Athena クエリを監視するための Docker ダッシュボードです。このフォルダだけで完結しており、Go のコードとフロントエンドはイメージ内でビルドされます。

> **このフォルダについて**: EC2（ALB の後ろ）へデプロイするための独立したプロジェクトです。
>
> - **EC2**: このフォルダをそのままコピーし、`.env` の `CLOUDOPS_TRUSTED_PROXIES` を VPC の CIDR に変更してから `docker compose up -d --build` を実行します。手順は「EC2 + ALB での運用（HTTPS）」を参照してください。`.env.local` は EC2 にコピーしないでください。
> - **PC でのテスト**: `.env.local` を指定して実行します（`127.0.0.1:8082`、PC の `~/.aws` のプロファイルを使用）。データベースは EC2 とは別のボリュームです。
>
>   ```powershell
>   docker compose --env-file .env.local up -d --build
>   docker compose --env-file .env.local exec cloudops /app/cloudops user add <ユーザー名>
>   docker compose --env-file .env.local down
>   ```

## 設定ファイルの準備

初回は `config.example.json` を `config.json` にコピーしてください。EC2 用は `.env.example` を `.env` に、PC 用は `.env.local.example` を `.env.local` にコピーし、環境に合わせて編集します。既存の設定ファイルは上書きしないでください。

PC 用の例は Linux / macOS 向けです。Windows では `COMPOSE_FILE` の区切りを `;` に変更し、`AWS_CONFIG_DIR` の行を削除してください。PC 用は `COMPOSE_PROJECT_NAME=cloudops-local` で EC2 用と別のデータボリュームを使用します。

実際の環境設定（`.env`、`.env.local`、`config.json`）、データベース、バックアップ、個人メモ、Docker イメージのアーカイブは Git と Docker ビルドコンテキストから除外します。公開する設定例には実際の認証情報や環境固有の値を記載しないでください。

## スクリーンショット用の架空データ

`screenshot` モードは、WAF・EC2・RDS のグラフ、前期間との比較、サンプルリクエスト、クエリ結果と推定使用額を架空データで表示します。画面には「撮影モード（架空）」と表示されます。AWS には接続せず、クエリ実行と CSV ダウンロードは無効です。架空の履歴はデータベースに保存しません。`aws` モードの値が取得できない場合に、このデータを代用することはありません。

通常の `.env` を読み込まず、専用ポート・専用ボリュームで起動してください（Linux / macOS の例）。

```bash
docker compose --env-file /dev/null -f docker-compose.screenshot.yml up -d --build
docker compose --env-file /dev/null -f docker-compose.screenshot.yml exec cloudops /app/cloudops user add demo
```

Windows では `/dev/null` の代わりに空の環境変数ファイルを指定してください。表示されたパスワードで <http://127.0.0.1:8083> にログインします。概要・WAF・EC2・RDS・クエリの各画面と、ライト／ダークテーマを撮影できます。カスタム期間にも対応します。撮影用データは実環境の設定を参照しません。

```bash
# 撮影用コンテナだけを停止する（ログイン情報は専用ボリュームに保持）
docker compose --env-file /dev/null -f docker-compose.screenshot.yml down
```

## ダッシュボードの機能

- 概要、WAF、EC2、RDS、クエリの各画面を表示します。
- AWS の ID、EC2 の状態、CloudWatch メトリクス、RDS、WAF を取得します。
- 期間は過去 1 日、7 日間、1 か月（30 日）から選択できます。それ以外の期間は「カスタム期間」を選択し、開始・終了日時を指定します（最大 90 日）。
- ページと適用済み期間は URL のクエリパラメータで保持します（例: `/?page=waf&period=7`）。カスタム期間は `/?page=waf&period=custom&start=2026-11-01T00:00:00Z&end=2026-11-02T00:00:00Z` のように UTC の分単位の開始・終了日時を含み、ピッカーには JST で復元します。再読み込み、リンク共有、ブラウザの戻る・進む、ログイン後も引き継ぎます。固定期間は開いた時点から遡る相対期間、カスタム期間は指定日時のままです。未適用の入力や SQL は URL に保存しません。不明なページは概要へ、不正な期間（開始・終了の不足、逆転、未来、90 日超など）は過去 1 日へ戻します。共有先でもログインが必要です。
- WAF・EC2・RDS のメトリクスを時系列グラフで表示し、平均値に加えて最大値（ピーク）も表示します。RDS の空きストレージは最新値を表示します。グラフはカードの横幅いっぱいに表示し、グラフ上にカーソルを置くと、その時刻の値をツールチップで表示します。
- 主な値（WAF のリクエスト数、EC2・RDS の平均値）には、直前の同じ長さの期間との増減を表示します（過去 1 日は前日比、7 日間は前週比、1 か月は前月比、カスタム期間は前期間比）。+50% 以上は黄色、+100% 以上は赤で表示します。前期間の値はほとんど変わらないため、固定期間では 1 時間に 1 回だけ取得します（CloudWatch の料金の増加は数 % 程度）。
- 取得できない値は `データなし` と表示し、メトリクスを推測で作成することはありません。
- データをメモリ上にキャッシュし、`collection_interval` の間隔で再収集します。誰も画面を見ていない間（`idle_pause_after`）は収集を一時停止します。定期収集の対象はプルダウンの 3 つの期間です（期間を追加すると CloudWatch の料金も増えます）。
- 画面は 60 秒ごとにキャッシュを読み直して自動更新します（カスタム期間を除く）。
- 初回表示、画面切り替え、期間変更、手動更新では、折れ線と小さな棒グラフを左から右へ、円形の指標を現在値まで約 1.1 秒で描画します。自動更新や画面幅の変更では再生を開始しません。OS・ブラウザの「動きを減らす」設定時はアニメーションを省略します。データの取得を遅らせることはありません。
- 実データには `aws` モード、空の画面には `demo` モード、撮影用の架空データには `screenshot` モードを使用します。
- WAF 画面には、ルール別ブロック数（選択期間、CloudWatch）と、直近 3 時間のサンプルリクエスト（AWS WAF の `GetSampledRequests`、無料）を表示します。Athena は使用しません（下記「WAF のサンプルリクエストとルール別ブロック数」）。
- WAF ログに対する Athena クエリを画面から手動で実行できます。よく使うクエリは `queries/` フォルダに保存し、プルダウンから選択できます。
- 利用にはログインが必要です。ユーザー、操作ログ、クエリ履歴（誰がどのクエリを実行したか）、1日の Athena 使用額は SQLite に保存します（下記「ログインとユーザー管理」）。

## 主なファイル

```
.
├── Dockerfile / docker-compose.yml   コンテナのビルドと実行（このテンプレートを実行する唯一のエントリーポイント）
├── .env                              Compose が自動で読み込む環境変数（Git と Docker イメージからは除外）
├── config.example.json               AWS 環境設定のテンプレート（config.json はこれをコピーして作成）
├── go.mod / go.sum                   Go のモジュール定義
├── app/                              Go のコードと画面（package main）
├── queries/                          保存済みクエリ（.sql）。コンテナに読み取り専用でマウント
├── deploy/                           AWS 側の設定の例（IAM ポリシー、EC2 のユーザーデータ）
└── notes/                            個人用のメモ（アプリケーションには含まれない）
```

`app/` の中身:

- `main.go` と `awscollector.go`: HTTP API、キャッシュ、AWS コレクターです。
- `comparison.go`: 前期間との比較値（前日比・前週比など）の取得です。
- `athena.go` と `savedqueries.go`: Athena クエリの実行と保存済みクエリの読み込みです。
- `wafdetails.go`: WAF のサンプルリクエストとルール別ブロック数の取得です。
- `budget.go`: Athena の利用制限（1日の使用額の上限、パーティションの範囲）です。
- `auth.go` と `templates/login.html`: ログイン画面、セッション、操作ログの記録です。
- `store.go`: SQLite データベース（ユーザー、セッション、操作ログ、クエリ履歴、1日の使用額、停止状態）です。
- `users.go`: ユーザー管理と停止の解除を行う管理者用コマンドです。
- `web/`: ダッシュボードの画面です。`go:embed` でバイナリに同梱するため、Go のコードと同じフォルダに置いています。
- `*_test.go`: テストです。

`deploy/iam-policy.example.json` は、インフラの読み取りと Athena クエリに必要な権限の例です。`deploy/user-data.sh` は EC2 の初期設定です。

## ホスト側の環境変数

以下の変数は、このフォルダの `.env` ファイルに記載するか、Compose 実行前に PowerShell で設定してください。

| 変数 | 必須 | 使用箇所 | 用途 |
| --- | --- | --- | --- |
| `AWS_PROFILE` | PC で実行する場合 | `docker-compose.local.yml` | `%USERPROFILE%\.aws` に設定済みのプロファイル名。EC2（IAM ロール）では指定しません。 |
| `AWS_REGION` | `aws` モードでは必須 | `docker-compose.yml` とアプリケーション | EC2/RDS/CloudWatch のメインリージョン。設定ファイルの `region` より優先されます。 |
| `CLOUDOPS_CONFIG_FILE` | 推奨 | `docker-compose.yml` | ホスト上の設定 JSON のパス。未指定の場合は `config.example.json` を使用します。 |
| `CLOUDOPS_MODE` | 任意 | `docker-compose.yml` | 既定値は `aws`。AWS を呼び出さない場合は `demo` を指定します。 |
| `COMPOSE_FILE` | PC で実行する場合 | Docker Compose | `docker-compose.yml;docker-compose.local.yml`（Linux / macOS では `:` 区切り）。PC の `.aws` をマウントする設定を自動で重ねます。EC2 では指定しません。 |
| `USERPROFILE` | Windows では自動 | `docker-compose.local.yml` | コンテナにマウントする `.aws` フォルダの場所。通常はすでに存在します。 |
| `AWS_CONFIG_DIR` | PC が Linux などの場合 | `docker-compose.local.yml` | `.aws` フォルダのパス。未指定の場合は `%USERPROFILE%\.aws` を使用します。 |
| `CLOUDOPS_TRUSTED_PROXIES` | ALB の後ろで実行する場合 | `docker-compose.yml` とアプリケーション | ALB などの接続元の CIDR（カンマ区切り、例: VPC の CIDR）。この接続元からのリクエストに限り、`X-Forwarded-For`（操作ログの IP）と `X-Forwarded-Proto`（Cookie の Secure）を使います。設定ファイルの `trusted_proxies` より優先されます。 |
| `CLOUDOPS_BIND_ADDRESS` | 任意 | `docker-compose.yml` | ダッシュボードを公開するホスト側のアドレス。既定は `127.0.0.1`（このPCのみ）。下記「VPN 内への公開」を参照。 |
| `CLOUDOPS_PORT` | 任意 | `docker-compose.yml` | ホスト側のポート。既定は `8080`。 |

`.env` の例:

```dotenv
# PC で実行する場合
AWS_PROFILE=your-profile
AWS_REGION=ap-northeast-1
CLOUDOPS_CONFIG_FILE=./config.json
COMPOSE_FILE=docker-compose.yml;docker-compose.local.yml
```

EC2 で実行する場合の `.env` は「EC2 + ALB での運用（HTTPS）」を参照してください。

PowerShell で設定した変数は `.env` の値より優先されます。

次の変数はコンテナ内部で使用するもので、Compose ですでに定義されています。手動で渡す必要はありません: `CLOUDOPS_CONFIG`、`CLOUDOPS_ADDRESS`、`CLOUDOPS_DB`（データベースのパス。`database.path` より優先）。PC で実行する場合は、`docker-compose.local.yml` で `AWS_CONFIG_FILE`、`AWS_SHARED_CREDENTIALS_FILE`、`HOME` も定義されます。

## JSON 設定ファイル

[config.example.json](../config.example.json) をもとに `config.json` を作成し、以下を入力してください。

```json
{
  "mode": "aws",
  "address": "127.0.0.1:8080",
  "region": "your-region",
  "timezone": "UTC",
  "collection_interval": "5m",
  "idle_pause_after": "30m",
  "waf": {
    "web_acl_id": "web-acl-id",
    "web_acl_name": "web-acl-name",
    "region": "us-east-1"
  },
  "ec2": {
    "instance_ids": ["i-xxxxxxxxxxxxxxxxx"],
    "memory_namespace": "CWAgent",
    "memory_metric": "mem_used_percent"
  },
  "rds": { "instance_ids": ["rds-identifier"] },
  "athena": {
    "region": "your-region",
    "database": "",
    "catalog": "AwsDataCatalog",
    "workgroup": "",
    "results_location": "",
    "table": "",
    "queries_dir": "queries",
    "daily_budget_usd": 1.0,
    "max_query_days": 31
  },
  "database": { "path": "data/cloudops.db" },
  "auth": { "session_ttl": "12h", "cookie_secure": false },
  "trusted_proxies": []
}
```

`database.path` は SQLite のファイルです。Docker では環境変数 `CLOUDOPS_DB`（`/data/cloudops.db`）が優先されるため、変更する必要はありません。`auth.session_ttl` はログインの有効期間（既定 `12h`）です。`auth.cookie_secure` は HTTPS で公開する場合だけ `true` にします（HTTP のまま `true` にするとログインできなくなります）。ALB で HTTPS を終端する場合は、`trusted_proxies`（または環境変数 `CLOUDOPS_TRUSTED_PROXIES`）に ALB の接続元を指定すれば、ALB が付ける `X-Forwarded-Proto: https` を見て自動で Secure を付けるため、`false` のままで構いません。`trusted_proxies` には ALB などの接続元の CIDR を指定します。ここに含まれる接続元からのリクエストだけ、`X-Forwarded-For` の最後の値を利用者の IP として操作ログに記録します（空の場合はヘッダーを使わず、接続元の IP を記録します）。

`web_acl_id` と `web_acl_name` は同じ Web ACL のものを指定してください。CloudFront に関連付けられた WAF の場合、メトリクスは `us-east-1` で取得します。EC2 のインスタンス ID と RDS の識別子は、`region` で指定したリージョンに存在している必要があります。

`timezone` は画面上の日時表示に使用されます（例: 日本時間で表示する場合は `Asia/Tokyo`）。表示だけの設定で、バックエンドの収集・キャッシュ・前期間との比較、Athena のパーティション（`log_time`）の指定は常に UTC で行います。グラフの軸とツールチップ、画面下部の期間表示、保存済みクエリの `{{timezone}}`（例: 「最近ブロックされたリクエスト」の時刻列）がこの設定に従います。変更後は `docker compose restart` で反映されます。

カスタム期間の開始・終了は、PC のタイムゾーンにかかわらず日本時間（JST）で入力します。指定できるのは最大 90 日で、未来の日時は選択できません。

`idle_pause_after` の間ダッシュボードへのアクセスがなければ、CloudWatch の定期収集を一時停止して料金を抑えます（既定 `30m`、`"0"` で無効）。次に画面が開かれると、表示する期間のデータをすぐに収集してから表示し（数秒かかります）、定期収集を再開します。ブラウザのタブがバックグラウンドにある間は画面の自動更新が止まるため、アクセスとして数えません。一時停止中かどうかは `/api/health` の `collection_paused` で確認できます。

EC2 のメモリ使用率は AWS の標準メトリクスに含まれないため、OS 側から CloudWatch に送信されているメトリクスを `ec2.memory_namespace` / `ec2.memory_metric` で指定します。ディメンションは `InstanceId` のみであることが前提です。省略時は CloudWatch Agent の既定値を使用します。

| 送信方法 | `memory_namespace` | `memory_metric` |
| --- | --- | --- |
| CloudWatch Agent（既定） | `CWAgent` | `mem_used_percent` |
| CloudWatch Monitoring Scripts（旧方式） | `System/Linux` | `MemoryUtilization` |

実際の値は次のコマンドで確認できます: `aws cloudwatch list-metrics --dimensions Name=InstanceId,Value=<id> --query "Metrics[?Namespace!='AWS/EC2']"`

Glue、ワークグループ、結果出力用の S3 バケットが用意できるまで、Athena の項目は空のままでも構いません。

### WAF のサンプルリクエストとルール別ブロック数

WAF 画面の下部には、Athena を使わずに取得できる 2 つの情報を表示します。

| 表示 | 内容 | 取得元と料金 |
| --- | --- | --- |
| ルール別ブロック数 | 選択した期間に各ルールがブロックしたリクエスト数（上位 10 件） | CloudWatch の `BlockedRequests`（`Rule` ディメンション）。ルールの数だけメトリクスを読むため、固定期間は 15 分に 1 回だけ取得します（ルール 10 個で月 1 USD 未満が目安） |
| サンプルリクエスト（直近3時間） | 時刻、アクション、クライアント IP、国、メソッド、URI、ルール（最新 200 件） | AWS WAF の `GetSampledRequests`（無料）。5 分に 1 回取得します |

- サンプルリクエストは AWS WAF が記録したサンプル（ルールごとに最大 100 件）で、すべてのリクエストではありません。選択した期間に関係なく、常に直近 3 時間のものを表示します。全件を調べる場合は Athena クエリを使用します。
- サンプルリクエストを取得するには、IAM に `wafv2:GetSampledRequests` の権限が必要です。また、Web ACL の各ルールで「サンプルリクエスト」（`SampledRequestsEnabled`）が有効になっている必要があります（コンソールで作成した場合は既定で有効）。
- ルールの一覧は CloudWatch の `ListMetrics` から 1 時間に 1 回取得します（`wafv2:GetWebACL` の権限は不要）。
- `waf.scope` には Web ACL の種類を指定します。CloudFront 用は `CLOUDFRONT`（既定）、ALB や API Gateway 用は `REGIONAL` です。Web ACL の ARN は `web_acl_name` と `web_acl_id` から組み立てるため、`web_acl_name` には Web ACL の名前（メトリクス名と同じ場合）を指定してください。

### Athena クエリ

「クエリ」画面から WAF ログに対して Athena クエリを手動で実行できます。クエリは「実行」を押したときだけ発行され、定期収集では使用しないため、使わなければ Athena の料金は発生しません。

- 実行には `athena.database` と、`athena.workgroup` または `athena.results_location` が必要です。
- `athena.table` には WAF ログのテーブル名を指定します。保存済みクエリの `{{table}}` に入ります。
- 保存済みクエリは `queries/` フォルダの `.sql` ファイルです。プルダウンで選択するとエディタに表示され、実行前に編集できます。書き方とプレースホルダーは [queries/README.md](../queries/README.md) を参照してください。
- 実行できるのは読み取り専用のクエリ（`SELECT` / `WITH`）のみです。結果は画面に最大 1,000 行まで表示します。
- 「CSVダウンロード」で、Athena が S3（`results_location`）に保存した結果の全件を CSV でダウンロードできます。ブラウザは有効期限 5 分の署名付き URL で S3 から直接ダウンロードします。結果バケットに対する `s3:GetObject` 権限が必要で、バケットは Athena と同じリージョンにある必要があります。
- 履歴には実行者、スキャン量、推定料金（1TB あたり約 5 USD、1 クエリ最小 10MB）を表示します。画面には最新 20 件を表示し、すべての履歴はデータベースに保存します。コンテナを再起動した後は、成功したクエリの結果を Athena から取り直して表示します（Athena の料金は発生しません）。
- 全員が全員のクエリ履歴を見られます。機密情報を含むクエリは「結果を共有しない」にチェックを入れて実行してください。他のユーザーには実行者・クエリ名・状態・スキャン量だけが表示され、SQL・結果・CSV ダウンロードは実行したユーザーだけが利用できます。この場合も、SQL を含めてデータベースには記録されます。実行中のクエリのキャンセルは誰でもできます。
- 想定外の高額クエリを防ぐため、ワークグループの「クエリごとのデータ使用量の制限」を設定しておくことを推奨します。

#### 利用制限

複数人で使っても料金が膨らまないよう、クエリの実行前に次の制限を確認します。

| 制限 | 内容 | 設定 |
| --- | --- | --- |
| 1日の使用額の上限 | 今日（`timezone` の日付）の推定料金の合計が上限に達すると、クエリの実行を停止します。**日付が変わっても、コンテナを再作成しても自動では解除されません**（停止状態はデータベースに保存します）。 | `athena.daily_budget_usd`（既定 `1.0` USD） |
| パーティションの範囲 | SQL に `log_time BETWEEN 'yyyy/MM/dd/HH/mm' AND 'yyyy/MM/dd/HH/mm'` がないクエリと、範囲が上限を超えるクエリは実行しません。コメント内の条件は数えません。 | `athena.max_query_days`（既定 `31` 日） |
| 同時実行 | 実行中のクエリがある間は、他の人も含めて新しいクエリを実行できません。 | なし（1 件） |

- 今日の使用額は、ワークグループの実行履歴（`ListQueryExecutions` / `BatchGetQueryExecution`）から計算します。同じワークグループでコンソールなどから実行したクエリも含まれ、コンテナを再起動しても値は変わりません。ダッシュボード専用のワークグループを `athena.workgroup` に指定してください。
- 使用額を確認できない場合（権限不足など）は、安全のためクエリを実行しません。
- 確認した使用額は日ごとにデータベース（`athena_daily_usage`）に記録します。その日に最後に確認した時点の値のため、日付が変わる直前に実行したクエリは含まれないことがあります。
- 「クエリ」画面に今日の使用額と上限を表示します。保存済みクエリを編集した場合や自由に入力した SQL は、実行前に確認ダイアログを表示します。
- 使用額はスキャン量からの推定値です。実際の請求額とは端数処理などで差が出ることがあります。また、実行中のクエリの料金は終了後に加算されるため、上限を最大でクエリ 1 件分超えることがあります（ワークグループの「クエリごとのデータ使用量の制限」で上限を抑えられます）。

**上限に達したときの解除方法（管理者）**: 次のどちらかで解除します。解除後も今日の使用額が上限以上であれば、次の確認で再び停止します。

1. `config.json` の `athena.daily_budget_usd` を停止したときの上限より大きい値に変更し、コンテナを再作成します。起動時に停止状態を解除します。

   ```powershell
   docker compose up -d --force-recreate
   ```

2. 上限を変えずに解除する場合（日付が変わった後など）は、次のコマンドを実行します。再起動は不要です。

   ```powershell
   docker compose exec cloudops /app/cloudops budget unlock
   ```

### 値の確認方法

| 項目 | コンソール | CLI（読み取り専用） |
| --- | --- | --- |
| `region` | EC2 → インスタンス（URL または右上のリージョン） | `aws ec2 describe-instances --instance-ids <id> --query "Reservations[].Instances[].Placement.AvailabilityZone"` |
| `waf.web_acl_id` / `web_acl_name` | WAF & Shield → Web ACLs（CloudFront の場合はリージョンを「Global」に） | `aws wafv2 list-web-acls --scope CLOUDFRONT --region us-east-1` |
| `athena.database` | Athena → クエリエディタ、または Glue → データベース | `aws glue get-databases --region <region>` |
| `athena.workgroup` | Athena → ワークグループ（既定は `primary`） | `aws athena list-work-groups --region <region>` |
| `athena.results_location` | Athena → ワークグループ → クエリ結果の場所 | `aws athena get-work-group --work-group primary --region <region>` |

## PowerShell での初回実行

この README があるフォルダで実行します。

```powershell
# .env を使わない場合のみ
$env:AWS_PROFILE = "your-profile"
$env:AWS_REGION = "your-region"
$env:CLOUDOPS_CONFIG_FILE = "./config.json"

aws sts get-caller-identity

Copy-Item config.example.json config.json
# config.json を編集し、環境の ID を入力してください。

docker compose up -d --build
```

最初にログインするユーザーを作成します（下記「ログインとユーザー管理」）。

```powershell
docker compose exec cloudops /app/cloudops user add <ユーザー名>
```

<http://127.0.0.1:8080> を開いてログインしてください。既定ではホストに公開されるのは `127.0.0.1:8080` のみで、コンテナ内ではサービスが `0.0.0.0:8080` で待ち受けます。

## ログインとユーザー管理

ダッシュボードの利用にはログインが必要です（`/api/health` を除く）。ユーザーは 1 種類のみで、全員が同じ機能を使えます。画面からユーザーを作成することはできないため、管理者がコンテナ内でコマンドを実行します。

| 操作 | コマンド |
| --- | --- |
| ユーザーを作成する | `docker compose exec cloudops /app/cloudops user add <ユーザー名>` |
| パスワードを再設定する | `docker compose exec cloudops /app/cloudops user passwd <ユーザー名>` |
| ユーザーを無効にする / 有効に戻す | `docker compose exec cloudops /app/cloudops user disable <ユーザー名>`（`enable` で戻す） |
| ユーザーの一覧 | `docker compose exec cloudops /app/cloudops user list` |
| Athena の停止を解除する | `docker compose exec cloudops /app/cloudops budget unlock` |

- `add` と `passwd` はランダムなパスワードを生成して一度だけ表示します。本人に安全な方法で伝えてください。パスワードを指定する場合は `-T` と `--password-stdin` を付け、標準入力から渡します（12 文字以上）: `"<パスワード>" | docker compose exec -T cloudops /app/cloudops user add <ユーザー名> --password-stdin`
- ログイン後、各ユーザーは画面右上の「パスワード変更」から自分のパスワードを変更できます（現在のパスワードが必要、12 文字以上）。変更すると、他の端末やブラウザのログインは解除されます。現在のパスワードの間違いはログイン失敗として数えます。
- パスワードの再設定と無効化を行うと、そのユーザーのログイン中のセッションは無効になります。
- ユーザー名に使える文字は英数字と `.` `_` `@` `-` です（大文字・小文字は区別しません）。
- 続けて 5 回ログインに失敗したユーザーは、15 分間ログインできなくなります。
- パスワードは PBKDF2-SHA256 でハッシュ化して保存します。セッションの Cookie は `HttpOnly` / `SameSite=Strict` で、データベースにはトークンのハッシュだけを保存します。

### データベース

ユーザー、セッション、操作ログ、クエリ履歴、1日の使用額、Athena の停止状態は SQLite（コンテナ内の `/data/cloudops.db`）に保存します。`/data` は Docker のボリューム `cloudops-data` で、コンテナを再作成しても残ります（`docker compose down -v` を実行するとボリュームごと削除されるため注意してください）。コンテナで書き込みができるのはこのボリュームだけです。

| テーブル | 内容 |
| --- | --- |
| `users` | ユーザー名、パスワードのハッシュ、無効化・ログイン失敗の状態 |
| `sessions` | ログイン中のセッション（トークンのハッシュ、有効期限） |
| `audit_log` | 操作ログ: `login`、`login_failed`、`logout`、`password_change`、`password_change_failed`、`query_start`、`query_cancel`、`csv_download`、`dashboard_refresh`、`budget_lock`、`budget_unlock`、`user_add` など |
| `athena_queries` | 実行したすべてのクエリ（実行者、SQL、共有しない設定、状態、スキャン量、推定料金）。結果の行は保存しません |
| `athena_daily_usage` | 日ごとの Athena 使用額（推定、USD） |
| `budget_lock` | 使用額の上限による停止状態 |

バックアップや内容の確認は、コンテナを停止してからデータベースのフォルダをコピーし、ホスト側の SQLite ツールで開きます。

```powershell
docker compose stop
docker compose cp cloudops:/data ./backup
docker compose start
```

## VPN 内への公開

ダッシュボードはログインが必要ですが、通信は HTTP のため暗号化されません（パスワードと Cookie がネットワーク上をそのまま流れます）。**公開は VPN 内に限定してください。** HTTPS で公開する場合は「EC2 + ALB での運用（HTTPS）」を参照してください。

### このPCから公開する場合（VPN の IP にバインド）

1. `ipconfig` で VPN アダプターの IPv4 アドレスと、VPN のネットワーク範囲を確認します。
2. `.env` の `CLOUDOPS_BIND_ADDRESS` に VPN の IP を設定し、コンテナを再作成します。

   ```dotenv
   CLOUDOPS_BIND_ADDRESS=10.x.x.x
   ```

   ```powershell
   docker compose up -d
   ```

3. Windows ファイアウォールで、VPN のネットワーク範囲からのみポートを許可します（管理者の PowerShell）。

   ```powershell
   New-NetFirewallRule -DisplayName "CloudOps dashboard" -Direction Inbound -Protocol TCP -LocalPort 8080 -RemoteAddress <VPNのネットワーク範囲（例: 10.8.0.0/16）> -Action Allow
   ```

4. VPN 内のユーザーは `http://<VPNのIP>:8080/` でアクセスできます。

VPN の IP が変わった場合は `.env` を更新して `docker compose up -d` を再実行してください。VPN に接続していない状態で起動すると、そのアドレスが存在しないためコンテナの起動に失敗します。

公開をやめる場合は `CLOUDOPS_BIND_ADDRESS=127.0.0.1` に戻して再起動し、ファイアウォールのルールを削除します（`Remove-NetFirewallRule -DisplayName "CloudOps dashboard"`）。

### 専用マシンで運用する場合

- IP が変わる可能性がある場合は `CLOUDOPS_BIND_ADDRESS=0.0.0.0` とし、**ファイアウォールで VPN のネットワーク範囲以外からのアクセスを必ず遮断**してください。
- Linux では Docker が iptables を直接操作するため、公開したポートは `ufw` などのルールを通らないことがあります。VPN インターフェースの IP に固定でバインドするか、`DOCKER-USER` チェーンで制限してください。
- EC2 では `.aws` をマウントせず、インスタンスの IAM ロールを使います（下記「EC2 + ALB での運用（HTTPS）」）。Linux の PC で `.aws` を使う場合は `.env` の `AWS_CONFIG_DIR` にフォルダのパスを指定し、`COMPOSE_FILE` で `docker-compose.local.yml` を重ねます。コンテナは UID 65532（nonroot）で動作するため、認証情報ファイルをこのユーザーが読み取れるようにする必要があります。
- 長期間の運用では、SSO の一時トークンより、このダッシュボード専用の IAM ユーザー（[deploy/iam-policy.example.json](../deploy/iam-policy.example.json) の権限のみ）を使うと再ログインが不要になります。

AWS を使わずに実行する場合:

```powershell
$env:CLOUDOPS_MODE = "demo"
docker compose up -d --build
```

停止する場合:

```powershell
docker compose down
```

コードまたはフロントエンドを変更した後:

```powershell
docker compose build --no-cache
docker compose up -d --force-recreate
```

## EC2 + ALB での運用（HTTPS）

ALB で HTTPS を終端し、EC2 上のコンテナには ALB から HTTP で転送します。

```
利用者のブラウザ ──HTTPS 443──▶ ALB（証明書 *.example.jp、HTTP 80 は 443 にリダイレクト）
                                   │ HTTP 8080（X-Forwarded-For / X-Forwarded-Proto を付けて転送）
                                   ▼
                                 EC2（Docker、cloudops コンテナ、IAM ロール）──▶ AWS API（CloudWatch、Athena など）
```

1. **IAM ロール**: [deploy/iam-policy.example.json](../deploy/iam-policy.example.json) の権限を持つロールを作成し、インスタンスプロファイルとして EC2 に付けます。アクセスキーは使いません。
2. **IMDSv2 のホップ数の上限を 2 にする**: コンテナは Docker のネットワークを経由してインスタンスメタデータ（IAM ロールの一時認証情報）を取得するため、既定の 1 では届きません。起動時は「高度な詳細」→「メタデータのレスポンスホップ制限」を `2`、「メタデータのバージョン」を「V2 のみ（トークンは必須）」にします。起動済みのインスタンスは次のコマンドで変更できます。

   ```bash
   aws ec2 modify-instance-metadata-options --instance-id <インスタンスID> \
     --http-endpoint enabled --http-tokens required --http-put-response-hop-limit 2
   ```

3. **EC2 にデプロイする**: Docker と Docker Compose プラグインをインストールし、このフォルダをコピーして `config.json` を作成します。`.env` は次のようにします（`AWS_PROFILE` と `COMPOSE_FILE` は書きません）。t3.small など メモリ 2GB のインスタンスでは、イメージのビルド中にメモリが不足することがあるため、1〜2GB のスワップを追加しておくと安全です。

   ```dotenv
   AWS_REGION=ap-northeast-1
   CLOUDOPS_CONFIG_FILE=./config.json
   # ALB から転送を受けるため、すべてのインターフェースで待ち受ける（アクセスはセキュリティグループで ALB のみに制限する）
   CLOUDOPS_BIND_ADDRESS=0.0.0.0
   CLOUDOPS_PORT=8080
   # ALB の IP は変わるため、ALB のサブネットを含む VPC の CIDR を指定する
   CLOUDOPS_TRUSTED_PROXIES=10.0.0.0/16
   ```

   ```bash
   docker compose up -d --build
   docker compose exec cloudops /app/cloudops user add <ユーザー名>
   ```

4. **ターゲットグループ**: ターゲットタイプは「インスタンス」、プロトコル HTTP、ポート `8080` で作成し、EC2 を登録します。ヘルスチェックのパスは `/api/health`（ログイン不要、200 を返す）です。セッションはデータベースに保存するため、スティッキーセッションは不要です。
5. **ALB**: HTTPS（443）のリスナーに証明書を設定してターゲットグループに転送し、HTTP（80）のリスナーは HTTPS にリダイレクトします。VPN からのみ利用する場合は内部向け（internal）の ALB にします。インターネット向けにする場合は、次のセキュリティグループで接続元を必ず制限してください。
6. **セキュリティグループ**:

   | 対象 | インバウンド | 接続元 |
   | --- | --- | --- |
   | ALB | 443、80 | 利用者のネットワーク（VPN や社内の CIDR） |
   | EC2 | 8080 | ALB のセキュリティグループのみ |

   EC2 の 8080 をインターネットに公開しないでください。ALB を経由しないアクセスは HTTPS になりません。
7. **DNS**: Route 53 でダッシュボードのドメインのエイリアスレコード（A）を ALB に向けます。別の DNS サービスの場合は ALB の DNS 名への CNAME を作成します。
8. **確認**: `https://<ドメイン>/` を開いてログイン画面が表示されることを確認します。ログイン後、`audit_log` の `ip` に利用者の IP（ALB の IP ではない）が記録されていれば、`CLOUDOPS_TRUSTED_PROXIES` が正しく設定されています。

### 更新のデプロイ（PC でビルドしたイメージを EC2 に送る）

t3.small などメモリ 2GB のインスタンスでは、イメージのビルド（AWS SDK と SQLite のコンパイル）にスワップを使っても長い時間がかかります。コードや画面を変更したときは PC でビルドし、イメージを tar ファイルにして送ると、EC2 ではビルドせずに起動できます。ユーザー、操作ログ、クエリ履歴、使用額はボリューム `cloudops-data` に保存されているため、コンテナを作り直しても消えません。

1. **PC でテストする**（このフォルダで実行。Docker Desktop を起動しておきます）。

   ```powershell
   docker run --rm -v "${PWD}:/src" -w /src golang:1.27.4-alpine go test ./...
   ```

2. **PC でビルドして保存する**。EC2 が arm64（t4g など）の場合は `linux/arm64` にします。

   ```powershell
   docker build --platform linux/amd64 -t ec2-dash-cloudops:latest .
   docker save ec2-dash-cloudops:latest -o ec2-dash-cloudops.tar
   ```

3. **EC2 に送る**。`ec2-dash-cloudops.tar` を SFTP などで EC2 のホームフォルダ（`~/`）に送ります。`docker-compose.yml`、`config.json`、`queries/` などを変更した場合は、それらも `~/ec2-dash/` に送ります（`app/` は送らなくても構いません）。`.env.local` と `docker-compose.local.yml` は送りません。
4. **EC2 でイメージを読み込む**。

   ```bash
   docker load -i ~/ec2-dash-cloudops.tar
   ```

   同じ名前のイメージがすでにある場合は、次のように表示されます。`latest` が新しいイメージを指すようになり、古いイメージは名前のない（`<none>`）イメージとして残ります。この時点では、動いているコンテナはまだ古いイメージのままです。

   ```
   The image ec2-dash-cloudops:latest already exists, renaming the old one with ID sha256:… to empty string
   Loaded image: ec2-dash-cloudops:latest
   ```

5. **コンテナを作り直す**。`--build` は付けません（付けると、送ったイメージではなく EC2 上でビルドし直します）。イメージが変わっていれば `Recreated` と表示されます。

   ```bash
   cd ~/ec2-dash && docker compose up -d
   ```

6. **確認する**。コンテナのイメージ ID が、新しい `latest` の ID と同じであることを確認します（4 で表示された古い ID と同じなら、作り直されていません）。

   ```bash
   docker images ec2-dash-cloudops
   docker inspect --format '{{.Image}}' $(docker compose ps -q cloudops)
   ```

   その後、`https://<ドメイン>/` でログインし、変更した機能を確認します。

7. **古いイメージを削除する**。

   ```bash
   docker image prune -f
   ```

`config.json` だけを変更した場合はイメージを送る必要はなく、`docker compose restart` で反映されます。`queries/*.sql` の変更は再起動も不要です。

## 認証情報と権限

EC2 ではインスタンスの IAM ロールを使います（上記「EC2 + ALB での運用（HTTPS）」）。PC で実行する場合は、ホスト側で `aws configure --profile your-profile` を実行するか、SSO を使用してプロファイルを設定し、`COMPOSE_FILE` で `docker-compose.local.yml` を重ねてください。`%USERPROFILE%\.aws` フォルダは読み取り専用でマウントされ、アクセスキーがイメージにコピーされることはありません。

SSO を使用する場合は、コンテナを起動する前にホスト側で `aws sso login --profile your-profile` を実行してください。マウントは読み取り専用のため、コンテナ内からトークンを更新することはできません。

[deploy/iam-policy.example.json](../deploy/iam-policy.example.json) を出発点として、環境に合わせてリソースを絞り込んでください。STS、EC2、RDS、CloudWatch の権限が含まれています。Athena を使用する場合は、Athena、Glue、S3 にも対応する権限が必要です。1日の使用額の確認に `athena:ListQueryExecutions` と `athena:BatchGetQueryExecution` を使用します（ない場合、クエリは実行されません）。
