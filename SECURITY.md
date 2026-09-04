# 安全策略

## 支持范围

SeisForge Studio 目前仅为最新公开版本提供安全修复。旧版本可能仍可下载用于结果复现，但不会持续获得安全更新。

## 私下报告漏洞

请不要在公开 Issue、Discussion、Pull Request、日志或截图中披露漏洞细节、真实地震数据、许可证、机器码或个人信息。

优先使用仓库 **Security** 页面中的 **Report a vulnerability**（Private vulnerability reporting）提交报告：

<https://github.com/DavidLiu-code/SeisForge-Studio/security/advisories/new>

如果该入口暂不可用，请通过 GitHub 提供的私下联系方式联系仓库所有者；公开渠道中只说明“需要私下报告安全问题”，不要附带复现步骤或敏感材料。

报告建议包含：

- 受影响版本与 Windows 版本；
- 问题类型、影响和触发条件；
- 最小化复现步骤；
- 已脱敏的日志、崩溃信息或合成测试文件；
- 你认为可行的缓解措施。

维护者会尽力在 3 个工作日内确认收到报告，并在初步分析完成后同步影响范围与修复计划。修复公开前，请为维护者保留合理的协调时间。

## 数据安全说明

SEG-Y、导航文件和解释成果可能包含保密勘探信息。报告问题时请优先使用合成数据，切勿未经授权上传真实项目文件。SeisForge Studio 不需要管理员权限；任何要求关闭系统安全功能或上传许可证私钥的操作都不属于正常支持流程。
