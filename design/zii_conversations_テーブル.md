---
type: consultation
project:
status: open
created: "2026-09-23"
updated: "2026-09-23"
tags: []
---

# conversations テーブル設計

## 相談目的

conversationsで使用するSQLiteのテーブル設計を行う

## 背景

現在の状況。

## 現在の構成

```text
conversations
├─ id              TEXT PRIMARY KEY       UUID
├─ guild_id         TEXT NULL
├─ scope_id         TEXT NOT NULL
├─ user_id          TEXT NOT NULL
├─ created_at       TEXT NOT NULL          RFC3339 UTC
└─ last_active_at   TEXT NOT NULL          RFC3339 UTC

INDEX
(guild_id, scope_id, user_id, last_active_at)

```

## 問題

-

## 検討事項

## conversations テーブル

### CV-01：Primary Key

- 状態：決定
- 決めたいこと：`conversation_id`の型・生成方式
	- 型：TEXT
	- 生成方法：UUID
### CV-02：Conversation Key構成

- 状態：決定済み
- 決めたいこと：DB上での保持方法
- 対象：
    - `guild_id` nullable
    - `scope_id`
    - `user_id`

### CV-03：Conversation有効期間管理

- 状態：決定
- 決めたいこと：
    - `created_at`
    - `last_active_at`
    - 7日経過判定方法
- 決定事項
    - `created_at`：Conversation作成時のみ設定、以降変更しない
    - `last_active_at`：Conversation作成時に設定、そのConversationでUser Messageを受け付けるたび更新
    - 7日経過判定方法
	    - now >= last_active_at + 7日

### CV-04：Active Conversation検索条件

- 状態：決定
- 決めたいこと：
    - `guild_id + scope_id + user_id`
    - `last_active_at`
        を使った既存Conversation検索条件
- 決定事項
    - `guild_id + scope_id + user_id`
    - `last_active_at`
        ・`guild_id + scope_id + user_id`が存在するかつ
	        last_active_at > now - 7日場合にActive Conversationと判断
	- 複数候補がある場合は以下で取得
		- ORDER BY last_active_at DESC LIMIT 1
	- DMの場合はguild_id = NULLを許容

### CV-05：UNIQUE / INDEX

- 状態：決定
- 決めたいこと：
    - Conversation検索用Index
    - UNIQUE制約が必要か
- 決定事項
    - Conversation検索用Index
	    - (guild_id, scope_id, user_id, last_active_at)
    - UNIQUE制約が必要か
	    - id：PRIMARY KEYとして一意
	    - Conversation Key：UNIQUEにしない

## リスク

-

## 未解決事項

- [ ]

## 次のアクション

- [ ]

## ADR候補

- [ ]

## 関連

-