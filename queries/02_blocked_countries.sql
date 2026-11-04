-- name: 国別ブロック数 上位10件
-- description: 期間内にブロックされたリクエストを送信元の国ごとに集計し、全リクエストに対する割合も表示します。
SELECT
  httprequest.country AS country,
  COUNT_IF(action = 'BLOCK') AS blocked_requests,
  COUNT(*) AS total_requests,
  ROUND(100.0 * COUNT_IF(action = 'BLOCK') / COUNT(*), 2) AS blocked_percent
FROM {{table}}
WHERE log_time BETWEEN '{{start_minute}}' AND '{{end_minute}}'
  AND timestamp BETWEEN {{start_ms}} AND {{end_ms}}
GROUP BY httprequest.country
ORDER BY blocked_requests DESC
LIMIT 10
