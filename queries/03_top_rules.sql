-- name: WAFルール別の処理数 上位10件
-- description: リクエストを最終的にブロック・チャレンジ等したルール（terminatingruleid）を件数順に表示します。デフォルトアクションは除外します。
SELECT
  terminatingruleid AS rule_id,
  action,
  COUNT(*) AS requests
FROM {{table}}
WHERE log_time BETWEEN '{{start_minute}}' AND '{{end_minute}}'
  AND timestamp BETWEEN {{start_ms}} AND {{end_ms}}
  AND terminatingruleid <> 'Default_Action'
GROUP BY terminatingruleid, action
ORDER BY requests DESC
LIMIT 10
