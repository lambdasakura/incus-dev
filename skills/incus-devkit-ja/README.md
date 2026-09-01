# incus-devkit スキル

`idev` をAIコーディングエージェントから使うためのAgent Skill。

*[English version](../incus-devkit/README.md)*

## 導入

エージェントが読める場所へ置く。

```bash
# そのユーザーのすべてのプロジェクトで使う
cp -r skills/incus-devkit-ja ~/.claude/skills/

# 特定のプロジェクトだけで使う
cp -r skills/incus-devkit-ja /path/to/project/.claude/skills/
```

## 構成

```text
incus-devkit-ja/
├── SKILL.md                      # 原則・コマンド・作業手順
├── references/
│   ├── dev-yml.md                # dev.yml の全項目
│   └── troubleshooting.md        # エラー文言から引く
└── templates/
    └── dev.yml                   # 注釈つきの雛形
```

内容はリポジトリの `docs/` に基づく。
CLIの挙動を変えた場合は、マニュアルと合わせてこちらも更新すること。
