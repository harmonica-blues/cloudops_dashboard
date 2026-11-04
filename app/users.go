package main

// 管理者用のコマンド。画面にユーザー管理はないため、コンテナ内でこのコマンドを実行する。
//   docker compose exec cloudops /app/cloudops user add <ユーザー名>
// パスワードは自動で生成して表示する。--password-stdin を付けると標準入力の1行目をパスワードにする。

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const commandUsage = `使い方:
  cloudops user add <ユーザー名> [--password-stdin]     ユーザーを作成する
  cloudops user passwd <ユーザー名> [--password-stdin]  パスワードを再設定する（ログイン中のセッションは無効になる）
  cloudops user disable <ユーザー名>                     ユーザーを無効にする（ログイン中のセッションは無効になる）
  cloudops user enable <ユーザー名>                      無効にしたユーザーを有効に戻す
  cloudops user list                                     ユーザーの一覧を表示する
  cloudops budget unlock                                 使用額の上限による Athena の停止を解除する`

// isCommand は引数が管理者用のコマンドかどうかを返す（そうでなければ従来どおり設定ファイルのパスとして扱う）。
func isCommand(args []string) bool {
	return len(args) > 0 && (args[0] == "user" || args[0] == "budget")
}

func runCommand(args []string, stdin io.Reader, stdout io.Writer) error {
	cfg, err := loadConfig("")
	if err != nil {
		return err
	}
	store, err := openStore(cfg.Database.Path)
	if err != nil {
		return err
	}
	defer store.Close()
	return runStoreCommand(store, args, stdin, stdout)
}

func runStoreCommand(store *Store, args []string, stdin io.Reader, stdout io.Writer) error {
	passwordFromStdin := false
	var positional []string
	for _, arg := range args {
		if arg == "--password-stdin" {
			passwordFromStdin = true
			continue
		}
		positional = append(positional, arg)
	}
	command := strings.Join(positional[:min(2, len(positional))], " ")
	name := ""
	if len(positional) > 2 {
		name = positional[2]
	}
	now := time.Now()
	logCommand := func(action string, detail map[string]any) {
		if err := store.audit(auditEntry{Username: "cli", Action: action, Detail: detail}, now); err != nil {
			fmt.Fprintf(stdout, "操作ログを保存できませんでした: %v\n", err)
		}
	}
	switch command {
	case "user add", "user passwd":
		if err := validateUsername(name); err != nil {
			return err
		}
		password, generated, err := choosePassword(passwordFromStdin, stdin)
		if err != nil {
			return err
		}
		hash, err := hashPassword(password)
		if err != nil {
			return err
		}
		if command == "user add" {
			if err := store.createUser(name, hash, now); err != nil {
				return err
			}
			logCommand("user_add", map[string]any{"username": name})
			fmt.Fprintf(stdout, "ユーザー %s を作成しました。\n", name)
		} else {
			if err := store.setPassword(name, hash); err != nil {
				return err
			}
			logCommand("user_passwd", map[string]any{"username": name})
			fmt.Fprintf(stdout, "ユーザー %s のパスワードを再設定しました。\n", name)
		}
		if generated {
			fmt.Fprintf(stdout, "パスワード: %s\n（この画面以外では確認できません。本人に安全な方法で伝えてください）\n", password)
		}
	case "user disable", "user enable":
		if err := validateUsername(name); err != nil {
			return err
		}
		disabled := command == "user disable"
		if err := store.setDisabled(name, disabled); err != nil {
			return err
		}
		if disabled {
			logCommand("user_disable", map[string]any{"username": name})
			fmt.Fprintf(stdout, "ユーザー %s を無効にしました。\n", name)
		} else {
			logCommand("user_enable", map[string]any{"username": name})
			fmt.Fprintf(stdout, "ユーザー %s を有効にしました。\n", name)
		}
	case "user list":
		users, err := store.listUsers()
		if err != nil {
			return err
		}
		if len(users) == 0 {
			fmt.Fprintln(stdout, "ユーザーはいません。cloudops user add <ユーザー名> で作成してください。")
		}
		for _, user := range users {
			status := "有効"
			if user.Disabled {
				status = "無効"
			} else if now.Before(user.LockedUntil) {
				status = "ログイン停止中（" + user.LockedUntil.Format(time.RFC3339) + " まで）"
			}
			fmt.Fprintf(stdout, "%s\t%s\t作成: %s\n", user.Username, status, user.CreatedAt.Format("2006-01-02"))
		}
	case "budget unlock":
		lock, err := store.budgetLock()
		if err != nil {
			return err
		}
		if lock == nil {
			fmt.Fprintln(stdout, "Athena は停止していません。")
			return nil
		}
		if _, err := store.deleteBudgetLock(); err != nil {
			return err
		}
		logCommand("budget_unlock", map[string]any{"reason": "cli", "day": lock.Day, "spent_usd": lock.SpentUSD, "budget_usd": lock.BudgetUSD})
		fmt.Fprintf(stdout, "Athena の停止を解除しました（%s に $%.4f / 上限 $%.2f で停止）。今日の使用額が上限以上の場合は、次の確認で再び停止します。\n", lock.Day, lock.SpentUSD, lock.BudgetUSD)
	default:
		return fmt.Errorf("%s", commandUsage)
	}
	return nil
}

// choosePassword は --password-stdin なら標準入力の1行目を使い、そうでなければランダムなパスワードを生成する。
func choosePassword(fromStdin bool, stdin io.Reader) (string, bool, error) {
	if !fromStdin {
		password, err := randomToken(18)
		return password, true, err
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", false, err
	}
	password := strings.TrimRight(line, "\r\n")
	if len([]rune(password)) < minPasswordLength {
		return "", false, fmt.Errorf("パスワードは %d 文字以上にしてください", minPasswordLength)
	}
	return password, false, nil
}

func exitWithCommand(args []string) {
	if err := runCommand(args, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
