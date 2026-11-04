package main

// Athena への手動クエリ実行。クエリはユーザーが画面から実行したときだけ発行し、定期収集では一切使用しない。

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	athenatypes "github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	// 画面に表示する結果の最大行数。全件が必要な場合は S3 の結果ファイルを参照する
	maxResultRows = 1000
	// 画面に表示し、メモリ上に保持するクエリ履歴の件数（すべての履歴はデータベースに保存する）
	maxQueryHistory = 20
	// Athena の料金（スキャン量 1TB あたり USD）と、1クエリあたりの最小課金スキャン量
	athenaPricePerTB    = 5.0
	athenaMinBilledByte = 10 * 1024 * 1024
	// CSV ダウンロード用の署名付き URL の有効期限
	csvLinkExpiry = 5 * time.Minute
)

type AthenaRunner struct {
	client    *athena.Client
	presigner *s3.PresignClient
	cfg       AthenaConfig
}

// AthenaStatus は画面に表示する Athena の設定状態。
type AthenaStatus struct {
	Configured bool     `json:"configured"`
	Missing    []string `json:"missing,omitempty"`
	Message    string   `json:"message,omitempty"`
	Region     string   `json:"region"`
	Database   string   `json:"database"`
	Catalog    string   `json:"catalog"`
	Workgroup  string   `json:"workgroup"`
	Table      string   `json:"table"`
	// 今日の使用額と制限の状態。/api/athena で Athena が使える場合だけ返す
	Usage *AthenaUsage `json:"usage,omitempty"`
}

// athenaRegion は athena.region を優先し、未指定ならメインのリージョンを使う。
func athenaRegion(cfg Config) string {
	if region := strings.TrimSpace(cfg.Athena.Region); region != "" {
		return region
	}
	return cfg.Region
}

// athenaMissingSettings はクエリ実行に必要で、未設定の項目を返す。
func athenaMissingSettings(cfg AthenaConfig) []string {
	var missing []string
	if strings.TrimSpace(cfg.Database) == "" {
		missing = append(missing, "athena.database")
	}
	if strings.TrimSpace(cfg.Workgroup) == "" && strings.TrimSpace(cfg.ResultsLocation) == "" {
		missing = append(missing, "athena.workgroup または athena.results_location")
	}
	return missing
}

func newAthenaRunner(ctx context.Context, cfg Config) (*AthenaRunner, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(athenaRegion(cfg)))
	if err != nil {
		return nil, fmt.Errorf("Athena用AWS設定の読み込みに失敗しました: %w", err)
	}
	// 結果の S3 バケットは Athena と同じリージョンにある前提
	return &AthenaRunner{client: athena.NewFromConfig(awsCfg), presigner: s3.NewPresignClient(s3.NewFromConfig(awsCfg)), cfg: cfg.Athena}, nil
}

// csvDownloadURL は Athena が S3 に保存した結果 CSV（全件）をダウンロードするための署名付き URL を返す。
// ブラウザが S3 から直接ダウンロードするため、大きな結果でもコンテナを経由しない。
func (r *AthenaRunner) csvDownloadURL(ctx context.Context, query *Query) (string, error) {
	bucket, key, err := parseS3URI(query.outputLocation)
	if err != nil {
		return "", err
	}
	request, err := r.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     aws.String(bucket),
		Key:                        aws.String(key),
		ResponseContentDisposition: aws.String(csvContentDisposition(query)),
		ResponseContentType:        aws.String("text/csv; charset=utf-8"),
	}, s3.WithPresignExpires(csvLinkExpiry))
	if err != nil {
		return "", fmt.Errorf("CSVダウンロード用URLの作成に失敗しました: %w", err)
	}
	return request.URL, nil
}

// parseS3URI は s3://bucket/key 形式をバケット名とキーに分ける。
func parseS3URI(uri string) (string, string, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(uri), "s3://")
	bucket, key, found := strings.Cut(rest, "/")
	if !ok || !found || bucket == "" || key == "" {
		return "", "", fmt.Errorf("結果ファイルの場所が不正です: %q", uri)
	}
	return bucket, key, nil
}

// csvContentDisposition はダウンロード時のファイル名（クエリ名_開始日時.csv）を指定する。
// 日本語のファイル名は filename* で渡し、対応していないブラウザ向けに ASCII のファイル名も付ける。
func csvContentDisposition(query *Query) string {
	stamp := "result"
	if started, err := time.Parse(time.RFC3339, query.StartedAt); err == nil {
		stamp = started.UTC().Format("20060102_150405")
	}
	name := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, strings.TrimSpace(query.Name))
	if name == "" {
		name = "athena"
	}
	fileName := fmt.Sprintf("%s_%s.csv", name, stamp)
	return fmt.Sprintf(`attachment; filename="athena_%s.csv"; filename*=UTF-8''%s`, stamp, url.PathEscape(fileName))
}

func (r *AthenaRunner) start(ctx context.Context, sql string) (string, error) {
	catalog := strings.TrimSpace(r.cfg.Catalog)
	if catalog == "" {
		catalog = "AwsDataCatalog"
	}
	input := &athena.StartQueryExecutionInput{
		QueryString:           aws.String(sql),
		QueryExecutionContext: &athenatypes.QueryExecutionContext{Database: aws.String(r.cfg.Database), Catalog: aws.String(catalog)},
	}
	if workgroup := strings.TrimSpace(r.cfg.Workgroup); workgroup != "" {
		input.WorkGroup = aws.String(workgroup)
	}
	if location := strings.TrimSpace(r.cfg.ResultsLocation); location != "" {
		input.ResultConfiguration = &athenatypes.ResultConfiguration{OutputLocation: aws.String(location)}
	}
	output, err := r.client.StartQueryExecution(ctx, input)
	if err != nil {
		return "", fmt.Errorf("Athenaクエリの開始に失敗しました: %w", err)
	}
	return aws.ToString(output.QueryExecutionId), nil
}

func (r *AthenaRunner) stop(ctx context.Context, id string) error {
	if _, err := r.client.StopQueryExecution(ctx, &athena.StopQueryExecutionInput{QueryExecutionId: aws.String(id)}); err != nil {
		return fmt.Errorf("Athenaクエリのキャンセルに失敗しました: %w", err)
	}
	return nil
}

// refresh は実行状態・統計を取得して query を更新する。成功していれば結果も取得する（取得済みなら再取得しない）。
func (r *AthenaRunner) refresh(ctx context.Context, query *Query) error {
	output, err := r.client.GetQueryExecution(ctx, &athena.GetQueryExecutionInput{QueryExecutionId: aws.String(query.ID)})
	if err != nil {
		return fmt.Errorf("Athenaクエリの状態取得に失敗しました: %w", err)
	}
	execution := output.QueryExecution
	if execution == nil {
		return nil
	}
	if execution.ResultConfiguration != nil {
		query.outputLocation = aws.ToString(execution.ResultConfiguration.OutputLocation)
	}
	if status := execution.Status; status != nil {
		query.State = string(status.State)
		if status.StateChangeReason != nil {
			query.Error = aws.ToString(status.StateChangeReason)
		}
		if status.CompletionDateTime != nil {
			query.FinishedAt = status.CompletionDateTime.UTC().Format(time.RFC3339)
		}
	}
	if statistics := execution.Statistics; statistics != nil {
		query.BytesScanned = aws.ToInt64(statistics.DataScannedInBytes)
		query.DurationMS = aws.ToInt64(statistics.TotalExecutionTimeInMillis)
		query.CostUSD = estimateAthenaCost(query.BytesScanned)
	}
	if query.State == string(athenatypes.QueryExecutionStateSucceeded) && query.Columns == nil {
		columns, rows, truncated, err := r.results(ctx, query.ID)
		if err != nil {
			return err
		}
		query.Columns, query.Rows, query.Truncated = columns, rows, truncated
	}
	return nil
}

// results は結果を最大 maxResultRows 行まで取得する。それ以上ある場合は truncated = true を返す。
func (r *AthenaRunner) results(ctx context.Context, id string) ([]string, [][]string, bool, error) {
	columns := []string{}
	rows := [][]string{}
	var nextToken *string
	firstPage := true
	for {
		output, err := r.client.GetQueryResults(ctx, &athena.GetQueryResultsInput{QueryExecutionId: aws.String(id), NextToken: nextToken, MaxResults: aws.Int32(maxResultRows)})
		if err != nil {
			return nil, nil, false, fmt.Errorf("Athenaクエリの結果取得に失敗しました: %w", err)
		}
		if output.ResultSet == nil {
			break
		}
		if firstPage && output.ResultSet.ResultSetMetadata != nil {
			for _, column := range output.ResultSet.ResultSetMetadata.ColumnInfo {
				columns = append(columns, aws.ToString(column.Name))
			}
		}
		for index, row := range output.ResultSet.Rows {
			values := make([]string, len(row.Data))
			for dataIndex, datum := range row.Data {
				values[dataIndex] = aws.ToString(datum.VarCharValue)
			}
			// SELECT の結果は先頭行が列名（ヘッダー）になっているため読み飛ばす
			if firstPage && index == 0 && isHeaderRow(values, columns) {
				continue
			}
			if len(rows) >= maxResultRows {
				return columns, rows, true, nil
			}
			rows = append(rows, values)
		}
		firstPage = false
		if aws.ToString(output.NextToken) == "" {
			break
		}
		nextToken = output.NextToken
	}
	return columns, rows, false, nil
}

func isHeaderRow(values, columns []string) bool {
	if len(values) == 0 || len(values) != len(columns) {
		return false
	}
	for index := range values {
		if values[index] != columns[index] {
			return false
		}
	}
	return true
}

// estimateAthenaCost はスキャン量から料金の目安（USD）を計算する。実際の請求額とは端数処理などで差が出ることがある。
func estimateAthenaCost(bytesScanned int64) float64 {
	if bytesScanned <= 0 {
		return 0
	}
	billed := math.Max(float64(bytesScanned), athenaMinBilledByte)
	return billed / math.Pow(1024, 4) * athenaPricePerTB
}

// isReadOnlySQL は先頭のコメントと空白を除いた最初のキーワードが SELECT または WITH であることを確認する。
func isReadOnlySQL(sql string) bool {
	rest := strings.TrimSpace(sql)
	for {
		switch {
		case strings.HasPrefix(rest, "--"):
			newline := strings.IndexByte(rest, '\n')
			if newline < 0 {
				return false
			}
			rest = strings.TrimSpace(rest[newline+1:])
		case strings.HasPrefix(rest, "/*"):
			end := strings.Index(rest, "*/")
			if end < 0 {
				return false
			}
			rest = strings.TrimSpace(rest[end+2:])
		default:
			upper := strings.ToUpper(rest)
			return hasKeyword(upper, "SELECT") || hasKeyword(upper, "WITH")
		}
	}
}

// hasKeyword は text が keyword で始まり、その直後が単語の区切りであることを確認する（SELECTED などを除外）。
func hasKeyword(text, keyword string) bool {
	if !strings.HasPrefix(text, keyword) {
		return false
	}
	if len(text) == len(keyword) {
		return true
	}
	next := text[len(keyword)]
	return !(next >= 'A' && next <= 'Z' || next >= '0' && next <= '9' || next == '_')
}

func isTerminalQueryState(state string) bool {
	switch athenatypes.QueryExecutionState(state) {
	case athenatypes.QueryExecutionStateSucceeded, athenatypes.QueryExecutionStateFailed, athenatypes.QueryExecutionStateCancelled:
		return true
	}
	return false
}
