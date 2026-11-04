-- name: 最近ブロックされたリクエスト 100件
-- description: 期間内にブロックされたリクエストを新しい順に表示します。時刻は config.json の timezone で表示します。
SELECT
  format_datetime(from_unixtime(timestamp / 1000) AT TIME ZONE '{{timezone}}', 'yyyy-MM-dd HH:mm:ss') AS time,
  httprequest.clientip AS client_ip,
  httprequest.country AS country,
  httprequest.httpmethod AS method,
  httprequest.uri AS uri,
  terminatingruleid AS rule_id
FROM {{table}}
WHERE log_time BETWEEN '{{start_minute}}' AND '{{end_minute}}'
  AND action = 'BLOCK'
  AND timestamp BETWEEN {{start_ms}} AND {{end_ms}}
ORDER BY timestamp DESC
LIMIT 100
