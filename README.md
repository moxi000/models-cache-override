# 模型目录覆写

CLIProxyAPI 插件。它在 Codex 客户端拿到 `models_cache` 之前，把你的修改叠到 CLIProxyAPI 当前要下发的模型目录上。

没改过的字段会跟随上游更新。改过的字段保持你的值，直到在管理页里恢复。只能覆写上游目录里已经存在的模型，不能追加上游没有的模型。

共用覆写按模型拆开放在 `overrides/<模型 slug>.json`，一个文件只描述一个模型。例如：

```text
https://raw.githubusercontent.com/moxi000/models-cache-override/main/overrides/deepseek-v4.1-flash.json
```

管理页先选中模型，再点「拉取此模型的 GitHub 配置」。拉取后只比较这一份，列出新增项和冲突项。冲突默认不勾选，确认后才写入本机。要改某个模型的共用参数，只对该文件提 Pull Request。

管理页入口：`/v0/resource/plugins/models-cache-override/status`，或在管理面板的插件菜单里打开「模型目录」。

## 商店源

本插件收录在独立商店仓库 [cpa-plugin-store](https://github.com/moxi000/cpa-plugin-store)。在 `config.yaml` 里追加：

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/moxi000/cpa-plugin-store/main/registry.json"
```

保存后，在管理面板的插件商店里选择这个源，即可找到并安装 **模型目录覆写**（插件 ID：`models-cache-override`）。以后新增的插件也会放进同一个商店源。

安装使用本仓库的 GitHub Release。发布包名称为：

```text
models-cache-override_<version>_<goos>_<goarch>.zip
checksums.txt
```

压缩包根目录直接包含 `models-cache-override.so`（或对应平台的动态库），不要再套一层目录。

## 本地配置

安装后会写入：

```yaml
plugins:
  configs:
    models-cache-override:
      enabled: true
      priority: 10
      match-base: true
```

`match-base: true` 时，`openai/gpt-5.5` 也会匹配覆写里的 `gpt-5.5`。

## 开发

需要启用 CGO 的 Go 环境。

```bash
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go build -buildmode=c-shared -o models-cache-override.so .
```

把 `models-cache-override.so` 放到 CLIProxyAPI 的 `plugins/linux/amd64/`（或当前平台对应目录）后重启服务。
