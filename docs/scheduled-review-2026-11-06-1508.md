# 2026-11-06 15:08 定时复核

## 用户最新策略

- GUI从逐部件测试改为：先完整实现官方结构树/素材/交互，再由Lead集中统一测试。
- 右栏真实OS补验已暂停，不再发送OS键鼠；实现阶段只做必要编译/静态自检。
- goal已更新为active revision10，TUI与GUI仍并行。

## TUI / 宿主

- W162无catalog启动、W165环境模型真实effort、W166 turn finalizing均已Lead全绿；W161/W164已收口。
- W167已交付NO-GO：80=69/5、120=75/8，拆出W168 Export、W169键位、W170输入、W171 scrollback。
- W168已在最新树证实projection跨root turn覆盖，W173正在修行身份；W172正在修inline replay/resize；W170已定位parse-keypress分片解析。
- W163等待W168/W169/W170/W171/W172/W173全部关闭，GO.1未封版。

## GUI

- composer附件rail发现W119既有实现，转为支撑审计，避免重复开发。
- 会话附件卡已批准attachments.go+conversation.go/message_area.go最小实画接线，按官方attachmentRow→bubble→referenceSummary，不混用Composer卡几何。
- sidebar Home/End Target作用域修复继续；真实OS集中测试后置。
- 未见reset、进程误杀、Electron/WebView回退或goal停摆。
