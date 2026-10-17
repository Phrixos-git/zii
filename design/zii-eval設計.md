---
type: consultation
project:
status: open
created: "2026-10-17"
updated: "2026-10-17"
tags: []
---

# zii-eval設計

## 相談目的

複数モデルを同じテストケースで評価・比較するため、
評価CLI `zii-eval`の設定、実行、結果判定、レポート出力と比較方法を設計する。

## 背景

当初はQwenのみを想定していたが、他のモデルでも比較できるようにモデルプロファイルを追加した。
複数モデルを簡単に比較するため、評価CLIを追加した。

## 現在の構成

```text
zii-eval
├─ check
│   └─ 評価用Profile / テストケースの検証
├─ run
│   ├─ LLM Client / Tool Calling Loop / System Prompt
│   ├─ fixture / Search MCP
│   └─ 評価結果の保存
└─ compare
    ├─ 2つのreport.jsonの比較
    └─ 比較結果の保存
```

## 問題

-

## 検討事項

## EV-01：役割 / コマンド

- 状態：**決定**
- 決めたいこと：
    - 複数モデルを同じテストケースで評価・比較する方法
- 決定内容：
    - `cmd/zii-eval`を評価用CLIとし、`internal/eval`から本番と同じLLM Client、Tool Calling Loop、`BuildSystemPrompt`を使用する。
    - コマンドは以下とする。

```text
check   Profileとテストケースを検証する。通信は行わない。
run     指定したProfile・テストケースで評価し、結果を保存する。
compare 2つのreport.jsonを比較し、結果を保存する。
```

- 評価はDiscordや本番の会話DBを使用しない。Discordの受付・送信・保存、日時情報の生成を含むService全体のE2E試験とは区別する。機械的なPASS判定だけでは、回答全体の事実性を評価できない。
- 決定理由：
    - 複数モデルを使用する際に簡単に比較できるよう、モデルプロファイルと併せて評価CLIを追加した。
- 影響する項目：
    - EV-02
    - EV-03
    - EV-04
    - EV-05
    - EV-06
    - Orchestrator設計 OR-04 / OR-07
- 残課題：
    - なし
- 確認方法：
    - `check`、`run`、`compare`がそれぞれの役割を実行する。

---

## EV-02：評価用Profile / テストケース

- 状態：**決定**
- 決めたいこと：
    - 評価対象のモデルとテスト条件の指定方法
- 決定内容：
    - 評価用Profileは`eval/profiles`、テストケースは`eval/testcases`で管理する。Profileとケースは未知の項目・不正値を拒否する。
    - 評価用Profileから`model_profile_config`と`model_profile_id`を指定してOrchestrator設計 OR-19の設定を参照できる。両方を指定し、`model`や`base_url`との矛盾はエラーとする。評価用ProfileにModel名と接続先を直接指定する方式も持つ。
- 影響する項目：
    - EV-01
    - EV-03
    - Orchestrator設計 OR-19
- 残課題：
    - なし
- 確認方法：
    - `check`が通信せずに設定・ケースの不正を検出する。
    - 本番のモデルプロファイルを参照する場合に、Model名や接続先の矛盾を拒否する。

---

## EV-03：Tool Mode / Token計算 / 実行単位

- 状態：**決定**
- 決めたいこと：
    - 外部接続と実行条件の管理方法
- 決定内容：
    - `tool_mode=fixture`では保存済みのTool応答を返し、`live`では実際のSearch MCPを使用する。fixtureでもLLMへの通信は実行する。
    - Token計算は`server`または`estimate`を選択する。`estimate`は評価専用の概算であり、本番のToken計算への自動切替ではない。
    - ケースごとに新しいTool Loopとfixtureの状態を用意し、逐次実行する。繰り返し回数・ウォームアップ回数・ケースID・カテゴリを指定できる。ウォームアップは集計から除外する。
- 影響する項目：
    - EV-02
    - EV-04
    - EV-05
    - Orchestrator設計 OR-07 / OR-09
- 残課題：
    - なし
- 確認方法：
    - fixture / liveの違いとToken計算方式を確認する。
    - ウォームアップを集計から除外する。

---

## EV-04：結果判定 / 終了コード

- 状態：**決定**
- 決めたいこと：
    - 評価結果の分類とCLIの終了条件
- 決定内容：
    - テストケースで回答本文、必要・禁止Tool、Tool回数、応答時間などを機械的に検証する。結果は以下に分類する。

```text
PASS  期待条件を満たし、途中の異常もない。
WARN  最終回答は期待条件を満たすが、途中で異常を検出した。
FAIL  最終回答の生成または期待条件の検証に失敗した。
SKIP  選択したTool Modeの対象外。
```

- 既定ではFAILまたはWARNがあれば終了コード1とする。`--fail-on-warn=false`でWARNを終了コード1の対象から外せる。
- 影響する項目：
    - EV-03
    - EV-05
    - EV-06
- 残課題：
    - なし
- 確認方法：
    - PASS / WARN / FAIL / SKIPの分類を確認する。
    - WARNの有無と`--fail-on-warn`の指定に応じた終了コードを確認する。

---

## EV-05：レポート出力 / 保存対象

- 状態：**決定**
- 決めたいこと：
    - 評価結果と比較条件、回答本文の保存方法
- 決定内容：
    - `run`は新規の出力ディレクトリに`samples.jsonl`、`report.json`、`results.csv`、`summary.md`を保存する。実行条件、Source情報、Suite・Tool Schema・System Promptのハッシュも記録する。
    - 回答本文は`--save-answers`指定時のみ保存し、`reasoning_content`は保存しない。API Keyは環境変数から取得し、結果へ保存しない。
- 影響する項目：
    - EV-02
    - EV-04
    - EV-06
- 残課題：
    - なし
- 確認方法：
    - 新規の出力ディレクトリに評価結果を保存し、既存ディレクトリへの出力を拒否する。
    - `--save-answers`指定時だけ回答本文を保存する。
    - 推論本文とAPI Keyを結果へ保存しない。

---

## EV-06：Report比較

- 状態：**決定**
- 決めたいこと：
    - 比較可能な実行条件と比較結果の出力方法
- 決定内容：
    - `compare`は完了した実行だけを対象とし、Suite、Tool Mode、回数上限、Timeout、Token計算方式、Tool Schema、繰り返し回数、抽出条件などが一致することを確認する。比較結果は`comparison.json`、`comparison.csv`、`comparison.md`へ保存する。
- 影響する項目：
    - EV-02
    - EV-03
    - EV-04
    - EV-05
- 残課題：
    - なし
- 確認方法：
    - 条件が異なるReportの比較を拒否する。
    - 比較可能なReportから差分を出力する。

---

## リスク

-

## 未解決事項

- [ ]

## 次のアクション

- [ ]

## ADR候補

- [ ]

## 関連

- [Orchestrator設計](zii_Discord_Bot_Orchestrator設計.md)
- [Discord Bot設計](zii_Discord_BOT設計.md)
