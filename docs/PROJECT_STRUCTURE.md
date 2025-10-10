# 项目结构说明

本文档说明项目的目录结构和文件组织。

## 📂 目录树

```
OpenAI-Compatible-API-Proxy-for-Z/
├── README.md                      # 项目主文档
├── CLAUDE.md                      # Claude Code 工作指导
├── PROJECT_STRUCTURE.md           # 本文档
│
├── main.go                        # 程序入口
├── go.mod, go.sum                 # Go模块依赖
│
├── 核心功能模块 (*.go)
│   ├── handlers_optimized.go     # 优化的请求处理器
│   ├── stream_handler.go         # 流式响应处理
│   ├── message_converter.go      # 消息格式转换
│   ├── image_uploader.go         # 图片上传处理
│   ├── response_helper.go        # 响应辅助函数
│   ├── signature.go              # 签名生成
│   ├── model_mapper.go           # 模型映射
│   ├── features.go               # 特性配置
│   └── types_fix.go              # 类型定义
│
├── 测试文件 (*_test.go)
│   ├── api_test.go               # API测试
│   ├── nonstream_test.go         # 非流式测试
│   ├── retry_test.go             # 重试机制测试
│   ├── retry_integration_test.go # 重试集成测试
│   ├── signature_test.go         # 签名测试
│   └── tool_format_test.go       # 工具调用格式测试
│
├── config/                        # 配置模块
│   ├── models.go                 # 模型配置
│   ├── models_test.go            # 模型配置测试
│   └── fingerprints.go           # 浏览器指纹配置
│
├── scripts/                       # 测试和部署脚本
│   ├── README.md                 # 脚本说明文档
│   ├── test_quick.sh             # 快速功能测试 ⭐️
│   ├── test_essential.sh         # 基础功能测试
│   ├── test_comprehensive.sh     # 完整测试套件
│   ├── test_optimized.sh         # 性能测试
│   ├── test_tool_comprehensive.sh # 工具调用综合测试
│   └── test_tool_format.sh       # 工具格式验证
│
└── docs/                          # 项目文档
    ├── README.md                  # 文档目录说明
    ├── TOOL_CALL_FIX.md          # 工具调用修复文档
    ├── OPTIMIZATION_SUMMARY.md    # 性能优化总结
    ├── FINAL_OPTIMIZATION.md      # 最终优化方案
    ├── IMPROVEMENTS.md            # 改进方案
    └── improvement_suggestions.md # 改进建议
```

## 📋 文件分类

### 核心源代码（根目录）
- ✅ **主程序**: `main.go`
- 🔧 **功能模块**: `*_handler.go`, `*_converter.go`, `*_helper.go`
- 🧪 **测试文件**: `*_test.go`
- 📦 **配置模块**: `config/`

**为什么保留在根目录？**
- Go语言单包项目的标准做法
- 编译简单，无需修改import路径
- 便于快速开发和维护

### 测试脚本（scripts/）
所有 `.sh` 测试脚本统一放在此目录：
- ⚡ 功能测试：验证API功能正常
- 🔍 性能测试：测试优化效果
- 🛠️ 工具测试：验证工具调用格式

**快速开始**:
```bash
# 快速验证功能
./scripts/test_quick.sh

# 查看所有脚本说明
cat scripts/README.md
```

### 文档（docs/）
所有技术文档、优化记录统一放在此目录：
- 📘 技术文档：功能实现说明
- ⚡ 优化文档：性能优化记录
- 💡 改进建议：未来优化方向

**查看文档**:
```bash
# 查看文档目录
cat docs/README.md

# 查看工具调用修复说明
cat docs/TOOL_CALL_FIX.md
```

## 🎯 快速导航

### 我想...

**了解项目** → 阅读 [README.md](README.md)

**开始开发** → 阅读 [CLAUDE.md](CLAUDE.md)

**运行测试** → 执行 `./scripts/test_quick.sh`

**查看文档** → 浏览 [docs/](docs/)

**调试问题** → 查看 [CLAUDE.md#调试技巧](CLAUDE.md#调试技巧)

**贡献代码** → 阅读 [docs/IMPROVEMENTS.md](docs/IMPROVEMENTS.md)

## 📊 文件统计

```
Go源文件:     18个 (main.go + 功能模块)
测试文件:      6个 (*_test.go)
测试脚本:      6个 (scripts/*.sh)
文档文件:      6个 (docs/*.md)
配置文件:      3个 (config/*.go)
```

## 🔄 最近更新

### 2025-10-10 - 项目结构整理
- ✅ 创建 `scripts/` 目录，移动所有测试脚本
- ✅ 整理 `docs/` 目录，统一管理文档
- ✅ 创建各目录的 README.md 说明
- ✅ 更新 CLAUDE.md 中的路径引用

**优点**:
- 根目录更清爽
- 文件分类清晰
- 易于查找和维护

## 🔧 维护指南

### 添加新功能模块
```bash
# 在根目录创建 .go 文件
touch new_feature.go
touch new_feature_test.go
```

### 添加新测试脚本
```bash
# 在 scripts/ 目录创建
touch scripts/test_new_feature.sh
chmod +x scripts/test_new_feature.sh
# 更新 scripts/README.md
```

### 添加新文档
```bash
# 在 docs/ 目录创建
touch docs/NEW_FEATURE.md
# 更新 docs/README.md
```

## 📞 问题反馈

如果您对项目结构有建议，请：
- 提交 [GitHub Issue](https://github.com/icy37785/openai-compatible-api-proxy-for-z/issues)
- 发起 [Pull Request](https://github.com/icy37785/openai-compatible-api-proxy-for-z/pulls)

---

**最后更新**: 2025-10-10
**维护者**: Claude Code
