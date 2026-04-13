# Promo与Portal注册购买一体化及邮件链路增强阶段总结 - 2026-04-13

## 日期

2026-04-13

## 当前分支

`fix/startup-and-device-auth`

## 本轮更新功能总览

结合最近已提交记录与当前工作区未提交改动，本轮后端最近更新可归纳为三条主线：

1. 新用户首单优惠 Promo 主链路落地与收口
2. Portal 注册购买一体化与金额一致性修复
3. 邮件发送链路可观测性增强，以及 Resend webhook 联调准备

这三条主线之间是连续收口关系，而不是孤立需求：

- Promo 把“首单优惠”从一次性 coupon 思路收敛到用户资格 grant 模型。
- Portal 把“未登录购买页”从普通购买页收敛成注册购买一体化入口。
- 邮件链路增强则是为 portal 内联邮箱验证和后续投递排障补足可观测性。

## 每个功能模块具体改了什么

### 1. 新用户首单优惠 Promo 主链路

这一部分已在当前分支中完成并推送，相关主提交包括：

- `1b57ea8 feat(promo): integrate first-order promo across portal flows`
- `39e95ea docs(promo): expand backend change log`

主要完成内容：

- 新增 `user_promo_grants` 模型与迁移
- 在注册、OAuth、设备登录建号、portal 建号等入口统一发放 signup promo
- 在普通购买、portal 购买、支付回调、关单、后台改单等链路接入 grant 的预览、锁定、核销、释放
- 在用户信息接口中增加 promo 状态返回，并支持 dismiss

当前意义：

- 新人优惠已经不再依赖“前端必须传固定 coupon code”
- 后端主链路已经是 grant-first 模型

### 2. Portal 注册购买一体化

这一部分已在当前分支中完成并推送，相关主提交包括：

- `39bbee5 fix(portal): tighten checkout rules and expose payable amount`
- `a81d6b2 feat(portal): add inline verification checkout flow`
- `b9167ed fix(portal): keep new-email first order amount stable`
- `d8d71e8 docs(portal): add inline verification and amount fix summary`

主要完成内容：

- `portal/pre` 增加：
  - `can_purchase`
  - `purchase_block_reason`
  - `account_mode`
  - `next_action`
  - `verification_type`
  - `require_password`
- 新增 portal 专属接口：
  - `POST /v1/public/portal/send_code`
  - `POST /v1/public/portal/verification/ticket`
- `portal/purchase` 支持三类用户：
  - 新邮箱：验证码验证 + password + ticket 后建号下单
  - 已存在已验证邮箱：password 直购
  - 已存在未验证邮箱：验证码验证 + password + ticket 后验证并下单
- 增加同邮箱 pending order 复用，避免重复造单
- 修复新邮箱首单在 portal 下单时误吃 signup promo，导致 `order.amount` 被写低的问题

当前意义：

- purchasing 页已经具备“注册 + 购买一体化”后端能力
- 新邮箱、已验证老用户、未验证老用户三条路径都已经可跑通
- `pre.amount`、`purchase.payable_amount`、`order.amount`、checkout 支付金额已重新对齐

### 3. 邮件发送链路可观测性增强 P0 + P1 准备

这一部分当前仍在工作区，尚未提交。

已完成的代码改动方向：

- 后台测试发信与验证码发送增加发送尝试日志
- 成功和失败都写入 `system_logs(type=10)`
- 为邮件日志扩展：
  - `source`
  - `trace_id`
  - `status`
  - `error_message`
  - `provider_status`
  - `provider_event_time`
  - `provider_message_id`
  - `provider_response_excerpt`
  - `updated_at`
- SMTP 发送时尽量写应用侧 `trace_id` 到邮件头
- 新增 Resend webhook 路由与最小回写逻辑骨架：
  - `POST /v1/webhook/email/resend`

当前意义：

- 应用侧邮件发送过程已经基本可追踪
- 但 provider / delivery 层目前仍未真正闭环，因为 webhook secret 和公网入口放行还未完成

## 涉及哪些核心文件/模块

### Promo / Portal 已完成并已推送的核心模块

- `internal/promo/service.go`
- `internal/model/userpromo/`
- `initialize/migrate/database/02135_user_promo_grants.*.sql`
- `internal/logic/public/order/`
- `internal/logic/public/portal/`
- `internal/logic/notify/`
- `internal/logic/public/user/`
- `apis/public/portal.api`
- `apis/public/user.api`
- `apis/types.api`
- `internal/types/types.go`

### 邮件链路增强当前工作区核心模块

- `internal/logic/admin/authMethod/testEmailSendLogic.go`
- `internal/logic/common/sendEmailCodeLogic.go`
- `queue/logic/email/sendEmailLogic.go`
- `internal/model/log/log.go`
- `internal/logic/admin/log/getMessageLogListLogic.go`
- `internal/logic/admin/log/filterEmailLogLogic.go`
- `pkg/email/sender.go`
- `pkg/email/smtp/email.go`
- `pkg/email/worker.go`
- `queue/types/email.go`
- `internal/handler/webhook/`
- `internal/logic/webhook/`
- `internal/config/config.go`
- `internal/model/auth/auth.go`
- `internal/handler/routes.go`

## 已完成并可用的部分

### 已 commit 且已 push

以下内容已经明确完成并推送到当前分支远端：

- Promo 首单优惠 grant 主链路
- Portal 注册购买一体化核心能力
- `can_purchase / purchase_block_reason / payable_amount`
- 新邮箱 portal 首单金额修复
- Portal 阶段总结文档

当前远端最新已推送提交包括：

- `d8d71e8 docs(portal): add inline verification and amount fix summary`
- `b9167ed fix(portal): keep new-email first order amount stable`
- `a81d6b2 feat(portal): add inline verification checkout flow`
- `39bbee5 fix(portal): tighten checkout rules and expose payable amount`
- `39e95ea docs(promo): expand backend change log`
- `1b57ea8 feat(promo): integrate first-order promo across portal flows`

### 已经在运行中验证过的能力

- 新邮箱 portal 流程金额一致
- 已存在已验证邮箱 password 直购
- 已存在未验证邮箱验证后下单
- 同邮箱 pending order 复用
- 后台测试发信接口成功
- 验证码发送接口成功

## 代码已完成但暂未启用的部分

以下部分代码已经具备，但当前环境尚未完成真正启用：

- Resend webhook 真实联调
- `provider_status / provider_event_time / provider_message_id` 的真实 provider 回写
- delivery / bounced / suppressed / rejected 这一层的真实观测闭环

阻塞点不是代码主体，而是环境：

- `auth_method.method='email'.config.webhook_secret` 仍为空
- 公网 `https://www.bingka.net/v1/webhook/email/resend` 当前没有稳定直达后端
- Resend 控制台 webhook 还未完成最终联调

## 风险、边界和后续建议

### 当前风险

- Portal / promo 主链路已经稳定，但邮件链路的 provider / 投递层仍存在观测盲区
- 当前系统能证明“应用发了”，但还不能稳定证明“provider 已投递”
- 本地工作区仍存在环境类改动，不应混入业务提交

### 边界

- 本轮没有改前端
- 本轮没有重构支付主链路
- 本轮没有做大规模 schema 扩张
- 邮件 P1 目前仍维持“轻量可生产演进”路线，而不是重型全链路监控平台

### 后续建议

1. 完成 `webhook_secret` 配置
2. 为 `/v1/webhook/email/resend` 做公网放行
3. 在 Resend 控制台创建 webhook 并完成真实 event 回写验证
4. 再考虑是否继续做：
   - provider message id 精确关联
   - 后台页面展示增强
   - 更细粒度的 provider 状态统计

## git 状态总结

### 本次生成文档前的真实状态

#### 已 commit 且已 push

- Promo 主链路
- Portal 注册购买一体化主链路
- Portal 金额修复与总结文档

#### 已 commit 但未 push

- 无

#### 已修改但未 commit

当前工作区里，属于本轮最近更新范围、但尚未提交的，是邮件链路 P1 相关代码：

- `internal/config/config.go`
- `internal/handler/routes.go`
- `internal/logic/admin/authMethod/testEmailSendLogic.go`
- `internal/logic/admin/log/filterEmailLogLogic.go`
- `internal/logic/admin/log/getMessageLogListLogic.go`
- `internal/logic/common/sendEmailCodeLogic.go`
- `internal/model/auth/auth.go`
- `internal/model/log/log.go`
- `internal/types/types.go`
- `pkg/email/sender.go`
- `pkg/email/smtp/email.go`
- `pkg/email/worker.go`
- `queue/logic/email/sendEmailLogic.go`
- `queue/types/email.go`
- `internal/handler/webhook/`
- `internal/logic/webhook/`

#### 不相关改动（应排除）

- `docker-compose.yml`
- `etc/ppanel.yaml`
- `.codex`
- `backups/`
- `data/`
- `etc/ppanel.yaml.bak`

### 结论

从仓库真实状态看，最近这一轮后端更新已经完成了“Promo -> Portal -> 邮件链路增强”三段式收口：

- Promo：资格模型落地
- Portal：注册购买一体化与金额一致性落地
- 邮件：应用侧可观测性增强，provider 联调准备就绪但未完全启用
