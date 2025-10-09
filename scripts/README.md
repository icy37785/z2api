# 测试脚本说明

本目录包含项目的所有测试和验证脚本。

## 📋 脚本列表

### 功能测试脚本

#### 🚀 test_quick.sh
**快速功能验证脚本（最常用）**

- **用途**: 快速测试所有核心功能
- **耗时**: ~30秒
- **测试内容**:
  - 健康检查
  - 基础对话（非流式）
  - 流式响应
  - 工具调用
  - 多轮对话
  - 不同模型切换
  - 自定义参数
  - 错误处理
  - 并发请求

**使用方法**:
```bash
./scripts/test_quick.sh
```

---

#### 📦 test_essential.sh
**基础功能测试**

- **用途**: 测试最核心的必备功能
- **耗时**: ~15秒
- **测试内容**:
  - 健康检查
  - 基础对话
  - 流式响应
  - 基本错误处理

**使用方法**:
```bash
./scripts/test_essential.sh
```

---

#### 🔍 test_comprehensive.sh
**完整测试套件**

- **用途**: 全面测试所有功能和边界情况
- **耗时**: ~2分钟
- **测试内容**:
  - 所有功能完整测试
  - 边界条件验证
  - 错误恢复机制
  - 性能指标
  - 并发压力测试

**使用方法**:
```bash
./scripts/test_comprehensive.sh
```

---

#### ⚡ test_optimized.sh
**性能优化测试**

- **用途**: 验证性能优化效果
- **耗时**: ~1分钟
- **测试内容**:
  - 优化处理器对比
  - 响应时间测量
  - 内存使用监控
  - 并发性能

**使用方法**:
```bash
./scripts/test_optimized.sh
```

---

### 工具调用测试脚本

#### 🔧 test_tool_format.sh
**工具调用格式验证**

- **用途**: 验证工具调用返回格式是否符合OpenAI标准
- **耗时**: ~10秒
- **测试内容**:
  - 工具调用基础测试
  - JSON格式验证
  - 必需字段检查

**使用方法**:
```bash
./scripts/test_tool_format.sh
```

---

#### 🛠️ test_tool_comprehensive.sh
**工具调用综合测试**

- **用途**: 全面测试工具调用功能
- **耗时**: ~30秒
- **测试内容**:
  - 基础工具调用
  - tool_choice 强制调用
  - 流式工具调用
  - 格式标准验证
  - 服务器日志分析

**使用方法**:
```bash
./scripts/test_tool_comprehensive.sh
```

**注意**:
- 如果返回 `null`，这是正常的（模型选择不调用工具）
- 关键是验证当工具被调用时，格式是否符合OpenAI标准

---

## 🎯 推荐使用场景

### 日常开发
```bash
# 快速验证功能是否正常
./scripts/test_quick.sh
```

### 提交代码前
```bash
# 运行完整测试确保没有破坏功能
./scripts/test_comprehensive.sh
```

### 性能优化后
```bash
# 验证优化效果
./scripts/test_optimized.sh
```

### 调试工具调用问题
```bash
# 详细检查工具调用
./scripts/test_tool_comprehensive.sh
```

---

## 📝 环境要求

所有脚本需要：
- ✅ 已编译的 `z2api` 二进制文件（在项目根目录）
- ✅ `curl` 命令
- ✅ `jq` 命令（用于JSON处理）
- ✅ 端口8080-8085可用

---

## 🔧 故障排查

### 脚本无法执行
```bash
# 添加执行权限
chmod +x scripts/*.sh
```

### 端口被占用
```bash
# 杀掉占用端口的进程
killall z2api
```

### jq 命令未安装
```bash
# macOS
brew install jq

# Ubuntu/Debian
sudo apt-get install jq
```

---

## 📊 测试结果解读

- ✅ **绿色 ✓**: 测试通过
- ❌ **红色 ✗**: 测试失败
- ⚠️ **黄色 ⚠**: 警告（功能可能正常，但有注意事项）

---

## 🤝 贡献

添加新测试脚本时：
1. 遵循命名规范：`test_<功能>_<类型>.sh`
2. 添加清晰的注释和输出
3. 更新本 README 文档
4. 确保脚本可独立运行

---

## 📚 相关文档

- [项目主文档](../README.md)
- [工具调用修复说明](../docs/TOOL_CALL_FIX.md)
- [性能优化总结](../docs/OPTIMIZATION_SUMMARY.md)
