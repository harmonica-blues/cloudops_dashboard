-- name: カウントモードで一致したルール 上位10件
-- description: ブロックはせずカウントのみ行うルール（nonterminatingmatchingrules）の一致数を表示します。ルールをブロックに切り替える前の確認に使います。
SELECT
  rule.ruleid AS rule_id,
  rule.action AS action,
  COUNT(*) AS requests
FROM {{table}}
CROSS JOIN UNNEST(nonterminatingmatchingrules) AS matched(rule)
WHERE log_time BETWEEN '{{start_minute}}' AND '{{end_minute}}'
  AND timestamp BETWEEN {{start_ms}} AND {{end_ms}}
GROUP BY rule.ruleid, rule.action
ORDER BY requests DESC
LIMIT 10
