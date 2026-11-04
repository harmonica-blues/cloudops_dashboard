# 保存済みクエリ

このフォルダの `.sql` ファイルが、ダッシュボードの「クエリ」画面のプルダウンにファイル名順で表示されます。ファイルを追加・編集すると、コンテナを再起動しなくても画面を開き直すだけで反映されます。

## ファイルの書き方

```sql
-- name: プルダウンに表示する名前
-- description: 画面に表示する説明（任意）
SELECT ...
FROM {{table}}
WHERE timestamp BETWEEN {{start_ms}} AND {{end_ms}}
```

- `-- name:` と `-- description:` の行は SQL から取り除かれます。省略した場合はファイル名が名前になります。
- 実行できるのは読み取り専用のクエリ（`SELECT` / `WITH`）のみです。
- ファイル名の先頭に番号（`01_`、`02_` …）を付けると、表示順を制御できます。

## プレースホルダー

クエリを選択すると、次のプレースホルダーが値に置き換えられた状態でエディタに表示されます。期間はページ上部の期間選択の値です。実行前にエディタ上で自由に編集できます。

| プレースホルダー | 置き換えられる値 | 例 |
| --- | --- | --- |
| `{{table}}` | `athena.table` の値 | `waf_logs` |
| `{{database}}` | `athena.database` の値 | `wafdb` |
| `{{start_ms}}` / `{{end_ms}}` | 期間の開始・終了（UNIX ミリ秒）。WAF ログの `timestamp` 列と比較できます | `1761436800000` |
| `{{start}}` / `{{end}}` | 期間の開始・終了（UTC、`TIMESTAMP` リテラル用） | `2026-10-26 00:00:00` |
| `{{start_date}}` / `{{end_date}}` | 期間の開始日・終了日（UTC） | `2026/10/26` |
| `{{start_minute}}` / `{{end_minute}}` | パーティション射影 `log_time`（`yyyy/MM/dd/HH/mm`、UTC）用の開始・終了。ログ配信の遅延を考慮し、終了は期間の 15 分後になります | `2026/10/26/06/15` |
| `{{timezone}}` | `timezone` の値 | `Asia/Tokyo` |

## 料金について

Athena はスキャンしたデータ量に応じて課金されます（1TB あたり約 5 USD、1 クエリ最小 10MB）。

WAF ログのテーブルは `log_time` 列（`yyyy/MM/dd/HH/mm`）でパーティション射影されています。**新しいクエリにも必ず次の条件を入れてください。** これがないと全期間のログをスキャンし、料金が大きくなります。

```sql
WHERE log_time BETWEEN '{{start_minute}}' AND '{{end_minute}}'
  AND timestamp BETWEEN {{start_ms}} AND {{end_ms}}
```

`log_time` で読み込むフォルダを絞り込み、`timestamp` で期間を正確に絞り込みます。

`log_time BETWEEN '...' AND '...'` がないクエリと、`log_time` の範囲が `athena.max_query_days`（既定 31 日）を超えるクエリは実行されません。1日の使用額の上限などの利用制限は [README.md](../README.md#利用制限) を参照してください。
