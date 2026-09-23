---
type: consultation
project:
status: open
created: "2026-09-23"
updated: "2026-09-23"
tags: []
---

# messages テーブル設計

## 相談目的

messagesを管理するSQLiteのテーブル設計を行う

## 背景

現在の状況。

## 現在の構成

```text
messages
├─ id                  TEXT PRIMARY KEY    UUID
├─ conversation_id     TEXT NOT NULL
│   └─ FK → conversations.id
│      ON DELETE CASCADE
├─ discord_message_id  TEXT NOT NULL UNIQUE
├─ role                TEXT NOT NULL
│   └─ CHECK ('user', 'assistant')
├─ content             TEXT NOT NULL
└─ created_at          TEXT NOT NULL       RFC3339 UTC

INDEX
(conversation_id, created_at)

```

## 問題

-

## 検討事項

## messages テーブル

### MSG-01：Primary Key

- 状態：決定
- 決めたいこと：Message内部IDの型・生成方式
- 決定事項
	- Message内部IDはconversation_idと同じ型、生成方法を採用

### MSG-02：Conversationとの関連

- 状態：決定
- 決めたいこと：
    - `conversation_id`
    - Foreign Key
    - Conversation削除時の扱い
- 決定事項
	- conversation_id
		- NOT NULL
		- conversations.idと同じ型
	- Foreign Key
		- messages.conversation_id → conversations.id
	- ON DELETE
		- CASCADE
	- SQLite
		- foreign_keys = ON
	- Conversation期限切れ
		- 削除しない
		- 新しいconversation_idを作成する
	- 実際にConversationをDELETEした場合のみ
		- 所属Messageも自動削除
### MSG-03：Discord Message ID

- 状態：決定
- 決めたいこと：
	- `discord_message_id`の型
	- `NULL`を許可するか
	- `UNIQUE`制約を付けるか
	- Discordイベントの重複受信をどう扱うか
- 決定事項
	- `discord_message_id`の型：TEXT
	- `NULL`を許可するか：不可
	- `UNIQUE`制約を付けるか：付ける
	- Discordから取得したMessage IDを文字列のまま保存する
	- messages.idとは分離する
	- User Message / Bot Replyの両方を保存する
		- Assistant Messageの場合
		  - 通常Reply：
		    - 実際のDiscord Reply Message IDを保存する。
		  - 分割Reply：
		    - 最初のReply Message IDを代表IDとして保存する。
		    - contentには分割前のAssistant最終回答全文を保存する。
		  - 初期実装では2件目以降のReply Message IDはSQLiteへ保存しない。
	- UNIQUE制約により同一Discord Messageの二重登録を防ぐ
### MSG-04：Role

- 状態：決定
- 決めたいこと：
    - `user`
    - `assistant`
    - 必要なら`tool`
        のどこまで保存するか
- 決定事項
	- Column:
	  role
	- 型:
	  TEXT
	- NOT NULL:
	  Yes
	- 許可値:
	  user
	  assistant
	- user:
	  Discord質問者のMessageを保存
	- assistant:
	  BotがDiscordへ返した最終回答を保存
	- tool:
	  messagesには保存しない
	  Orchestratorの1 Request内だけで使用する
	- system:
	  messagesには保存しない
	  Orchestrator設定から毎回付与する
	- 制約:
	  CHECK (role IN ('user', 'assistant'))
	- Conversationの1ターン:
	  user + assistant の1往復
### MSG-05：Message Content

- 状態：決定
- 決めたいこと：
    - `content`
    - NULL許可
    - 空文字許可
- 決定事項
	- 型：TEXT
	- NULL：不可
	- 空文字：空文字禁止
	- UNIQUE：付けない
### MSG-06：Token数

- 状態：決定
- 決めたいこと：
    - `token_count`を保存するか
    - 8,192 tokens制御に利用するか
- 決定事項
	- token_count:
		- SQLiteには保存しない
		- Token計算:
			- OrchestratorのContext Builderで実行
			- 使用中LLMと互換性のあるTokenizerを使用
		- Conversation履歴上限:
			- 最大5ターン
			- 最大8,192 tokens
			- どちらか先に到達した方を上限とする
		- 履歴選択:
			- 最新ターンから遡って追加する
			- 8,192 tokensを超える古いターンを除外する
		- ターンの扱い:
			- user + assistantを1単位とする
			- ターン途中で分割しない
		- 8,192 tokensの対象:
			- 過去Conversation履歴のみ
			- System Prompt / 今回のUser Message / Tool Result / 最終回答枠は含めない
### MSG-07：Timestamp

- 状態：決定
- 決めたいこと：
    - `created_at`
    - 保存形式
- 決定事項
	- 型：TEXT
	- 保存形式：UTC固定 RFC 3339(例：2026-09-23T04:30:00Z)
	- Discord Message自体の作成時刻

### MSG-08：履歴取得用INDEX

- 状態：決定
- 決めたいこと：
    - `conversation_id + created_at`
        の複合Index
- 決定事項
	- 採用する
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