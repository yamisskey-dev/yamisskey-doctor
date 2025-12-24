# yamisskey-doctor

Misskey インスタンスの診断・修復・バックアップ検証を行う CLI ツール。

## インストール

```bash
go install github.com/yamisskey-dev/yamisskey-doctor@latest
```

## コマンド

```bash
yamisskey-doctor check <url>     # API/Streaming ヘルスチェック
yamisskey-doctor restore         # バックアップから復元
yamisskey-doctor verify          # バックアップ復元検証
yamisskey-doctor repair          # DB 不整合の修復
```

### check

Misskey インスタンスの API と Streaming の状態をチェックします。

```bash
yamisskey-doctor check yami.ski
yamisskey-doctor check --format json yami.ski
yamisskey-doctor check --quiet yami.ski

# 管理者トークンがあれば追加情報を取得
MISSKEY_TOKEN=xxx yamisskey-doctor check yami.ski
```

| オプション | 説明 | デフォルト |
|-----------|------|-----------|
| `-f, --format` | 出力形式 (text/json) | text |
| `-t, --timeout` | タイムアウト秒数 | 5 |
| `-q, --quiet` | 出力なし（終了コードのみ） | false |

**チェック項目:**

| 項目 | 認証 | 説明 |
|------|------|------|
| API | 不要 | `/api/meta` の応答確認 |
| Meta | 不要 | バージョン、インスタンス名、連合状態 |
| Stats | 不要 | ノート数、ユーザー数 |
| Streaming | 不要 | WebSocket 接続確認 |
| Queue | 要 | ジョブキュー状態 |
| Server | 要 | CPU/メモリ使用率 |

**終了コード:** 0=healthy, 1=degraded, 2=unhealthy

### restore

```bash
yamisskey-doctor restore --source r2 --list
yamisskey-doctor restore --source r2 --file backup.7z
```

> 未実装

### verify

```bash
yamisskey-doctor verify --source r2 --latest
```

> 未実装

### repair

```bash
yamisskey-doctor repair --dry-run
yamisskey-doctor repair --type all
```

> 未実装

## 環境変数

```bash
MISSKEY_TOKEN=xxx      # check で追加情報取得
POSTGRES_HOST=localhost
POSTGRES_PORT=5432
POSTGRES_USER=misskey
POSTGRES_PASSWORD=xxx
POSTGRES_DB=misskey
```

## ライセンス

MIT
