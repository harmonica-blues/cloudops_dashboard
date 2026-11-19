# Saved queries

[日本語](README.md) | **English** · [Operations guide](../docs/operations.en.md)

The `.sql` files in this directory appear in filename order in the dashboard's Queries selector. Add or edit a file and reopen the view to see the change; no container restart is required.

## File format

```sql
-- name: Name shown in the selector
-- description: Description shown in the UI (optional)
SELECT ...
FROM {{table}}
WHERE log_time BETWEEN '{{start_minute}}' AND '{{end_minute}}'
  AND timestamp BETWEEN {{start_ms}} AND {{end_ms}}
```

- The `-- name:` and `-- description:` lines are removed from the SQL. Without a name, the filename is used.
- Only read-only `SELECT` / `WITH` queries are accepted.
- Prefix filenames with numbers such as `01_`, `02_`, and so on to control display order.
- The application interface is Japanese; use Japanese names and descriptions for templates intended for its users.

## Placeholders

Selecting a template replaces these placeholders in the editor. The range comes from the page's period selector. You can edit the resulting SQL before execution.

| Placeholder | Replacement | Example |
| --- | --- | --- |
| `{{table}}` | `athena.table` | `waf_logs` |
| `{{database}}` | `athena.database` | `wafdb` |
| `{{start_ms}}` / `{{end_ms}}` | Start / end as Unix milliseconds, for comparison with the WAF log `timestamp` column | `1761436800000` |
| `{{start}}` / `{{end}}` | UTC start / end for `TIMESTAMP` literals | `2026-10-26 00:00:00` |
| `{{start_date}}` / `{{end_date}}` | UTC start / end dates | `2026/10/26` |
| `{{start_minute}}` / `{{end_minute}}` | UTC partition projection range for `log_time`, formatted as `yyyy/MM/dd/HH/mm`. The end includes a 15-minute margin for log delivery delays. | `2026/10/26/06/15` |
| `{{timezone}}` | The configured `timezone` | `Asia/Tokyo` |

## Scan costs

The application's Athena estimate uses about USD 5 per TB scanned, with a 10 MB minimum per query.

The WAF log table uses partition projection on `log_time` (`yyyy/MM/dd/HH/mm`). **Include the following condition in every new query.** Without partition filtering, a query can scan logs across the entire retention period and substantially increase costs.

```sql
WHERE log_time BETWEEN '{{start_minute}}' AND '{{end_minute}}'
  AND timestamp BETWEEN {{start_ms}} AND {{end_ms}}
```

`log_time` narrows the folders scanned; `timestamp` precisely filters the requested time range.

Queries without `log_time BETWEEN '...' AND '...'`, or with a partition range longer than `athena.max_query_days` (default 31 days), are rejected. See [Usage limits](../docs/operations.en.md#usage-limits) for the daily spending limit and other controls.
