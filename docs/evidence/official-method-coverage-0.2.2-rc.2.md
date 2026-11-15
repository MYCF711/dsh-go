# 官方 0.2.2-rc.2 方法面 223 条逐条落点盘点（机器生成）

> 生成命令：`python tmp/GO8-RC2/audit_method_coverage.py tmp/GO8-RC2/contracts-rc2.json . docs/evidence/official-method-coverage-0.2.2-rc.2.md`
> 判据是**落点**（源码里出现方法名，`ns/method` 与 `ns.method` 两种形态都算），不代表行为正确；行为证据见 `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md` 与各任务报告。

## 汇总

| 状态 | 条数 |
|---|---:|
| HANDLED | 222 |
| HANDLED-但落点标注未实现 | 1 |
| **合计** | **223** |

## 逐条

| 方法 | 状态 | 生产落点（首个） | 测试落点（首个） |
|---|---|---|---|
| `account/cancelSignIn` | HANDLED | internal/transport/handlers_account_official.go:78 | internal/transport/handlers_account_official_test.go |
| `account/getBalance` | HANDLED | internal/transport/handlers_account_official.go:79 | internal/transport/handlers_account_official_test.go |
| `account/getNotificationContent` | HANDLED | internal/account/notifications.go:8 | internal/account/notifications_test.go |
| `account/getNotifications` | HANDLED | internal/account/notifications.go:9 | internal/account/notifications_test.go |
| `account/getProfile` | HANDLED | internal/transport/handlers_account_official.go:74 | internal/transport/handlers_account_official_test.go |
| `account/getState` | HANDLED | internal/transport/handlers_account_official.go:84 | internal/transport/handlers_account_official_test.go |
| `account/hasRunningAccountTasks` | HANDLED | internal/transport/handlers_account_official.go:85 | internal/transport/handlers_account_official_test.go |
| `account/reportNotificationEvent` | HANDLED | internal/account/notifications.go:10 | internal/account/notifications_test.go |
| `account/signOut` | HANDLED | cmd/dshd/serve_account_api.go:452 | internal/transport/handlers_account_official_test.go |
| `account/startSignIn` | HANDLED | internal/transport/handlers_account_official.go:83 | internal/transport/handlers_account_official_test.go |
| `account/watch` | HANDLED | cmd/dshd/serve_account_api.go:21 | internal/account/notifications_test.go |
| `account/watchExpiry` | HANDLED | internal/account/account.go:941 | internal/transport/handlers_account_official_test.go |
| `actionCards/act` | HANDLED | cmd/dshd/serve_account_api.go:18 | cmd/dshd/serve_action_cards_test.go |
| `agentPresets/list` | HANDLED | cmd/dshd/serve_agent_api.go:1233 | cmd/dshd/serve_agent_api_t04_test.go |
| `agentPresets/read` | HANDLED | cmd/dshd/serve_agent_presets_yaml.go:3 | cmd/dshd/serve_agent_presets_yaml_test.go |
| `agentPresets/select` | HANDLED | cmd/dshd/serve_sandbox_policy.go:349 | internal/transport/handlers_agent_official_test.go |
| `agentTeams/setEnabled` | HANDLED | cmd/dshd/serve.go:789 | cmd/dshd/serve_agent_api_test.go |
| `commands/execute` | HANDLED | cmd/dshd/serve_agent_api.go:106 | cmd/dshd/serve_agent_api_test.go |
| `commands/list` | HANDLED | cmd/dshd/serve_agent_api.go:106 | cmd/dshd/serve_agent_api_test.go |
| `cotTranslation/limits` | HANDLED | internal/transport/handlers_cot_translation_limits_official.go:3 | internal/transport/handlers_parity_a1_minimum_test.go |
| `cotTranslation/translate` | HANDLED | internal/transport/handlers_media_market_official.go:62 | internal/transport/handlers_media_market_official_test.go |
| `credentials/describe` | HANDLED | internal/llm/model_catalog.go:517 | internal/transport/handlers_settings_credential_mask_test.go |
| `credentials/set` | HANDLED | internal/transport/handlers_settings_official.go:11 | internal/llm/model_catalog_test.go |
| `credentials/unset` | HANDLED | cmd/dshd/serve_account_api.go:460 | internal/transport/handlers_settings_credential_mask_test.go |
| `cuaAccess/applicationIcons` | HANDLED | internal/cua/service.go:148 |  |
| `cuaAccess/applications` | HANDLED | internal/cua/service.go:136 |  |
| `cuaAccess/enablePersistentPtc` | HANDLED | internal/cua/service.go:257 |  |
| `cuaAccess/openSettings` | HANDLED | internal/cua/service.go:206 |  |
| `cuaAccess/ptcConfiguration` | HANDLED | internal/cua/ptc.go:11 |  |
| `cuaAccess/requestPermission` | HANDLED | internal/cua/service.go:224 |  |
| `cuaAccess/retryApproval` | HANDLED | internal/cua/service.go:279 |  |
| `cuaAccess/revoke` | HANDLED | internal/cua/service.go:271 |  |
| `cuaAccess/systemStatus` | HANDLED | internal/cua/service.go:119 |  |
| `directoryPicker/createDirectory` | HANDLED | internal/transport/handlers_workspace_official.go:74 | cmd/dshd/serve_workspace_api_test.go |
| `directoryPicker/list` | HANDLED | cmd/dshd/serve_workspace_api.go:71 | cmd/dshd/serve_workspace_api_test.go |
| `directoryPicker/pick` | HANDLED | cmd/dshd/serve_native_api.go:5 | cmd/dshd/serve_native_api_test.go |
| `dynamicCordisRunner/getClientCode` | HANDLED | internal/transport/handlers_media_market_official.go:72 | cmd/dshd/serve_cordis_api_test.go |
| `dynamicCordisRunner/inventory` | HANDLED | internal/transport/handlers_cordis_official.go:19 | cmd/dshd/serve_cordis_api_test.go |
| `dynamicCordisRunner/invoke` | HANDLED | internal/transport/handlers_media_market_official.go:73 | cmd/dshd/serve_cordis_api_test.go |
| `dynamicCordisRunner/reportClientGuardFailure` | HANDLED | internal/transport/handlers_media_market_official.go:74 | cmd/dshd/serve_cordis_api_test.go |
| `dynamicCordisRunner/reportRenderFailure` | HANDLED | internal/transport/handlers_media_market_official.go:75 | cmd/dshd/serve_cordis_api_test.go |
| `dynamicCordisRunner/resolveInspectQuery` | HANDLED | internal/transport/handlers_media_market_official.go:76 | cmd/dshd/serve_cordis_api_test.go |
| `dynamicCordisRunner/resolveRequestRun` | HANDLED | internal/transport/handlers_media_market_official.go:77 | cmd/dshd/serve_cordis_api_test.go |
| `dynamicCordisRunner/runHostHalf` | HANDLED | internal/transport/handlers_media_market_official.go:78 | cmd/dshd/serve_cordis_api_test.go |
| `dynamicCordisRunner/settleUserRun` | HANDLED | internal/transport/handlers_media_market_official.go:79 | cmd/dshd/serve_cordis_api_test.go |
| `dynamicCordisRunner/stopFromPanel` | HANDLED | internal/transport/handlers_media_market_official.go:80 | cmd/dshd/serve_cordis_api_test.go |
| `dynamicCordisRunner/syncInspectManifest` | HANDLED | internal/transport/handlers_media_market_official.go:81 | cmd/dshd/serve_cordis_api_test.go |
| `dynamicCordisRunner/undefineFromPanel` | HANDLED | internal/transport/handlers_media_market_official.go:82 | cmd/dshd/serve_cordis_api_test.go |
| `fileReferences/list` | HANDLED | cmd/dshd/serve_agent_api.go:1544 | cmd/dshd/serve_agent_api_test.go |
| `fileUploads/upload` | HANDLED | cmd/dshd/serve_agent_api.go:52 | cmd/dshd/serve_agent_api_test.go |
| `forgePanel/branches` | HANDLED | cmd/dshd/serve_forge_git.go:3 | cmd/dshd/serve_forge_git_test.go |
| `forgePanel/cancelWorktree` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:21 | internal/transport/handlers_forge_liveshare_official_test.go |
| `forgePanel/changeRequestChecks` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:22 | internal/transport/handlers_forge_change_request_official_test.go |
| `forgePanel/changeRequestCommits` | HANDLED | internal/transport/handlers_forge_rc2.go:8 | internal/transport/handlers_forge_rc2_test.go |
| `forgePanel/changeRequestDiff` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:23 | internal/transport/handlers_forge_change_request_official_test.go |
| `forgePanel/changeRequestReview` | HANDLED | internal/transport/handlers_forge_rc2.go:9 | internal/transport/handlers_forge_rc2_test.go |
| `forgePanel/changeRequestReviewWrite` | HANDLED | internal/transport/handlers_forge_rc2.go:10 | internal/transport/handlers_forge_rc2_test.go |
| `forgePanel/changeRequestStates` | HANDLED | internal/transport/handlers_forge_change_request_official.go:21 | internal/transport/handlers_forge_change_request_official_test.go |
| `forgePanel/changeRequestText` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:24 | internal/transport/handlers_forge_change_request_official_test.go |
| `forgePanel/changeRequestViewed` | HANDLED | internal/transport/handlers_forge_rc2.go:11 | internal/transport/handlers_forge_rc2_test.go |
| `forgePanel/continueInProject` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:25 | cmd/dshd/serve_worktree_api_test.go |
| `forgePanel/diffBases` | HANDLED | internal/forge/git_rc2.go:1 | internal/transport/handlers_forge_rc2_test.go |
| `forgePanel/dismiss` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:26 | internal/transport/handlers_forge_liveshare_official_test.go |
| `forgePanel/follow` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:27 | internal/transport/handlers_forge_liveshare_official_test.go |
| `forgePanel/localCommits` | HANDLED | internal/transport/handlers_forge_rc2.go:13 | internal/transport/handlers_forge_rc2_test.go |
| `forgePanel/localDiff` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:28 | cmd/dshd/serve_forge_git_test.go |
| `forgePanel/localText` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:29 | cmd/dshd/serve_forge_git_test.go |
| `forgePanel/markReady` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:30 | cmd/dshd/serve_forge_write_test.go |
| `forgePanel/merge` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:31 | cmd/dshd/serve_forge_write_test.go |
| `forgePanel/refresh` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:32 | internal/transport/handlers_forge_liveshare_official_test.go |
| `forgePanel/requestReviews` | HANDLED | internal/transport/handlers_forge_rc2.go:14 | internal/transport/handlers_forge_rc2_test.go |
| `forgePanel/requestWorktree` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:33 | cmd/dshd/serve_worktree_api_test.go |
| `forgePanel/rerunChecks` | HANDLED | internal/transport/handlers_forge_rc2.go:15 | internal/transport/handlers_forge_rc2_test.go |
| `forgePanel/restoreWorktree` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:34 | cmd/dshd/serve_worktree_api_test.go |
| `forgePanel/retryWorktree` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:35 | cmd/dshd/serve_worktree_api_test.go |
| `forgePanel/reviewerCandidates` | HANDLED | internal/transport/handlers_forge_rc2.go:16 | internal/transport/handlers_forge_rc2_test.go |
| `forgePanel/setFileViewed` | HANDLED | internal/transport/handlers_forge_rc2.go:17 | internal/transport/handlers_forge_rc2_test.go |
| `forgePanel/stopCreation` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:36 | internal/transport/handlers_forge_liveshare_official_test.go |
| `forgePanel/submitReview` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:37 | cmd/dshd/serve_forge_write_test.go |
| `forgePanel/track` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:38 | internal/transport/handlers_forge_liveshare_official_test.go |
| `forgePanel/updateBranch` | HANDLED | internal/transport/handlers_forge_rc2.go:18 | internal/transport/handlers_forge_rc2_test.go |
| `forgePanel/worktreeCleanup` | HANDLED | cmd/dshd/serve_worktree_api.go:427 | cmd/dshd/serve_worktree_archive_test.go |
| `goals/clear` | HANDLED | internal/transport/handlers_agent_official.go:39 | internal/transport/handlers_agent_official_test.go |
| `goals/complete` | HANDLED | internal/transport/handlers_agent_official.go:38 | internal/transport/handlers_agent_official_test.go |
| `goals/create` | HANDLED | internal/transport/handlers_agent_official.go:34 | internal/transport/handlers_agent_official_test.go |
| `goals/edit` | HANDLED | internal/transport/handlers_agent_official.go:35 | internal/transport/handlers_agent_official_test.go |
| `goals/get` | HANDLED | internal/transport/handlers_agent_official.go:574 | cmd/dshd/serve_goal_truth_test.go |
| `goals/pause` | HANDLED | internal/transport/handlers_agent_official.go:36 | internal/transport/handlers_agent_official_test.go |
| `goals/resume` | HANDLED | internal/goal/goal.go:428 | internal/transport/handlers_agent_official_test.go |
| `job/follow` | HANDLED | cmd/dshd/serve_official_wire.go:280 | cmd/dshd/serve_terminal_api_test.go |
| `job/kill` | HANDLED | cmd/dshd/serve_official_wire.go:292 | cmd/dshd/serve_terminal_api_test.go |
| `job/list` | HANDLED | cmd/dshd/serve_official_wire.go:280 | cmd/dshd/serve_terminal_api_test.go |
| `liveshareShares/addSessions` | HANDLED | internal/transport/handlers_forge_liveshare_official.go:125 | cmd/dshd/serve_liveshare_api_test.go |
| `liveshareShares/audit` | HANDLED | internal/transport/handlers_liveshare_official.go:77 | cmd/dshd/serve_liveshare_api_test.go |
| `liveshareShares/capabilities` | HANDLED | internal/transport/handlers_liveshare_official.go:73 | cmd/dshd/serve_liveshare_api_test.go |
| `liveshareShares/create` | HANDLED | internal/transport/handlers_liveshare_official.go:67 | cmd/dshd/serve_liveshare_api_test.go |
| `liveshareShares/identity` | HANDLED | internal/transport/handlers_liveshare_official.go:76 | cmd/dshd/serve_liveshare_api_test.go |
| `liveshareShares/list` | HANDLED | cmd/dshd/serve_liveshare_api.go:694 | cmd/dshd/serve_liveshare_api_test.go |
| `liveshareShares/relay` | HANDLED | internal/transport/handlers_liveshare_official.go:74 | cmd/dshd/serve_liveshare_api_test.go |
| `liveshareShares/setCapabilities` | HANDLED | internal/transport/handlers_liveshare_official.go:72 | cmd/dshd/serve_liveshare_api_test.go |
| `liveshareShares/setRelay` | HANDLED | cmd/dshd/serve_liveshare_api.go:707 | cmd/dshd/serve_liveshare_api_test.go |
| `liveshareShares/stop` | HANDLED | cmd/dshd/serve_liveshare_api.go:699 | cmd/dshd/serve_liveshare_api_test.go |
| `liveshareShares/testPublish` | HANDLED | internal/transport/handlers_liveshare_official.go:79 | cmd/dshd/serve_liveshare_api_test.go |
| `liveshareShares/testRelay` | HANDLED | internal/transport/handlers_liveshare_official.go:78 | cmd/dshd/serve_liveshare_api_test.go |
| `liveshareShares/updatePermissions` | HANDLED | internal/transport/handlers_liveshare_official.go:71 | cmd/dshd/serve_liveshare_api_test.go |
| `llm/discoverModels` | HANDLED | cmd/dshd/serve_account_api.go:86 | internal/llm/discover_test.go |
| `llm/listConfigurableProviders` | HANDLED | cmd/dshd/serve_provider_registry.go:3 | internal/settingsform/retry_policy_schema_test.go |
| `llm/listProviders` | HANDLED | cmd/dshd/serve_provider_registry.go:3 | internal/transport/handlers_account_official_test.go |
| `mcpManager/probe` | HANDLED | internal/mcp/registry.go:13 | internal/transport/handlers_mcp_manager_rc2_test.go |
| `mcpManager/restart` | HANDLED | internal/mcp/restart.go:8 | internal/transport/handlers_mcp_manager_rc2_test.go |
| `mcpManager/watch` | HANDLED | internal/mcp/types.go:15 | internal/mcp/types_test.go |
| `messageFeedback/delete` | HANDLED | internal/transport/handlers_agent_official.go:60 | internal/transport/handlers_agent_official_test.go |
| `messageFeedback/list` | HANDLED | internal/transport/handlers_agent_official.go:58 | internal/transport/handlers_agent_official_test.go |
| `messageFeedback/put` | HANDLED | internal/transport/handlers_agent_official.go:59 | internal/transport/handlers_agent_official_test.go |
| `officeKit/generation` | HANDLED | internal/transport/handlers_office_kit_rc2.go:7 | cmd/dshd/serve_office_kit_test.go |
| `officeKit/render` | HANDLED | internal/officekit/convert.go:90 | cmd/dshd/serve_office_kit_test.go |
| `permissionPresets/catalog` | HANDLED | internal/transport/handlers_permission_presets_official.go:3 | internal/transport/handlers_parity_a1_minimum_test.go |
| `pluginInventory/list` | HANDLED | cmd/dshd/serve_plugins_official.go:50 | internal/transport/handlers_parity_a1_minimum_test.go |
| `pluginManager/cancelInstall` | HANDLED | internal/transport/handlers_pluginmanager_official.go:73 | cmd/dshd/serve_plugins_official_test.go |
| `pluginManager/inspect` | HANDLED | internal/pluginmgr/bundles.go:142 | cmd/dshd/serve_plugins_official_test.go |
| `pluginManager/installBundle` | HANDLED | internal/pluginmgr/jobs.go:3 | cmd/dshd/serve_plugins_official_test.go |
| `pluginManager/listBundles` | HANDLED | internal/transport/handlers_pluginmanager_official.go:76 | cmd/dshd/serve_plugins_official_test.go |
| `pluginManager/listPlugins` | HANDLED | internal/transport/handlers_pluginmanager_official.go:77 | cmd/dshd/serve_plugins_official_test.go |
| `pluginManager/listVersionExemptions` | HANDLED | internal/transport/handlers_pluginmanager_official.go:63 | cmd/dshd/serve_plugins_official_test.go |
| `pluginManager/listVersions` | HANDLED | cmd/dshd/serve_official_wire.go:259 | internal/pluginmgr/official/adapter_test.go |
| `pluginManager/readBundleDetail` | HANDLED | internal/transport/handlers_pluginmanager_official.go:71 | internal/pluginmgr/official/adapter_test.go |
| `pluginManager/registries` | HANDLED | cmd/dshd/serve_official_wire.go:258 | cmd/dshd/serve_plugins_official_test.go |
| `pluginManager/removeBundle` | HANDLED | internal/transport/handlers_pluginmanager_official.go:67 | cmd/dshd/serve_plugins_official_test.go |
| `pluginManager/setBundleEnabled` | HANDLED | internal/transport/handlers_pluginmanager_official.go:68 | cmd/dshd/serve_plugins_official_test.go |
| `pluginManager/setPluginEnabled` | HANDLED | internal/transport/handlers_pluginmanager_official.go:69 | cmd/dshd/serve_plugins_official_test.go |
| `pluginManager/setVersionExemption` | HANDLED | internal/pluginmgr/exemption.go:301 | cmd/dshd/serve_plugins_official_test.go |
| `pluginManager/updateBundle` | HANDLED | internal/pluginmgr/manager.go:414 | cmd/dshd/serve_plugins_official_test.go |
| `pluginManager/waitForInstall` | HANDLED | internal/transport/handlers_pluginmanager_official.go:72 | cmd/dshd/serve_plugins_official_test.go |
| `pluginMarket/listIndexes` | HANDLED | cmd/dshd/serve_official_wire.go:245 | internal/transport/handlers_plugin_market_official_test.go |
| `pluginMarket/readEntryDetail` | HANDLED | cmd/dshd/serve_official_wire.go:256 | internal/transport/handlers_media_market_official_test.go |
| `pluginMarket/search` | HANDLED | cmd/dshd/serve_official_wire.go:255 | internal/transport/handlers_media_market_official_test.go |
| `pluginMarket/setIndexes` | HANDLED | cmd/dshd/serve_official_wire.go:254 | internal/transport/handlers_media_market_official_test.go |
| `pluginRecommendations/describe` | HANDLED | cmd/dshd/serve_plugin_recommendations.go:3 | cmd/dshd/serve_plugin_recommendations_test.go |
| `pluginRegistryProbe/fastest` | HANDLED | cmd/dshd/serve_official_wire.go:257 | internal/transport/handlers_plugin_market_official_test.go |
| `productAnalytics/enabled` | HANDLED | cmd/dshd/serve_analytics_api.go:57 | cmd/dshd/serve_analytics_api_test.go |
| `productAnalytics/report` | HANDLED | cmd/dshd/serve_account_api.go:17 | cmd/dshd/serve_analytics_api_test.go |
| `productAnalytics/watchPolicy` | HANDLED | cmd/dshd/serve_analytics_api.go:58 | cmd/dshd/serve_analytics_api_test.go |
| `remoteAccess/follow` | HANDLED | cmd/dshd/serve_remote_access_follow.go:3 | cmd/dshd/serve_w1_streams_test.go |
| `schedule/catalog` | HANDLED | cmd/dshd/serve_schedule_api.go:4 | cmd/dshd/serve_schedule_api_official_catalog_test.go |
| `schedule/create` | HANDLED | cmd/dshd/serve_schedule_api.go:3 | cmd/dshd/serve_schedule_api_test.go |
| `schedule/deleteInactive` | HANDLED | cmd/dshd/serve_schedule_api.go:1029 | internal/transport/handlers_schedule_official_test.go |
| `schedule/deliveryLimits` | HANDLED | cmd/dshd/serve_schedule_api.go:1029 | cmd/dshd/serve_schedule_api_official_catalog_test.go |
| `schedule/edit` | HANDLED | cmd/dshd/serve_schedule_api.go:526 | cmd/dshd/serve_schedule_api_test.go |
| `schedule/history` | HANDLED | cmd/dshd/serve_schedule_api.go:34 | cmd/dshd/serve_schedule_api_test.go |
| `schedule/list` | HANDLED | internal/schedule/mutations.go:281 | cmd/dshd/serve_schedule_api_test.go |
| `schedule/run` | HANDLED | cmd/dshd/serve_schedule_api.go:32 | cmd/dshd/serve_schedule_api_test.go |
| `schedule/view` | HANDLED | cmd/dshd/serve_schedule_api.go:805 | cmd/dshd/serve_schedule_api_test.go |
| `session/attachment` | HANDLED | internal/transport/handlers_session_official.go:16 | internal/officialwire/upload_test.go |
| `session/canOpenWorkspacePath` | HANDLED | cmd/dshd/serve_native_api.go:7 | cmd/dshd/serve_native_api_test.go |
| `session/cancel` | HANDLED | cmd/dshd/serve.go:440 | cmd/dshd/serve_approval_cancel_test.go |
| `session/control` | HANDLED | internal/transport/context_pressure_view.go:16 | internal/transport/handlers_session_official_t04_test.go |
| `session/create` | HANDLED | cmd/dshd/serve_create.go:29 | cmd/dshd/context_usage_real_test.go |
| `session/events` | HANDLED | cmd/dshd/serve_official_wire.go:52 | cmd/dshd/serve_official_wire_test.go |
| `session/follow` | HANDLED | cmd/dshd/serve_official_wire_follow.go:3 | cmd/dshd/serve_official_wire_follow_test.go |
| `session/fork` | HANDLED | internal/acp/server.go:382 | cmd/dshd/serve_session_ops_w145_test.go |
| `session/initializeDefaultModel` | HANDLED | cmd/dshd/serve_session_api.go:44 | internal/transport/handlers_session_official_t04_test.go |
| `session/list` | HANDLED | cmd/dshd/serve_liveshare_api.go:735 | cmd/dshd/metadata_init_test.go |
| `session/modelCatalog` | HANDLED | internal/transport/handlers_agent_official.go:92 | internal/transport/handlers_session_official_t04_test.go |
| `session/observeEvent` | HANDLED | internal/transport/handlers_session_official.go:21 | cmd/dshd/serve_official_wire_streams_test.go |
| `session/observeProjections` | HANDLED | internal/transport/handlers_session_official.go:21 | cmd/dshd/serve_official_wire_streams_test.go |
| `session/observeState` | HANDLED | internal/transport/handlers_session_official.go:21 | cmd/dshd/serve_official_wire_streams_test.go |
| `session/openWorkspacePath` | HANDLED | cmd/dshd/serve_native_api.go:8 | cmd/dshd/serve_native_api_test.go |
| `session/page` | HANDLED | cmd/dshd/serve_official_wire.go:173 | cmd/dshd/serve_official_wire_test.go |
| `session/projections` | HANDLED | cmd/dshd/serve_action_cards.go:53 | internal/transport/context_pressure_view_test.go |
| `session/prompt` | HANDLED | cmd/dshd/acp.go:470 | cmd/dshd/checkpoint_cli_test.go |
| `session/rename` | HANDLED | cmd/dshd/serve_title_auto.go:905 | cmd/dshd/metadata_init_test.go |
| `session/search` | HANDLED | internal/transport/handlers_session_official.go:15 | internal/transport/handlers_session_official_test.go |
| `session/selectModel` | HANDLED | cmd/dshd/serve_session_api.go:47 | internal/transport/handlers_session_official_test.go |
| `session/state` | HANDLED | cmd/dshd/serve_queue_official.go:3 | cmd/dshd/serve_queue_official_test.go |
| `session/updateQueue` | HANDLED | cmd/dshd/serve_queue_official.go:3 | cmd/dshd/serve_queue_official_test.go |
| `session/wait` | HANDLED | cmd/dshd/serve_terminal_api.go:98 | internal/transport/handlers_session_official_test.go |
| `session/workspacePathApplications` | HANDLED | cmd/dshd/serve_native_api.go:9 | cmd/dshd/serve_native_api_test.go |
| `sessionFeedback/record` | HANDLED | cmd/dshd/serve_agent_api.go:1915 | cmd/dshd/serve_agent_api_test.go |
| `sessionReferenceResolver/candidates` | HANDLED | cmd/dshd/serve_agent_api.go:1972 | cmd/dshd/serve_agent_api_test.go |
| `settings/describe` | HANDLED | cmd/dshd/serve_official_wire.go:267 | cmd/dshd/serve_settings_api_test.go |
| `settings/mutate` | HANDLED | cmd/dshd/serve_settings.go:211 | cmd/dshd/serve_settings_secret_redaction_test.go |
| `settings/openSettingsDocument` | HANDLED | cmd/dshd/serve_native_api.go:6 | cmd/dshd/serve_native_api_test.go |
| `settings/replace` | HANDLED | internal/settingsform/settingsform.go:441 | internal/transport/handlers_settings_official_test.go |
| `settings/update` | HANDLED | internal/settingsform/settingsform.go:99 | internal/settingsform/settingsform_test.go |
| `skills/list` | HANDLED | cmd/dshd/serve_agent_api.go:1450 | cmd/dshd/serve_agent_api_test.go |
| `speech/cancelPreparation` | HANDLED | internal/transport/handlers_media_market_official.go:60 | cmd/dshd/serve_speech_test.go |
| `speech/catalog` | HANDLED | internal/transport/handlers_speech_official.go:17 | cmd/dshd/serve_speech_test.go |
| `speech/configure` | HANDLED | internal/transport/handlers_media_market_official.go:59 | cmd/dshd/serve_speech_test.go |
| `speech/follow` | HANDLED | internal/transport/handlers_speech_official.go:18 | cmd/dshd/serve_speech_test.go |
| `speech/prepare` | HANDLED | internal/transport/handlers_media_market_official.go:57 | cmd/dshd/serve_speech_test.go |
| `speech/transcribe` | HANDLED | internal/transport/handlers_media_market_official.go:58 | cmd/dshd/serve_speech_test.go |
| `subagents/interruptByUser` | HANDLED | cmd/dshd/serve.go:790 | cmd/dshd/serve_agent_api_test.go |
| `subagents/prompt` | HANDLED | cmd/dshd/serve.go:797 | cmd/dshd/serve_agent_api_test.go |
| `terminal/attach` | HANDLED | cmd/dshd/serve_official_wire.go:280 | cmd/dshd/serve_terminal_api_test.go |
| `terminal/close` | HANDLED | cmd/dshd/serve_official_wire.go:287 | internal/transport/handlers_job_terminal_official_test.go |
| `terminal/create` | HANDLED | cmd/dshd/serve.go:641 | cmd/dshd/serve_terminal_api_test.go |
| `terminal/environment` | HANDLED | cmd/dshd/serve_official_wire.go:288 | internal/transport/handlers_job_terminal_official_test.go |
| `terminal/list` | HANDLED | cmd/dshd/serve.go:470 | cmd/dshd/terminal_bridge_test.go |
| `terminal/rename` | HANDLED | cmd/dshd/serve_official_wire.go:290 | internal/transport/handlers_job_terminal_official_test.go |
| `terminal/retain` | HANDLED | cmd/dshd/serve_official_wire.go:280 | cmd/dshd/serve_terminal_api_test.go |
| `terminal/shells` | HANDLED | cmd/dshd/serve_official_wire.go:289 | internal/transport/handlers_job_terminal_official_test.go |
| `terminal/watch` | HANDLED | cmd/dshd/serve.go:470 | internal/terminal/watch_test.go |
| `toolResults/watch` | HANDLED | cmd/dshd/serve_agent_api.go:2042 | cmd/dshd/serve_agent_api_test.go |
| `userQuestions/answer` | HANDLED | internal/transport/handlers_agent_official.go:47 | cmd/dshd/serve_agent_api_test.go |
| `userQuestions/attachWait` | HANDLED | cmd/dshd/serve_agent_api.go:108 | cmd/dshd/serve_agent_api_test.go |
| `web/listProviders` | HANDLED | cmd/dshd/serve.go:872 | internal/transport/handlers_web_providers_rc2_test.go |
| `workspace/archiveSession` | HANDLED | cmd/dshd/serve.go:827 | cmd/dshd/serve_workspace_api_test.go |
| `workspace/create` | HANDLED | cmd/dshd/serve_workspace_api.go:663 | cmd/dshd/serve_w1_streams_test.go |
| `workspace/delete` | HANDLED | cmd/dshd/serve_workspace_registry.go:97 | cmd/dshd/serve_w1_streams_test.go |
| `workspace/follow` | HANDLED | cmd/dshd/serve_workspace_follow.go:3 | cmd/dshd/serve_w1_streams_test.go |
| `workspace/initializeDefault` | HANDLED | cmd/dshd/documents_lookup_timeout.go:15 | cmd/dshd/serve_workspace_api_initialize_test.go |
| `workspace/insertBefore` | HANDLED | internal/transport/handlers_workspace_official.go:146 | cmd/dshd/serve_workspace_api_test.go |
| `workspace/insertSessionBefore` | HANDLED | internal/transport/handlers_workspace_official.go:61 | cmd/dshd/serve_workspace_api_test.go |
| `workspace/pinSession` | HANDLED | internal/transport/handlers_workspace_official.go:62 | cmd/dshd/serve_workspace_api_test.go |
| `workspace/rename` | HANDLED-但落点标注未实现 | internal/transport/handlers_workspace_official.go:58 | cmd/dshd/serve_w1_streams_test.go |
| `workspace/unarchiveSession` | HANDLED | internal/transport/handlers_workspace_official.go:65 | cmd/dshd/serve_workspace_api_test.go |
| `workspace/unpinSession` | HANDLED | internal/transport/handlers_workspace_official.go:63 | cmd/dshd/serve_workspace_api_test.go |
| `workspaceFiles/changes` | HANDLED | cmd/dshd/serve_workspace_api.go:39 | cmd/dshd/serve_official_wire_streams_test.go |
| `workspaceFiles/list` | HANDLED | cmd/dshd/serve_workspace_api.go:63 | cmd/dshd/serve_workspace_api_parity_test.go |
| `workspaceFiles/read` | HANDLED | cmd/dshd/serve_official_wire.go:188 | cmd/dshd/serve_official_wire_test.go |
| `workspaceFiles/readBytes` | HANDLED | cmd/dshd/serve_official_wire.go:188 | cmd/dshd/serve_official_wire_test.go |
| `workspaceFiles/search` | HANDLED | cmd/dshd/serve_workspace_api.go:1572 | internal/fssearch/search_test.go |
| `workspaceFiles/stat` | HANDLED | cmd/dshd/serve_workspace_api.go:1312 | cmd/dshd/serve_workspace_api_test.go |
