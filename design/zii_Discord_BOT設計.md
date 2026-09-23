---
type: consultation
project:
status: open
created: "2026-09-23"
updated: "2026-09-23"
tags: []
---

# Untitled

## 相談目的

Ochestratorへ質問者の質問内容や返答内容をディスコード側へ渡すための
統合窓口を設計する

## 背景

Ochestrator設計済み
各DBテーブル設計済み

## 現在の構成

```text
BOT Name:
    Zii

名称由来:
    中国語の「知性」「執行」をベースに命名

システム上の主な構成:
    Zii
    ├─ Discord Bot
    ├─ Orchestrator
    ├─ Local LLM
    └─ Search MCP

Discord
   ↓
Discord Bot
   ├─ Gateway接続
   ├─ Message受付判定
   ├─ Discord情報の正規化
   ├─ OrchestratorへRequest
   ├─ OrchestratorからResponse
   └─ 元MessageへReply
           ↓
      Orchestrator
```

## 問題

-

## 検討事項

## DB-01：Discord接続方式 / Gateway Intents

- 状態：**決定**
- 決めたいこと：
    - Discordへの接続方式
    - 必要なGateway Intents
    - Message Content Intentを使用するか
- 前提・制約：
    - 初回はテキスト質問のみ
    - PresenceやGuild Member一覧は不要
- 決定内容：
    - Discord GatewayへBot Accountで常時接続する。
    - 必要なIntentは最低限以下とする。

```
GUILDS
GUILD_MESSAGES
DIRECT_MESSAGES
```

- 初回は`MESSAGE_CONTENT` Privileged Intentを使用しない。
- Guild内では**Botへの@mentionを含むMessageのみ質問として受付**する。
- DMでは通常Messageを受付する。
- DiscordではDMおよびBotが@mentionされたMessageは、Message Content Intentなしでも内容へアクセスできる。
- `GUILD_MEMBERS`
- `GUILD_PRESENCES`  
    は有効化しない。
- 決定理由：
    - 必要最小限のDiscordデータだけ取得できる。
    - Privileged Intentへの依存を初期段階で避けられる。
    - 将来Bot規模が拡大しても移行しやすい。
- 影響する項目：
    - DB-02
    - Discord Developer Portal設定
- 残課題：
    - 将来「専用Channelではmention不要」とする場合は`MESSAGE_CONTENT`利用を再検討する。
- 確認方法：
    - Guildで@mentionした質問を受信できる。
    - DMを受信できる。
    - 通常のGuild Messageを処理しない。

---

## DB-02：質問Message受付条件

- 状態：**決定**
- 決めたいこと：
    - どのDiscord Messageを質問として扱うか
- 決定内容：

```
受付する:
    Guild:
        Botへの@mentionを含むUser Message

    DM:
        UserからBotへの通常Message

受付しない:
    Bot自身のMessage
    他BotのMessage
    Webhook Message
    System Message
    空Message
    Message Edit Event
    Message Delete Event
```

- 初回は**テキスト本文のみ対応**する。
- 以下はLLM入力へ含めない。

```
attachments
embeds
stickers
polls
voice
```

- 添付ファイルしか存在しないMessageは処理対象外。
- Guild MessageではBot mention部分を本文から除去してからOrchestratorへ渡す。
- 決定理由：
    - 初回実装をテキストチャットに限定できる。
    - Bot同士の無限応答を防げる。
- 影響する項目：
    - DB-04
- 残課題：
    - 添付画像・ファイル対応は将来設計。
- 確認方法：
    - User mention→処理
    - DM→処理
    - Bot Message→無視
    - 添付のみ→無視

---

## DB-03：Discord権限

- 状態：**決定**
- 決めたいこと：
    - Botへ与えるGuild権限
- 決定内容：
    - 初回稼働に必要な最低限：

```
View Channels
Send Messages
Send Messages in Threads
Read Message History
```

- Replyに必要な範囲以外の管理権限を付与しない。
- 以下は不要：

```
Administrator
Manage Messages
Manage Channels
Manage Roles
Manage Guild
Kick Members
Ban Members
```

- 決定理由：
    - Least Privilege。
    - Bot Token流出時の影響範囲を抑える。
- 影響する項目：
    - Discord Bot招待設定
- 残課題：
    - なし
- 確認方法：
    - 質問受信とReplyだけが正常に行えること。

---

## DB-04：Orchestrator Request Schema

- 状態：**決定**
- 決めたいこと：
    - Discord BotからOrchestratorへ何を渡すか
- 決定内容：

```
BotRequest
├─ request_id
├─ discord_message_id
├─ guild_id              nullable
├─ channel_id
├─ thread_id             nullable
├─ user_id
├─ content
├─ message_created_at
└─ received_at
```

- `request_id`
    - Bot側でUUID生成
    - 1 Discord入力につき1つ
- `discord_message_id`
    - Discord Message IDを文字列として保持
- `guild_id`
    - DMではNULL
- `channel_id`
    - Guild Channel / DM Channel
- `thread_id`
    - Threadの場合のみ設定
- `user_id`
    - 質問者
- `content`
    - mention除去・trim済み本文
- message_created_at Discord 
	- Message自体の作成時刻 → messages.created_atへ保存
	- messages.created_at = message_created_at
- `received_at`
    - UTC RFC3339
    - BotがEventを受け取った時刻
	    → Logging / latency計測用
- Bot側でConversation判定は行わない。
- `conversation_key`生成はOrchestratorの責務。
- 決定理由：
    - Discord固有処理とConversation処理を分離できる。
- 影響する項目：
    - OR-02
    - OR-13
- 残課題：
    - なし
- 確認方法：
    - Guild / Thread / DMそれぞれで期待値になること。

---

## DB-05：Orchestrator Response Schema

- 状態：**決定**
- 決めたいこと：
    - Botへ返す情報
- 決定内容：

```
BotResponse
├─ request_id
├─ reply_to_message_id
└─ content
```

- `request_id`
    - Requestとの対応確認
- `reply_to_message_id`
    - 元User Message ID
- `content`
    - LLM最終回答
- Botは回答内容の意味的な加工をしない。
- Discord固有の文字数分割だけBot側で行う。
- 決定理由：
    - OrchestratorとDiscord APIの責務を明確に分離できる。
- 影響する項目：
    - DB-06
- 残課題：
    - なし

---

## DB-06：Discord Reply方式

- 状態：**決定**
- 決めたいこと：
    - 最終回答をどう送信するか
- 決定内容：
    - 必ず元User Messageへの**Reply**として送信する。

```
User Message
    ↓
Bot Reply
```

- Botが独立した新規Messageとして回答しない。
- Discord Messageは通常最大2,000文字なので、回答が超える場合は分割する。
- Bot側の安全上限：

```
1 Message最大:
    1,900文字
```

- 100文字程度の余裕を設ける。
- 分割時は可能な限り：

```
段落
↓
改行
↓
文境界
```

の順で切る。

-  長文Replyの成功条件
	- 分割された全Messageの送信成功をもってReply全体を成功とする
	- 全Message成功時：
	  - 最初のReply Message IDを代表IDとしてOrchestratorへ返す
		  - 分割Reply時のMessage ID管理
			  - 分割された各Discord Messageはそれぞれ固有のMessage IDを持つ。
			  - 初期実装では複数IDを個別保存しない。
			  - 最初のReply Message IDを代表IDとしてOrchestratorへ返す。
			  - この代表IDは、Assistant回答全体を代表するdiscord_message_idとして扱う。
			  - SQLiteのmessages.contentには分割前の最終回答全文を保存する。
			  - したがってAssistantのdiscord_message_idとcontentは、
				    Discord Message単体との厳密な1対1対応ではない。
	- 途中のMessageで送信失敗した場合：
	  - Reply全体を失敗扱いとする
	  - partial_replyとしてError Logを記録する
	  - 未送信分の追加送信はRetry方針に従う

- Bot→Orchestratorの送信結果Schema
	DiscordReplyResult
	├─ request_id
	├─ success
	├─ discord_message_id nullable
	└─ created_at nullable
	success = true
	    → discord_message_id / created_at 必須
	success = false
	    → discord_message_id / created_at はNULL許可
- 分割Replyの場合のは以下
	- discord_message_id
		= 最初のReply Message ID
	- created_at
		= 最初のReply Messageの作成時刻

- 決定理由：
    - ユーザーがどの質問への回答か明確に確認できる。
    - 長文回答にも対応できる。
- 影響する項目：
    - messagesテーブル
    - OR-13
- 残課題：
    - 将来複数Reply IDを厳密管理する場合は別テーブルを検討。
- 確認方法：
    - 1,900文字以下
    - 4,000文字程度
    - Markdown code blockあり  
        を試験。

---

## DB-07：重複Event対策

- 状態：**決定**
- 決めたいこと：
    - 同じDiscord Message Eventを複数回受信した場合の扱い
- 決定内容：
    - Bot Adapter自身では短時間の**in-flight Message ID Set**を保持する。
    - 同一`discord_message_id`が処理中なら再投入しない。
    - 永続的な重複判定はOrchestrator / SQLiteの：

```
messages.discord_message_id UNIQUE
```

に任せる。

- Bot再起動後の重複はSQLite側で排除する。
- 決定理由：
    - Bot MemoryとSQLiteの二段階で重複処理を防げる。
- 影響する項目：
    - OR-14
- 残課題：
    - なし

---

## DB-08：Discord Rate Limit / Retry

- 状態：**決定**
- 決めたいこと：
    - Discord APIが429等を返した場合の処理
- 決定内容：
    - Discord Client LibraryのRate Limit処理を優先利用する。
    - 独自に固定待ち時間をハードコードしない。
    - HTTP 429ではDiscordが返す`retry_after`を必ず尊重する。Discordもこれを推奨している。
    - Reply送信のRetry対象：

```
429
一時的Network Error
HTTP 5xx
```

- 最大Retry：3回
- 429：
    - `retry_after`に従う
- Network / 5xx：
    - 1秒
    - 2秒
    - 4秒  
        の指数Backoff
- 以下はRetryしない：

```
400
401
403
404
```

- Orchestrator側ではDiscord送信Retryを行わない。
- 決定理由：
    - Discord Rate Limitとの二重制御を防げる。
- 影響する項目：
    - OR-13
- 残課題：
    - なし

---

## DB-09：Gateway再接続

- 状態：**決定**
- 決めたいこと：
    - Gateway切断時の復旧
- 決定内容：
    - Discord Client LibraryのGateway reconnect / resume機構を使用する。
    - Application独自WebSocket再実装は行わない。
    - 一時的Network断ではSession Resumeを優先。
    - Resume不能時は新Session接続。
    - 再接続中は新規Message処理を行わない。
    - reconnect完了後に通常受付へ復帰する。
- 決定理由：
    - Discord Gateway Protocolの複雑な再接続制御をLibraryへ任せられる。
- 影響する項目：
    - DB-11
- 残課題：
    - 採用Discord Library確定後にAPIを確認。
- 確認方法：
    - Network切断→復旧
    - Discord Gateway再接続  
        を試験。

---

## DB-10：ユーザー向けError Reply

- 状態：**決定**
- 決めたいこと：
    - Orchestrator失敗時にDiscordへ何を返すか
- 決定内容：
    - Orchestratorが正常回答を返せなかった場合でも、可能なら元Messageへ簡潔なError Replyを返す。
    - Error分類はBot側で細かく説明しない。

例：

```
処理中にエラーが発生しました。
時間をおいてもう一度試してください。
```

- 以下は絶対にDiscordへ出さない：

```
Stack Trace
SQLite Error全文
内部Path
Endpoint詳細
Bot Token
Secret
LLM Raw Error
MCP Raw Error
```

- 詳細はLoggingへ記録する。
- 決定理由：
    - 内部情報漏洩を防ぎながら、無応答を避けられる。
- 影響する項目：
    - OR-12
    - DB-11
- 残課題：
    - なし

---

## DB-11：設定値 / Secrets

- 状態：**決定**
- 決めたいこと：
    - Discord固有設定の管理
- 決定内容：

通常設定：

```
DISCORD_REPLY_MAX_CHARS=1900
DISCORD_SEND_MAX_RETRIES=3
LOG_LEVEL=info
```

Secret：

```
DISCORD_BOT_TOKEN
```

- Bot Tokenはコード・Git・通常Logへ絶対に含めない。
- `.env`を使用する場合も`.gitignore`対象。
- 将来的にsystemd Credential等へ移行可能な構造にする。
- 決定理由：
    - Secretと通常設定を分離できる。
- 影響する項目：
    - deployment
- 残課題：
    - Botの実行方式確定時にSecret注入方法を決定。

---

## DB-12：Logging

- 状態：**決定**
- 決めたいこと：
    - Discord Adapter側で何を記録するか
- 決定内容：
    - Orchestratorと同じくJSON Lines。
    - stdout / stderrへ出力。
    - 主要Field：

```
timestamp
level
component=discord_bot
event
request_id
discord_message_id
guild_id
channel_id
user_id
duration_ms
status
error_code
```

- 原則記録しない：

```
Message本文
Reply本文
Bot Token
Secret
Attachment内容
```

- `request_id`はOrchestratorへそのまま引き継ぐ。
- 決定理由：
    - Discord受信→LLM→MCP→Discord Replyまで同じ`request_id`で追跡できる。
- 影響する項目：
    - OR-16
- 残課題：
    - Metricsは初回対象外。
- 確認方法：
    - 1質問をBot/Orchestrator双方のLogで追跡できること。

---

## DB-13：Graceful Shutdown

- 状態：**決定**
- 決めたいこと：
    - Bot停止時の処理
- 決定内容：

```
SIGTERM / SIGINT
    ↓
新規Discord Message受付停止
    ↓
Orchestratorへの新規Request投入停止
    ↓
処理中Request完了待ち
    ↓
Discord Gateway切断
    ↓
終了
```

- Orchestrator側Grace Periodと合わせて**60秒**。
- Shutdown開始後の新規Gateway Eventは処理しない。
- 処理中RequestはOrchestratorのShutdown方針に従う。
- 決定理由：
    - Botだけ先に切断して処理中回答を失うことを防げる。
- 影響する項目：
    - OR-17
- 残課題：
    - なし
- 確認方法：
    - 回答生成中にSIGTERMを送り、重複Replyや異常終了がないこと。

## DB-14：User Input Validation

- 状態：**決定**

- 決めたいこと：
	- 最大Message長を設定
	- 無効UTF-8拒否
	- NULL文字等の制御文字を除去
	- 不要なzero-width文字を正規化または検出
	- Mention除去後に空なら処理しない
	- User単位Rate Limitを設定
- 決定内容：
　　- 最大入力:
	    4,000文字
	- Rate Limit:
	    1 userあたり 5 Request / 60秒
	    burst 2
	- UTF-8:
	    invalidなら拒否
	- 制御文字:
	    NUL等を拒否/除去
	    改行・TABは許可
	- zero-width:
	    一律削除しない
	    異常に大量の場合のみ拒否/警告

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