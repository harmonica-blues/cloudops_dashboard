-- name: ブロックされたIP 上位20件
-- description: 期間内にブロックされたリクエストの送信元IPと国を件数順に表示します。
SELECT
  httprequest.clientip AS client_ip,
  httprequest.country AS country,
  COUNT(*) AS blocked_requests
FROM {{table}}
WHERE log_time BETWEEN '{{start_minute}}' AND '{{end_minute}}'
  AND action = 'BLOCK'
  AND timestamp BETWEEN {{start_ms}} AND {{end_ms}}
GROUP BY httprequest.clientip, httprequest.country
ORDER BY blocked_requests DESC
LIMIT 20
