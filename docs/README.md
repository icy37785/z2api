# 项目文档

本目录包含项目的所有技术文档、优化记录和改进建议。

## 📚 文档列表

### 核心技术文档

#### 🔧 TOOL_CALL_FIX.md
**工具调用兼容性修复文档**

**内容**:
- 问题描述：为什么工具调用在Claude Code中不工作
- 根本原因分析
- OpenAI API标准格式说明
- 修复方案详解
- 验证测试方法
- 兼容性影响评估

**适合阅读对象**: 开发者、贡献者

---

### 性能优化文档

#### ⚡ OPTIMIZATION_SUMMARY.md
**性能优化总结**

**内容**:
- 优化目标和策略
- 关键性能改进点
- 优化前后对比
- 实施细节

**适合阅读对象**: 关注性能的开发者

---

#### 🚀 FINAL_OPTIMIZATION.md
**最终优化方案**

**内容**:
- 最终版本的优化实现
- 详细的性能指标
- 优化效果验证

**适合阅读对象**: 性能工程师、架构师

---

### 改进建议

#### 💡 IMPROVEMENTS.md
**项目改进方案**

**内容**:
- 已实现的改进
- 待实现的功能
- 技术债务清单
- 长期规划

**适合阅读对象**: 维护者、规划者

---

#### 📋 improvement_suggestions.md
**改进建议清单**

**内容**:
- 社区建议
- 用户反馈
- 潜在优化点
- 功能请求

**适合阅读对象**: 产品经理、开发者

---

## 📖 文档阅读顺序

### 新手开发者
1. 先阅读 [主README](../README.md) - 了解项目基本信息
2. 然后阅读 [CLAUDE.md](../CLAUDE.md) - 学习开发指南
3. 参考 [IMPROVEMENTS.md](IMPROVEMENTS.md) - 了解项目架构

### 调试工具调用问题
1. 阅读 [TOOL_CALL_FIX.md](TOOL_CALL_FIX.md)
2. 运行 [工具测试脚本](../scripts/test_tool_comprehensive.sh)

### 性能优化
1. 阅读 [OPTIMIZATION_SUMMARY.md](OPTIMIZATION_SUMMARY.md)
2. 查看 [FINAL_OPTIMIZATION.md](FINAL_OPTIMIZATION.md)
3. 运行 [性能测试](../scripts/test_optimized.sh)

### 贡献代码
1. 查看 [improvement_suggestions.md](improvement_suggestions.md)
2. 选择感兴趣的任务
3. 参考 [IMPROVEMENTS.md](IMPROVEMENTS.md) 了解技术背景

---

## 🔄 文档维护

### 更新频率
- **技术文档**: 重大功能更新时
- **优化文档**: 性能优化完成后
- **改进建议**: 持续更新

### 文档规范
- 使用中文编写
- 包含清晰的标题结构
- 提供代码示例
- 包含相关链接

---

## 🤝 贡献文档

添加新文档时：
1. 使用清晰的文件名（英文，描述性）
2. 在本README中添加说明
3. 遵循Markdown格式规范
4. 包含必要的代码示例
5. 提供相关链接

---

## 📂 相关目录

- [测试脚本](../scripts/) - 所有测试和验证脚本
- [配置文件](../config/) - 模型配置和指纹配置
- [根目录](../) - Go源代码

---

## 📞 联系方式

如有文档相关问题或建议，请：
- 提交 [GitHub Issue](https://github.com/icy37785/openai-compatible-api-proxy-for-z/issues)
- 参与 [Pull Request](https://github.com/icy37785/openai-compatible-api-proxy-for-z/pulls)
