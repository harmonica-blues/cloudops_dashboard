-- name: ブロックされたURI 上位20件
-- description: 期間内にブロックされたリクエストの対象URIを件数順に表示します。攻撃対象になっているパスの確認に使います。
SELECT
  httprequest.uri AS uri,
  COUNT(*) AS blocked_requests,
  COUNT(DISTINCT httprequest.clientip) AS unique_ips
FROM {{table}}
WHERE log_time BETWEEN '{{start_minute}}' AND '{{end_minute}}'
  AND action = 'BLOCK'
  AND timestamp BETWEEN {{start_ms}} AND {{end_ms}}
GROUP BY httprequest.uri
ORDER BY blocked_requests DESC
LIMIT 20
