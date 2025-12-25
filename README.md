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

R2/Linode Object Storage からバックアップをダウンロードしてデータベースに復元します。

```bash
# バックアップ一覧を表示
yamisskey-doctor restore --list

# 最新のバックアップを復元
yamisskey-doctor restore --latest

# 特定のバックアップを復元
yamisskey-doctor restore --file mk1_2025-01-01_03-00.sql.7z

# Linode ストレージから復元
yamisskey-doctor restore --storage linode --latest

# dry-run（実際には実行しない）
yamisskey-doctor restore --latest --dry-run
```

| オプション | 説明 | デフォルト |
|-----------|------|-----------|
| `-l, --list` | バックアップ一覧を表示 | - |
| `--latest` | 最新のバックアップを使用 | - |
| `-s, --storage` | ストレージタイプ (r2/linode) | r2 |
| `-f, --file` | 復元するバックアップファイル | - |
| `-d, --database` | 復元先データベース名 | POSTGRES_DB |
| `--dry-run` | 実行内容の表示のみ | false |
| `--force` | 確認プロンプトをスキップ | false |

**必要なツール:** rclone, 7z, psql

### verify

バックアップを一時データベースに復元して整合性を検証します。

```bash
# 最新のバックアップを検証
yamisskey-doctor verify --latest

# 特定のバックアップを検証
yamisskey-doctor verify --file mk1_2025-01-01_03-00.sql.7z

# ローカルの SQL ファイルを検証（rclone/7z 不要）
yamisskey-doctor verify --local /path/to/backup.sql

# JSON 形式で出力
yamisskey-doctor verify --latest --format json
```

| オプション | 説明 | デフォルト |
|-----------|------|-----------|
| `-l, --list` | バックアップ一覧を表示 | - |
| `--latest` | 最新のバックアップを使用 | - |
| `-s, --storage` | ストレージタイプ (r2/linode) | r2 |
| `-f, --file` | 検証するバックアップファイル | - |
| `--local` | ローカル SQL ファイルを検証 | - |
| `--format` | 出力形式 (text/json) | text |

**検証項目:**
- テーブル数
- 重要テーブルの存在確認 (user, note, meta, instance)
- ユーザー数・ノート数
- orphan レコードの検出

**必要なツール:** rclone, 7z, psql（`--local` の場合は psql のみ）

### repair

データベースの不整合を検出・修復します。

```bash
# dry-run（変更せずに問題を検出）
yamisskey-doctor repair --dry-run

# 実行（確認プロンプトあり）
yamisskey-doctor repair

# 確認なしで実行
yamisskey-doctor repair --force

# orphan レコードの修復のみ
yamisskey-doctor repair --orphans

# インデックス再構築のみ
yamisskey-doctor repair --reindex

# VACUUM ANALYZE のみ
yamisskey-doctor repair --vacuum
```

| オプション | 説明 | デフォルト |
|-----------|------|-----------|
| `--dry-run` | 変更せずに問題を検出 | false |
| `--force` | 確認プロンプトをスキップ | false |
| `-d, --database` | 対象データベース名 | POSTGRES_DB |
| `--format` | 出力形式 (text/json) | text |
| `--orphans` | orphan レコードの修復のみ | false |
| `--reindex` | インデックス再構築のみ | false |
| `--vacuum` | VACUUM ANALYZE のみ | false |

**修復項目:**
- orphan notes（存在しないユーザーのノート）
- orphan reactions（存在しないノートへのリアクション）
- orphan notifications（存在しないユーザーへの通知）
- orphan drive_files（存在しないユーザーのファイル）
- REINDEX DATABASE
- VACUUM ANALYZE

**必要なツール:** psql

## 環境変数

```bash
# check コマンド
MISSKEY_TOKEN=xxx           # 管理者トークン（追加情報取得用）

# restore/verify/repair コマンド
STORAGE_TYPE=r2             # ストレージタイプ (r2/linode)
R2_PREFIX=backups           # R2 バケットプレフィックス
LINODE_BUCKET=yamisskey-backup  # Linode バケット名
LINODE_PREFIX=backups       # Linode プレフィックス

POSTGRES_HOST=localhost     # PostgreSQL ホスト
POSTGRES_PORT=5432          # PostgreSQL ポート
POSTGRES_USER=misskey       # PostgreSQL ユーザー
POSTGRES_DB=mk1             # PostgreSQL データベース名
PGPASSWORD=xxx              # PostgreSQL パスワード

WORK_DIR=/tmp/yamisskey-restore  # 一時ファイル用ディレクトリ
```

## Docker

Docker コンテナとして実行することもできます。

```bash
# ビルド
docker build -t yamisskey-doctor .

# check コマンド
docker run --rm yamisskey-doctor check yami.ski

# cron モード（定期的に verify を実行）
docker run -d \
  -e MODE=cron \
  -e POSTGRES_HOST=postgres \
  -e PGPASSWORD=xxx \
  -v ./rclone.conf:/root/.config/rclone/rclone.conf:ro \
  yamisskey-doctor
```

docker-compose.example.yml を参考にしてください。

## ライセンス

MIT
