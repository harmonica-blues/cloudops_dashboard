package main

// 保存済みクエリ。athena.queries_dir の .sql ファイルを読み込み、画面のプルダウンに表示する。
// ファイルはリクエストのたびに読み直すため、追加・編集はコンテナを再起動しなくても反映される。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 1ファイルあたりの最大サイズ（誤って大きなファイルを置いた場合の保護）
const maxSavedQueryBytes = 64 * 1024

type SavedQuery struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SQL         string `json:"sql"`
}

// loadSavedQueries は dir 内の .sql ファイルをファイル名順に読み込む。フォルダがない場合は空の一覧を返す。
func loadSavedQueries(dir string) ([]SavedQuery, error) {
	queries := []SavedQuery{}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return queries, nil
	}
	if err != nil {
		return queries, fmt.Errorf("保存済みクエリのフォルダ %s を読み込めません: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".sql") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxSavedQueryBytes {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		queries = append(queries, parseSavedQuery(entry.Name(), string(content)))
	}
	return queries, nil
}

// parseSavedQuery はファイル先頭の「-- name:」「-- description:」行を名前と説明として取り出し、残りを SQL とする。
func parseSavedQuery(fileName, content string) SavedQuery {
	query := SavedQuery{ID: fileName, Name: strings.TrimSuffix(fileName, filepath.Ext(fileName))}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	body := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if value, ok := metadataValue(trimmed, "name"); ok {
			query.Name = value
			continue
		}
		if value, ok := metadataValue(trimmed, "description"); ok {
			query.Description = value
			continue
		}
		body = append(body, line)
	}
	query.SQL = strings.TrimSpace(strings.Join(body, "\n"))
	return query
}

func metadataValue(line, key string) (string, bool) {
	if !strings.HasPrefix(line, "--") {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, "--"))
	prefix := key + ":"
	if len(rest) < len(prefix) || !strings.EqualFold(rest[:len(prefix)], prefix) {
		return "", false
	}
	value := strings.TrimSpace(rest[len(prefix):])
	return value, value != ""
}
