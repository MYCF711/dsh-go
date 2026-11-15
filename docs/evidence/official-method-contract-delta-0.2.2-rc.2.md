# 官方方法契约 rc.1 -> rc.2 差异（结构性；描述文案变化不计）

- 方法总数：rc.1 **193** -> rc.2 **223**；新增 **35** / 删除 **5** / **结构契约变更 32**
- 源：`<asar>/dsh/node_modules/@deepseek-ai/*/lib/typert.host.js` 的 `createTypertRemoteInfo` 描述符 + `TYPERT.model` 签名

## 1. 新增方法（35）

### `account/getNotificationContent`  (package: `dsh-deepseek-account`)

描述：Query localized content of a notification, including one already consumed.

参数：
- `[0]` **id**（source=json）—— notification whose content is requested.
  - schema: `{"type": "string"}`
- `[1]` **client**（source=json）—— identity of the requesting UI; its language selects the localized content.
  - schema: `{"type": "object", "properties": {"version": {"type": "string"}, "locale": {"type": "string", "description": "Active UI language; only its primary subtag selects the Platform wire locale."}, "timezoneOffsetSeconds": {"type": "number", "description": "Offset from UTC in seconds, positive east of Greenwich."}}, "required": ["version", "locale", "timezoneOffsetSeconds"], "additionalProperties": true}`

结果：content without changing consumption, or null if signed out or the grant changed.

```json
{
 "oneOf": [
  {
   "type": "null",
   "const": null
  },
  {
   "type": "object",
   "properties": {
    "id": {
     "type": "string"
    },
    "createdAt": {
     "type": "string"
    },
    "displayUntil": {
     "oneOf": [
      {
       "type": "null",
       "const": null
      },
      {
       "type": "string"
      }
     ],
     "description": "Display deadline; null means the notification never expires."
    },
    "title": {
     "type": "string"
    },
    "body": {
     "type": "string"
    },
    "actions": {
     "type": "array",
     "items": {
      "type": "object",
      "properties": {
       "label": {
        "type": "string"
       },
       "style": {
        "type": "string",
        "enum": [
         "primary",
         "secondary"
        ]
       },
       "target": {
        "oneOf": [
         {
          "type": "object",
          "properties": {
           "type": {
            "type": "string",
            "const": "app_route"
           },
           "name": {
            "type": "string",
            "const": "account"
           }
          },
          "required": [
           "type",
           "name"
          ],
          "additionalProperties": true
         },
         {
          "type": "object",
          "properties": {
           "type": {
            "type": "string",
            "const": "dismiss"
           }
          },
          "required": [
           "type"
          ],
          "additionalProperties": true
         }
        ]
       }
      },
      "required": [
       "label",
       "style",
       "target"
      ],
      "additionalProperties": true
     }
    },
    "data": {
     "description": "Optional business data, retained independently of the localized copy."
    }
   },
   "required": [
    "id",
    "createdAt",
    "displayUntil",
    "title",
    "body",
    "actions"
   ],
   "additionalProperties": true
  }
 ]
}
```

### `account/getNotifications`  (package: `dsh-deepseek-account`)

描述：Query unconsumed account notifications in server order without reading profile data.

参数：
- `[0]` **client**（source=json）—— identity of the requesting UI for this call; its language selects the server-authored message.
  - schema: `{"type": "object", "properties": {"version": {"type": "string"}, "locale": {"type": "string", "description": "Active UI language; only its primary subtag selects the Platform wire locale."}, "timezoneOffsetSeconds": {"type": "number", "description": "Offset from UTC in seconds, positive east of Greenwich."}}, "required": ["version", "locale", "timezoneOffsetSeconds"], "additionalProperties": true}`

结果：notifications with their next query delay, or null if signed out or the grant changed during the query.

```json
{
 "oneOf": [
  {
   "type": "null",
   "const": null
  },
  {
   "type": "object",
   "properties": {
    "nextPollAfterSeconds": {
     "type": "number",
     "description": "Positive delay in seconds before querying again, within the browser timer range."
    },
    "notifications": {
     "type": "array",
     "items": {
      "type": "object",
      "properties": {
       "id": {
        "type": "string"
       },
       "createdAt": {
        "type": "string"
       },
       "displayUntil": {
        "oneOf": [
         {
          "type": "null",
          "const": null
         },
         {
          "type": "string"
         }
        ],
        "description": "Display deadline; null means the notification never expires."
       },
       "title": {
        "type": "string"
       },
       "body": {
        "type": "string"
       },
       "actions": {
        "type": "array",
        "items": {
         "type": "object",
         "properties": {
          "label": {
           "type": "string"
          },
          "style": {
           "type": "string",
           "enum": [
            "primary",
            "secondary"
           ]
          },
          "target": {
           "oneOf": [
            {
             "type": "object",
             "properties": {
              "type": {
               "type": "string",
               "const": "app_route"
              },
              "name": {
               "type": "string",
               "const": "account"
              }
             },
             "required": [
              "type",
              "name"
             ],
             "additionalProperties": true
            },
            {
             "type": "object",
             "properties": {
              "type": {
               "type": "string",
               "const": "dismiss"
              }
             },
             "required": [
              "type"
             ],
             "additionalProperties": true
            }
           ]
          }
         },
         "required": [
          "label",
          "style",
          "target"
         ],
         "additionalProperties": true
        }
       },
       "data": {
        "description": "Optional business data, retained independently of the localized copy."
       }
      },
      "required": [
       "id",
       "createdAt",
       "displayUntil",
       "title",
       "body",
       "actions"
      ],
      "additionalProperties": true
     }
    }
   },
   "required": [
    "nextPollAfterSeconds",
    "notifications"
   ],
   "additionalProperties": true
  }
 ]
}
```

### `account/reportNotificationEvent`  (package: `dsh-deepseek-account`)

描述：Report a notification interaction; the backend decides consumption.

参数：
- `[0]` **id**（source=json）—— notification the user interacted with.
  - schema: `{"type": "string"}`
- `[1]` **event**（source=json）—— displayed frame, non-dismiss action click, or explicit dismissal.
  - schema: `{"type": "string", "enum": ["displayed", "cta_clicked", "dismissed"]}`
- `[2]` **client**（source=json）—— identity of the requesting UI for this call.
  - schema: `{"type": "object", "properties": {"version": {"type": "string"}, "locale": {"type": "string", "description": "Active UI language; only its primary subtag selects the Platform wire locale."}, "timezoneOffsetSeconds": {"type": "number", "description": "Offset from UTC in seconds, positive east of Greenwich."}}, "required": ["version", "locale", "timezoneOffsetSeconds"], "additionalProperties": true}`

结果：true once the server accepts the event; false if signed out or credentials changed during the call.

```json
{
 "type": "boolean"
}
```

### `agentTeams/setEnabled`  (package: `dsh-agent-team`)

描述：Save a root Session's Team selection without stopping children or deleting tasks. The selection takes effect before awaiting persistence. Tools resolve availability at step preparation; prompt providers read the selection when evaluated. Task mutations check the current mode when their queued operation starts; admitted operations finish.

参数：
- `[0]` **agent**（source=lookup, wire=agentId）—— exact live root whose tree receives the selection.
  - schema: `{"type": "string"}`
- `[1]` **enabled**（source=json）—— selected Team mode.
  - schema: `{"type": "boolean"}`

结果：fulfillment after the selection flushes.

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-agent-team#agentTeams/setEnabled:result',
        create: _deepseek_ai_dsh_agent_team_agentTeams_setEnabled_result$schema,
        description: 'fulfillment after the selection flushes.',
        jsonSchemaError: 'typert tool schema: unsupported void value',
      }
```

### `cuaAccess/applicationIcons`  (package: `dsh-cua`)

描述：Load presentation icons separately from application decisions.

参数：

结果：Optional native PNG icons keyed by executable path; applications without paths are skipped.

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-cua#cuaAccess/applicationIcons:result',
        create: _deepseek_ai_dsh_cua_cuaAccess_applicationIcons_result$schema,
        description: 'Optional native PNG icons keyed by executable path; applications without paths are skipped.',
        jsonSchemaError: 'typert tool schema: selected dictionaries require string keys and unconstrained JSON values',
      }
```

### `cuaAccess/applications`  (package: `dsh-cua`)

描述：Read fresh stored application decisions without issuing a native request.

参数：

结果：Saved grants, refused applications and durable-storage availability.

```json
{
 "type": "object",
 "properties": {
  "allowedApps": {
   "type": "array",
   "items": {
    "type": "object",
    "properties": {
     "id": {
      "type": "string",
      "description": "Native identity digest admitted from the helper or validated profile configuration."
     },
     "name": {
      "type": "string",
      "description": "Application label presented when the decision was made."
     },
     "executablePath": {
      "type": "string",
      "description": "Canonical executable path for display and icons; absent when the operating system cannot provide it."
     }
    },
    "required": [
     "id",
     "name"
    ],
    "additionalProperties": true
   }
  },
  "blockedApps": {
   "type": "array",
   "items": {
    "type": "object",
    "properties": {
     "id": {
      "type": "string",
      "description": "Native identity digest admitted from the helper or validated profile configuration."
     },
     "name": {
      "type": "string",
      "description": "Application label presented when the decision was made."
     },
     "executablePath": {
      "type": "string",
      "description": "Canonical executable path for display and icons; absent when the operating system cannot provide it."
     }
    },
    "required": [
     "id",
     "name"
    ],
    "additionalProperties": true
   },
   "description": "Refusals from any session, excluding saved grants; other sessions may still hold session access."
  },
  "persistentAvailable": {
   "type": "boolean"
  }
 },
 "required": [
  "allowedApps",
  "blockedApps",
  "persistentAvailable"
 ],
 "additionalProperties": true
}
```

### `cuaAccess/enablePersistentPtc`  (package: `dsh-cua`)

描述：Persist in-memory PTC execution through the existing profile editor and Loader lifecycle.

参数：

结果：Completion after the shared runtime reload; already-persistent runtimes are left intact.

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-cua#cuaAccess/enablePersistentPtc:result',
        create: _deepseek_ai_dsh_cua_cuaAccess_enablePersistentPtc_result$schema,
        description: 'Completion after the shared runtime reload; already-persistent runtimes are left intact.',
        jsonSchemaError: 'typert tool schema: unsupported void value',
      }
```

### `cuaAccess/openSettings`  (package: `dsh-cua`)

描述：Open the macOS privacy pane selected by the user.

参数：
- `[0]` **permission**（source=json）—— Corresponding system privilege.
  - schema: `{"type": "string", "enum": ["accessibility", "screenRecording"]}`
- `[1]` **locale**（source=json）—— Current Client language for provider-owned recovery instructions.
  - schema: `{"type": "string"}`

结果：Completion after macOS accepted the open request.

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-cua#cuaAccess/openSettings:result',
        create: _deepseek_ai_dsh_cua_cuaAccess_openSettings_result$schema,
        description: 'Completion after macOS accepted the open request.',
        jsonSchemaError: 'typert tool schema: unsupported void value',
      }
```

### `cuaAccess/ptcConfiguration`  (package: `dsh-cua`)

描述：Read PTC execution state separately from native permission checks.

参数：

结果：Whether the shared runtime retains memory and has an editable profile entry.

```json
{
 "type": "object",
 "properties": {
  "persistent": {
   "type": "boolean"
  },
  "writable": {
   "type": "boolean"
  }
 },
 "required": [
  "persistent",
  "writable"
 ],
 "additionalProperties": true
}
```

### `cuaAccess/requestPermission`  (package: `dsh-cua`)

描述：Request the selected system privilege through the native provider.

参数：
- `[0]` **permission**（source=json）—— macOS privilege to request.
  - schema: `{"type": "string", "enum": ["accessibility", "screenRecording"]}`
- `[1]` **locale**（source=json）—— Current Client language for provider-owned permission UI.
  - schema: `{"type": "string"}`

结果：Current permission observations; requesting does not establish consent.

```json
{
 "oneOf": [
  {
   "type": "object",
   "properties": {
    "platform": {
     "type": "string",
     "const": "darwin"
    },
    "accessibility": {
     "type": "boolean"
    },
    "screenRecording": {
     "type": "boolean"
    },
    "helper": {
     "type": "object",
     "properties": {
      "pid": {
       "type": "number"
      },
      "path": {
       "type": "string"
      },
      "name": {
       "type": "string"
      }
     },
     "required": [
      "pid"
     ],
     "additionalProperties": true
    },
    "parent": {
     "type": "object",
     "properties": {
      "pid": {
       "type": "number"
      },
      "path": {
       "type": "string"
      },
      "name": {
       "type": "string"
      }
     },
     "required": [
      "pid"
     ],
     "additionalProperties": true
    },
    "launchApplication": {
     "type": "object",
     "properties": {
      "name": {
       "type": "string"
      },
      "path": {
       "type": "string"
      },
      "icon": {
       "type": "string"
      }
     },
     "required": [
      "name",
      "path"
     ],
     "additionalProperties": true,
     "description": "Application macOS reports as responsible for the helper, captured at startup; absent when it cannot be identified."
    }
   },
   "required": [
    "platform",
    "accessibility",
    "screenRecording",
    "helper",
    "parent"
   ],
   "additionalProperties": true
  },
  {
   "type": "object",
   "properties": {
    "platform": {
     "type": "string",
     "const": "win32"
    },
    "sessionLocked": {
     "oneOf": [
      {
       "type": "null",
       "const": null
      },
      {
       "type": "boolean",
       "enum": [
        false,
        true
       ]
      }
     ]
    },
    "captureAvailable": {
     "type": "boolean"
    },
    "elevated": {
     "type": "boolean"
    },
    "helper": {
     "type": "object",
     "properties": {
      "pid": {
       "type": "number"
      },
      "path": {
       "type": "string"
      },
      "name": {
       "type": "string"
      }
     },
     "required": [
      "pid"
     ],
     "additionalProperties": true
    }
   },
   "required": [
    "platform",
    "sessionLocked",
    "captureAvailable",
    "elevated",
    "helper"
   ],
   "additionalProperties": true
  }
 ]
}
```

### `cuaAccess/retryApproval`  (package: `dsh-cua`)

描述：Clear this application's refusals across sessions so each session may ask for access again.

参数：
- `[0]` **id**（source=json）—— Refused or revoked application displayed on the permission page.
  - schema: `{"type": "string"}`

结果：Completion after clearing refusals; no permission is granted and no action is replayed.

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-cua#cuaAccess/retryApproval:result',
        create: _deepseek_ai_dsh_cua_cuaAccess_retryApproval_result$schema,
        description: 'Completion after clearing refusals; no permission is granted and no action is replayed.',
        jsonSchemaError: 'typert tool schema: unsupported void value',
      }
```

### `cuaAccess/revoke`  (package: `dsh-cua`)

描述：Remove saved and session access and join affected operations.

参数：
- `[0]` **id**（source=json）—— Application identity displayed on the permission page.
  - schema: `{"type": "string"}`

结果：Completion after persistence and cancellation; dispatched input cannot be undone.

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-cua#cuaAccess/revoke:result',
        create: _deepseek_ai_dsh_cua_cuaAccess_revoke_result$schema,
        description: 'Completion after persistence and cancellation; dispatched input cannot be undone.',
        jsonSchemaError: 'typert tool schema: unsupported void value',
      }
```

### `cuaAccess/systemStatus`  (package: `dsh-cua`)

描述：Inspect system privileges independently of application decisions and icons.

参数：

结果：Platform-tagged access observations and native process metadata.

```json
{
 "oneOf": [
  {
   "type": "object",
   "properties": {
    "platform": {
     "type": "string",
     "const": "darwin"
    },
    "accessibility": {
     "type": "boolean"
    },
    "screenRecording": {
     "type": "boolean"
    },
    "helper": {
     "type": "object",
     "properties": {
      "pid": {
       "type": "number"
      },
      "path": {
       "type": "string"
      },
      "name": {
       "type": "string"
      }
     },
     "required": [
      "pid"
     ],
     "additionalProperties": true
    },
    "parent": {
     "type": "object",
     "properties": {
      "pid": {
       "type": "number"
      },
      "path": {
       "type": "string"
      },
      "name": {
       "type": "string"
      }
     },
     "required": [
      "pid"
     ],
     "additionalProperties": true
    },
    "launchApplication": {
     "type": "object",
     "properties": {
      "name": {
       "type": "string"
      },
      "path": {
       "type": "string"
      },
      "icon": {
       "type": "string"
      }
     },
     "required": [
      "name",
      "path"
     ],
     "additionalProperties": true,
     "description": "Application macOS reports as responsible for the helper, captured at startup; absent when it cannot be identified."
    }
   },
   "required": [
    "platform",
    "accessibility",
    "screenRecording",
    "helper",
    "parent"
   ],
   "additionalProperties": true
  },
  {
   "type": "object",
   "properties": {
    "platform": {
     "type": "string",
     "const": "win32"
    },
    "sessionLocked": {
     "oneOf": [
      {
       "type": "null",
       "const": null
      },
      {
       "type": "boolean",
       "enum": [
        false,
        true
       ]
      }
     ]
    },
    "captureAvailable": {
     "type": "boolean"
    },
    "elevated": {
     "type": "boolean"
    },
    "helper": {
     "type": "object",
     "properties": {
      "pid": {
       "type": "number"
      },
      "path": {
       "type": "string"
      },
      "name": {
       "type": "string"
      }
     },
     "required": [
      "pid"
     ],
     "additionalProperties": true
    }
   },
   "required": [
    "platform",
    "sessionLocked",
    "captureAvailable",
    "elevated",
    "helper"
   ],
   "additionalProperties": true
  }
 ]
}
```

### `forgePanel/changeRequestCommits`  (package: `dsh-experimental-client-ui-forge`)

描述：List the commits of a change request of a Session at its current head, which the changes view can show one or several of instead of the whole change request.

参数：
- `[0]` **session**（source=lookup, wire=sessionId）—— Session the change request is bound to.
  - schema: `{"type": "string"}`
- `[1]` **url**（source=json）—— binding URL of the change request.
  - schema: `{"type": "string"}`

结果：the commits oldest first with the head they were read at, or null when the provider does not read commits.

```json
{
 "oneOf": [
  {
   "type": "null",
   "const": null
  },
  {
   "type": "object",
   "properties": {
    "headSha": {
     "type": "string",
     "description": "Head commit the list was read at."
    },
    "commits": {
     "type": "array",
     "items": {
      "type": "object",
      "properties": {
       "sha": {
        "type": "string",
        "description": "Full commit object name."
       },
       "parent": {
        "oneOf": [
         {
          "type": "null",
          "const": null
         },
         {
          "type": "string"
         }
        ],
        "description": "Full object name of the first parent, or null for a root commit."
       },
       "title": {
        "type": "string",
        "description": "First line of the commit message."
       },
       "author": {
        "oneOf": [
         {
          "type": "null",
          "const": null
         },
         {
          "type": "string"
         }
        ],
        "description": "Author name or login as the host reports it, or null when it reports none."
       },
       "authoredAt": {
        "type": "string",
        "description": "ISO 8601 time the commit was authored."
       }
      },
      "required": [
       "sha",
       "parent",
       "title",
       "author",
       "authoredAt"
      ],
      "additionalProperties": true
     },
     "description": "Commits oldest first, ending with the head commit unless `truncated`."
    },
    "truncated": {
     "type": "boolean",
     "description": "Whether the change request has more commits than the provider lists; the listed ones are the oldest."
    }
   },
   "required": [
    "headSha",
    "commits",
    "truncated"
   ],
   "additionalProperties": true
  }
 ]
}
```

### `forgePanel/changeRequestReview`  (package: `dsh-experimental-client-ui-forge`)

描述：Read the review threads of a change request of a Session at the head commit of its displayed diff. Threads read at the same head commit and update time are reused from memory, for any Session, until they are `reviewRefreshMs` old, `maxCachedReviewBytes` evicts them, or a review write through this panel drops them; a read again after that age, or a `fresh` read, shows threads resolved elsewhere since, which do not change a GitHub pull request's update time.

参数：
- `[0]` **session**（source=lookup, wire=sessionId）—— Session the change request is bound to.
  - schema: `{"type": "string"}`
- `[1]` **url**（source=json）—— binding URL of the change request.
  - schema: `{"type": "string"}`
- `[2]` **headSha**（source=json）—— head commit of the displayed diff.
  - schema: `{"type": "string"}`
- `[3]` **fresh**（source=json）—— whether to read from the host even when threads at this head and update time are kept, as after the reader refreshes the view; the result replaces the kept threads.
  - schema: `{"type": "boolean"}`

结果：the threads at `headSha`, with `current` positions numbering the files of that diff.

```json
{
 "type": "object",
 "properties": {
  "headSha": {
   "type": "string",
   "description": "Head commit the threads were read at; `current` positions number its files."
  },
  "viewer": {
   "oneOf": [
    {
     "type": "null",
     "const": null
    },
    {
     "type": "string"
    }
   ],
   "description": "Login of the signed-in user the provider read as, or null when the host does not name one."
  },
  "threads": {
   "type": "array",
   "items": {
    "type": "object",
    "properties": {
     "id": {
      "type": "string",
      "description": "Host identity of the thread."
     },
     "path": {
      "type": "string",
      "description": "Repository-relative path the thread is on."
     },
     "side": {
      "type": "string",
      "enum": [
       "L",
       "R"
      ],
      "description": "Old (`L`) or new (`R`) side of the diff the lines number."
     },
     "subject": {
      "type": "string",
      "enum": [
       "file",
       "line"
      ],
      "description": "Whether the thread is on lines or on the whole file."
     },
     "current": {
      "oneOf": [
       {
        "type": "null",
        "const": null
       },
       {
        "type": "object",
        "properties": {
         "commit": {
          "type": "string",
          "description": "Full object name of the commit the diff's new side shows: new-side (`R`) lines number this commit's file, and old-side (`L`) lines number the file at the diff's base."
         },
         "startLine": {
          "type": "number",
          "description": "First line, inclusive."
         },
         "endLine": {
          "type": "number",
          "description": "Last line, inclusive."
         }
        },
        "required": [
         "commit",
         "startLine",
         "endLine"
        ],
        "additionalProperties": true
       }
      ],
      "description": "Lines in the diff that ends at the head commit of the read, whose `commit` is that head; null for an outdated or file thread."
     },
     "original": {
      "oneOf": [
       {
        "type": "null",
        "const": null
       },
       {
        "type": "object",
        "properties": {
         "commit": {
          "type": "string",
          "description": "Full object name of the commit the diff's new side shows: new-side (`R`) lines number this commit's file, and old-side (`L`) lines number the file at the diff's base."
         },
         "startLine": {
          "type": "number",
          "description": "First line, inclusive."
         },
         "endLine": {
          "type": "number",
          "description": "Last line, inclusive."
         }
        },
        "required": [
         "commit",
         "startLine",
         "endLine"
        ],
        "additionalProperties": true
       }
      ],
      "description": "Lines in the diff that ended at the commit the thread was written on; null for a file thread."
     },
     "diffHunk": {
      "oneOf": [
       {
        "type": "null",
        "const": null
       },
       {
        "type": "string"
       }
      ],
      "description": "Diff hunk the host recorded when the thread was written, ending at its last line; null when it recorded none."
     },
     "resolved": {
      "type": "boolean",
      "description": "Whether the thread is marked resolved."
     },
     "viewerCanReply": {
      "type": "boolean",
      "description": "Whether the signed-in user can reply to the thread through `writeReview`; false for a provider without it."
     },
     "viewerCanResolve": {
      "type": "boolean",
      "description": "Whether the signed-in user can mark the thread resolved, or unresolved when `resolved`, through `writeReview`; false for a provider without it."
     },
     "comments": {
      "type": "array",
      "items": {
       "type": "object",
       "properties": {
        "id": {
         "type": "string",
         "description": "Host identity of the comment."
        },
        "author": {
         "oneOf": [
          {
           "type": "null",
           "const": null
          },
          {
           "type": "object",
           "properties": {
            "login": {
             "type": "string",
             "description": "Login as the host spells it."
            },
            "avatarUrl": {
             "oneOf": [
              {
               "type": "null",
               "const": null
              },
              {
               "type": "string"
              }
             ],
             "description": "Avatar image URL, or null when the host reports none."
            },
            "bot": {
             "type": "boolean",
             "description": "Whether the account is an app or bot rather than a person."
            }
           },
           "required": [
            "login",
            "avatarUrl",
            "bot"
           ],
           "additionalProperties": true
          }
         ],
         "description": "Author, or null when the account no longer exists."
        },
        "body": {
         "type": "string",
         "description": "Markdown source as the author wrote it; it may contain raw HTML, which readers must not render as HTML."
        },
        "createdAt": {
         "type": "string",
         "description": "ISO 8601 time the comment was created."
        },
        "pending": {
         "type": "boolean",
         "description": "Whether the comment belongs to the signed-in user's review that is not submitted yet, which only that user sees."
        },
        "url": {
         "oneOf": [
          {
           "type": "null",
           "const": null
          },
          {
           "type": "string"
          }
         ],
         "description": "Web page of the comment, or null when the host reports none."
        },
        "viewerCanEdit": {
         "type": "boolean",
         "description": "Whether the signed-in user can replace the comment's text through `writeReview`; false for a provider without it."
        },
        "viewerCanDelete": {
         "type": "boolean",
         "description": "Whether the signed-in user can delete the comment through `writeReview`; false for a provider without it."
        }
       },
       "required": [
        "id",
        "author",
        "body",
        "createdAt",
        "pending",
        "url",
        "viewerCanEdit",
        "viewerCanDelete"
       ],
       "additionalProperties": true
      },
      "description": "Comments oldest first; the first starts the thread."
     }
    },
    "required": [
     "id",
     "path",
     "side",
     "subject",
     "current",
     "original",
     "diffHunk",
     "resolved",
     "viewerCanReply",
     "viewerCanResolve",
     "comments"
    ],
    "additionalProperties": true
   },
   "description": "Threads in host order, at most the provider's limit."
  },
  "truncated": {
   "type": "boolean",
   "description": "Whether the change request has more threads than `threads` holds."
  },
  "pendingReview": {
   "oneOf": [
    {
     "type": "null",
     "const": null
    },
    {
     "type": "object",
     "properties": {
      "id": {
       "type": "string",
       "description": "Host identity of the review."
      },
      "comments": {
       "type": "number",
       "description": "Comments the review holds."
      }
     },
     "required": [
      "id",
      "comments"
     ],
     "additionalProperties": true
    }
   ],
   "description": "The signed-in user's pending review, or null without one."
  },
  "viewerCanReview": {
   "type": "boolean",
   "description": "Whether the signed-in user can add comments to a pending review and submit or discard it through `writeReview`; false for a provider without it."
  }
 },
 "required": [
  "headSha",
  "viewer",
  "threads",
  "truncated",
  "pendingReview",
  "viewerCanReview"
 ],
 "additionalProperties": true
}
```

### `forgePanel/changeRequestReviewWrite`  (package: `dsh-experimental-client-ui-forge`)

描述：Write to the review threads or the pending review of a change request of a Session while its head is still the head the user saw. The threads kept for the change request are dropped once the host answers, so the next read shows the write, which a resolve does not show through a GitHub pull request's update time.

参数：
- `[0]` **session**（source=lookup, wire=sessionId）—— Session the change request is bound to.
  - schema: `{"type": "string"}`
- `[1]` **url**（source=json）—— binding URL of the change request.
  - schema: `{"type": "string"}`
- `[2]` **write**（source=json）—— the write and the head commit of the threads the user saw.
  - schema: `{"oneOf": [{"type": "object", "properties": {"headSha": {"type": "string", "description": "Head commit the user saw; providers check it before writing and refuse with `stale` once the head is another commit."}, "kind": {"type": "string", "const": "reply"}, "thread": {"type": "string"}, "body": {"type": "string"}, "pending": {"type": "boolean"}}, "required": ["headSha", "kind", "thread", "body", "pending"], "additionalProperties": true}, {"type": "object", "properties": {"headSha": {"type": "string", "description": "Head commit the user saw; providers check it before writing and refuse with `stale` once the head is another commit."}, "kind": {"type": "string", "const": "resolve"}, "thread": {"type": "string"}, "resolved": {"type": "boolean"}}, "required": ["headSha", "kind", "thread", "resolved"], "additionalProperties": true}, {"type": "object", "properties": {"headSha": {"type": "string", "description": "Head commit the user saw; providers check it before writing and refuse with `stale` once the head is another commit."}, "kind": {"type": "string", "const": "edit-comment"}, "comment": {"type": "string"}, "body": {"type": "string"}}, "required": ["headSha", "kind", "comment", "body"], "additionalProperties": true}, {"type": "object", "properties": {"headSha": {"type": "string", "description": "Head commit the user saw; providers check it before writing and refuse with `stale` once the head is another commit."}, "kind": {"type": "string", "const": "delete-comment"}, "comment": {"type": "string"}}, "required": ["headSha", "kind", "comment"], "additionalProperties": true}, {"type": "object", "properties": {"headSha": {"type": "string", "description": "Head commit the user saw; providers check it before writing and refuse with `stale` once the head is another commit."}, "kind": {"type": "string", "const": "add-pending"}, "comment": {"type": "object", "properties": {"path": {"type": "string"}, "side": {"type": "string", "enum": ["L", "R"]}, "startLine": {"type": "number"}, "endLine": {"type": "number"}, "body": {"type": "string"}}, "required": ["path", "side", "startLine", "endLine", "body"], "additionalProperties": true}}, "required": ["headSha", "kind", "comment"], "additionalProperties": true}, {"type": "object", "properties": {"headSha": {"type": "string", "description": "Head commit the user saw; providers check it before writing and refuse with `stale` once the head is another commit."}, "kind": {"type": "string", "const": "submit-pending"}, "review": {"type": "string"}, "event": {"type": "string", "enum": ["comment", "approve", "request-changes"]}, "body": {"type": "string"}}, "required": ["headSha", "kind", "review", "event", "body"], "additionalProperties": true}, {"type": "object", "properties": {"headSha": {"type": "string", "description": "Head commit the user saw; providers check it before writing and refuse with `stale` once the head is another commit."}, "kind": {"type": "string", "const": "discard-pending"}, "review": {"type": "string"}}, "required": ["headSha", "kind", "review"], "additionalProperties": true}]}`

结果：whether the host took the write; `stale` once the head moved, which polls the change request again, and `unknown` when the outcome must be checked on the host before the write is tried again.

```json
{
 "oneOf": [
  {
   "type": "object",
   "properties": {
    "ok": {
     "type": "boolean",
     "const": true
    }
   },
   "required": [
    "ok"
   ],
   "additionalProperties": true
  },
  {
   "type": "object",
   "properties": {
    "ok": {
     "type": "boolean",
     "const": false
    },
    "code": {
     "type": "string",
     "enum": [
      "failed",
      "unknown",
      "stale",
      "unsupported"
     ]
    },
    "message": {
     "type": "string"
    }
   },
   "required": [
    "ok",
    "code",
    "message"
   ],
   "additionalProperties": true
  }
 ]
}
```

### `forgePanel/changeRequestViewed`  (package: `dsh-experimental-client-ui-forge`)

描述：Read the signed-in user's viewed marks on the files of a change request of a Session, which the host keeps for the whole change request.

参数：
- `[0]` **session**（source=lookup, wire=sessionId）—— Session the change request is bound to.
  - schema: `{"type": "string"}`
- `[1]` **url**（source=json）—— binding URL of the change request.
  - schema: `{"type": "string"}`
- `[2]` **headSha**（source=json）—— head commit of the shown diff; marks read at another head are refused.
  - schema: `{"type": "string"}`

结果：the marks at `headSha`, or null when the provider keeps no viewed marks.

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-experimental-client-ui-forge#forgePanel/changeRequestViewed:result',
        create: _deepseek_ai_dsh_experimental_client_ui_forge_forgePanel_changeRequestViewed_result$schema,
        description: 'the marks at `headSha`, or null when the provider keeps no viewed marks.',
        jsonSchemaError: 'typert tool schema: selected dictionaries require string keys and unconstrained JSON values',
      }
```

### `forgePanel/diffBases`  (package: `dsh-experimental-client-ui-forge`)

描述：List the branches the `branch` scope of the Session's current checkout can compare with.

参数：
- `[0]` **session**（source=lookup, wire=sessionId）—— Session whose checkout is read.
  - schema: `{"type": "string"}`
- `[1]` **query**（source=json）—— text the branch names must contain, ignoring case; empty for every branch.
  - schema: `{"type": "string"}`

结果：the automatic fork point and at most `maxBranches` remote-tracking and local branches each, most recently committed first, without the checked-out branch.

```json
{
 "type": "object",
 "properties": {
  "automatic": {
   "oneOf": [
    {
     "type": "null",
     "const": null
    },
    {
     "type": "object",
     "properties": {
      "name": {
       "type": "string",
       "description": "The created worktree's base branch or abbreviated base commit, or the default branch of the code host remote."
      },
      "worktree": {
       "type": "boolean",
       "description": "Whether the fork point is the base commit of the worktree the Session created rather than a merge base."
      }
     },
     "required": [
      "name",
      "worktree"
     ],
     "additionalProperties": true
    }
   ],
   "description": "The fork point used while no base branch is chosen, whatever the query; null without one."
  },
  "remote": {
   "type": "array",
   "items": {
    "type": "object",
    "properties": {
     "ref": {
      "type": "string",
      "description": "Full ref name: `refs/heads/<branch>` or `refs/remotes/<remote>/<branch>`."
     },
     "name": {
      "type": "string",
      "description": "The ref without `refs/heads/` or `refs/remotes/`, such as `master` or `origin/master`."
     }
    },
    "required": [
     "ref",
     "name"
    ],
    "additionalProperties": true
   },
   "description": "Remote-tracking branches without symbolic refs such as `origin/HEAD`, most recently committed first, at most `maxBranches`."
  },
  "local": {
   "type": "array",
   "items": {
    "type": "object",
    "properties": {
     "ref": {
      "type": "string",
      "description": "Full ref name: `refs/heads/<branch>` or `refs/remotes/<remote>/<branch>`."
     },
     "name": {
      "type": "string",
      "description": "The ref without `refs/heads/` or `refs/remotes/`, such as `master` or `origin/master`."
     }
    },
    "required": [
     "ref",
     "name"
    ],
    "additionalProperties": true
   },
   "description": "Local branches other than the checked-out one, most recently committed first, at most `maxBranches`."
  }
 },
 "required": [
  "automatic",
  "remote",
  "local"
 ],
 "additionalProperties": true
}
```

### `forgePanel/localCommits`  (package: `dsh-experimental-client-ui-forge`)

描述：List the commits of the `branch` scope of the Session's current checkout, which the changes view can show one or several of instead of the whole scope.

参数：
- `[0]` **session**（source=lookup, wire=sessionId）—— Session whose checkout is read.
  - schema: `{"type": "string"}`
- `[1]` **base**（source=json）—— the base ref of the `branch` scope, or null for its automatic fork point.
  - schema: `{"oneOf": [{"type": "null", "const": null}, {"type": "string"}]}`

结果：the newest `maxCommits` commits between the fork point and HEAD, oldest first, and HEAD's commit.

```json
{
 "type": "object",
 "properties": {
  "headSha": {
   "type": "string",
   "description": "HEAD commit the list was read at."
  },
  "commits": {
   "type": "array",
   "items": {
    "type": "object",
    "properties": {
     "sha": {
      "type": "string",
      "description": "Full commit object name."
     },
     "parent": {
      "oneOf": [
       {
        "type": "null",
        "const": null
       },
       {
        "type": "string"
       }
      ],
      "description": "Full object name of the first parent, or null for a root commit."
     },
     "title": {
      "type": "string",
      "description": "First line of the commit message."
     },
     "author": {
      "oneOf": [
       {
        "type": "null",
        "const": null
       },
       {
        "type": "string"
       }
      ],
      "description": "Author name or login as the host reports it, or null when it reports none."
     },
     "authoredAt": {
      "type": "string",
      "description": "ISO 8601 time the commit was authored."
     }
    },
    "required": [
     "sha",
     "parent",
     "title",
     "author",
     "authoredAt"
    ],
    "additionalProperties": true
   },
   "description": "Commits oldest first, ending with HEAD's commit."
  },
  "truncated": {
   "type": "boolean",
   "description": "Whether the branch has more commits than `maxCommits`; the listed ones are the newest."
  }
 },
 "required": [
  "headSha",
  "commits",
  "truncated"
 ],
 "additionalProperties": true
}
```

### `forgePanel/requestReviews`  (package: `dsh-experimental-client-ui-forge`)

描述：Request reviews of a change request of a Session from more reviewers; the change request is polled again once the host answers.

参数：
- `[0]` **session**（source=lookup, wire=sessionId）—— Session the change request is bound to.
  - schema: `{"type": "string"}`
- `[1]` **url**（source=json）—— binding URL of the change request.
  - schema: `{"type": "string"}`
- `[2]` **reviewers**（source=json）—— the reviewers to ask, by kind and login.
  - schema: `{"type": "array", "items": {"type": "object", "properties": {"kind": {"type": "string", "enum": ["user", "bot", "team"], "description": "Whether the reviewer is a person's account, an app or bot account, or a team."}, "login": {"type": "string", "description": "Login, or `organization/slug` for a team."}}, "required": ["kind", "login"], "additionalProperties": true}}`

结果：

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-experimental-client-ui-forge#forgePanel/requestReviews:result',
        create: _deepseek_ai_dsh_experimental_client_ui_forge_forgePanel_requestReviews_result$schema,
        jsonSchemaError: 'typert tool schema: unsupported void value',
      }
```

### `forgePanel/rerunChecks`  (package: `dsh-experimental-client-ui-forge`)

描述：Rerun the failed checks of a change request of a Session while its head is still the commit whose checks the user saw; the change request is polled again once the host answers.

参数：
- `[0]` **session**（source=lookup, wire=sessionId）—— Session the change request is bound to.
  - schema: `{"type": "string"}`
- `[1]` **url**（source=json）—— binding URL of the change request.
  - schema: `{"type": "string"}`
- `[2]` **request**（source=json）—— the head commit whose checks the user saw.
  - schema: `{"type": "object", "properties": {"headSha": {"type": "string", "description": "Head commit whose checks the user saw; the provider refuses with `stale` once the head is another commit."}}, "required": ["headSha"], "additionalProperties": true}`

结果：

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-experimental-client-ui-forge#forgePanel/rerunChecks:result',
        create: _deepseek_ai_dsh_experimental_client_ui_forge_forgePanel_rerunChecks_result$schema,
        jsonSchemaError: 'typert tool schema: unsupported void value',
      }
```

### `forgePanel/reviewerCandidates`  (package: `dsh-experimental-client-ui-forge`)

描述：List accounts a review of a change request of a Session can be requested from.

参数：
- `[0]` **session**（source=lookup, wire=sessionId）—— Session the change request is bound to.
  - schema: `{"type": "string"}`
- `[1]` **url**（source=json）—— binding URL of the change request.
  - schema: `{"type": "string"}`
- `[2]` **query**（source=json）—— text the login or name contains; empty for the host's suggestions and first accounts.
  - schema: `{"type": "string"}`

结果：the accounts, the host's suggestions first.

```json
{
 "type": "array",
 "items": {
  "type": "object",
  "properties": {
   "login": {
    "type": "string",
    "description": "Login as the host spells it."
   },
   "name": {
    "oneOf": [
     {
      "type": "null",
      "const": null
     },
     {
      "type": "string"
     }
    ],
    "description": "Display name, or null when the account shows none."
   },
   "avatarUrl": {
    "oneOf": [
     {
      "type": "null",
      "const": null
     },
     {
      "type": "string"
     }
    ],
    "description": "Avatar image URL, or null when the host reports none."
   },
   "suggested": {
    "type": "boolean",
    "description": "Whether the host suggests the account for this change request, as from the history of its changed files."
   }
  },
  "required": [
   "login",
   "name",
   "avatarUrl",
   "suggested"
  ],
  "additionalProperties": true
 }
}
```

### `forgePanel/setFileViewed`  (package: `dsh-experimental-client-ui-forge`)

描述：Set or clear the signed-in user's viewed mark on one file of a change request of a Session.

参数：
- `[0]` **session**（source=lookup, wire=sessionId）—— Session the change request is bound to.
  - schema: `{"type": "string"}`
- `[1]` **url**（source=json）—— binding URL of the change request.
  - schema: `{"type": "string"}`
- `[2]` **request**（source=json）—— the file, its mark, and the head commit of the shown diff.
  - schema: `{"type": "object", "properties": {"path": {"type": "string", "description": "Repository-relative path of the changed file."}, "viewed": {"type": "boolean", "description": "Whether the file is viewed."}, "headSha": {"type": "string", "description": "Head commit whose changes the user saw; the provider refuses with `stale` once the head is another commit."}}, "required": ["path", "viewed", "headSha"], "additionalProperties": true}`

结果：

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-experimental-client-ui-forge#forgePanel/setFileViewed:result',
        create: _deepseek_ai_dsh_experimental_client_ui_forge_forgePanel_setFileViewed_result$schema,
        jsonSchemaError: 'typert tool schema: unsupported void value',
      }
```

### `forgePanel/updateBranch`  (package: `dsh-experimental-client-ui-forge`)

描述：Bring the base branch's new commits into the head branch of a change request of a Session while its head is still `headSha`; the change request is polled again once the host answers.

参数：
- `[0]` **session**（source=lookup, wire=sessionId）—— Session the change request is bound to.
  - schema: `{"type": "string"}`
- `[1]` **url**（source=json）—— binding URL of the change request.
  - schema: `{"type": "string"}`
- `[2]` **method**（source=json）—— merge commit or rebase.
  - schema: `{"type": "string", "enum": ["merge", "rebase"]}`
- `[3]` **headSha**（source=json）—— head commit the user confirmed.
  - schema: `{"type": "string"}`

结果：

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-experimental-client-ui-forge#forgePanel/updateBranch:result',
        create: _deepseek_ai_dsh_experimental_client_ui_forge_forgePanel_updateBranch_result$schema,
        jsonSchemaError: 'typert tool schema: unsupported void value',
      }
```

### `forgePanel/worktreeCleanup`  (package: `dsh-experimental-client-ui-forge`)

描述：Read or await the worktree service's archive cleanup of the Session's checkout.

参数：
- `[0]` **session**（source=lookup, wire=sessionId）—— Session whose cleanup is visible to the caller.
  - schema: `{"type": "string"}`
- `[1]` **revision**（source=json）—— null reads now; a matching revision waits for completion.
  - schema: `{"oneOf": [{"type": "null", "const": null}, {"type": "number"}]}`

结果：the cleanup state, or null without a worktree service, an attempt, or a current revision.

```json
{
 "oneOf": [
  {
   "type": "null",
   "const": null
  },
  {
   "type": "object",
   "properties": {
    "revision": {
     "type": "number",
     "description": "Monotonic attempt number within this Host lifetime; a Remote waits on a specific one."
    },
    "status": {
     "type": "string",
     "enum": [
      "failed",
      "retained",
      "scheduled",
      "removed"
     ],
     "description": "`scheduled` until the background inspection ends; `retained` when a use, policy, or Git state keeps the checkout; `removed` after `git worktree remove`; `failed` when removal threw and may have left a partial directory."
    }
   },
   "required": [
    "revision",
    "status"
   ],
   "additionalProperties": true
  }
 ]
}
```

### `liveshareShares/addSessions`  (package: `dsh-experimental-liveshare`)

描述：Add source Sessions to one share this host already publishes.

参数：
- `[0]` **request**（source=json）—— Share and the Sessions to add.
  - schema: `{"type": "object", "properties": {"shareId": {"type": "string"}, "sessionIds": {"type": "array", "items": {"type": "string"}, "description": "Sessions to add; ids already served by the share are ignored."}}, "required": ["shareId", "sessionIds"], "additionalProperties": true}`

结果：The source Sessions the share now serves.

```json
{
 "type": "object",
 "properties": {
  "shareId": {
   "type": "string"
  },
  "sessionIds": {
   "type": "array",
   "items": {
    "type": "string"
   }
  }
 },
 "required": [
  "shareId",
  "sessionIds"
 ],
 "additionalProperties": true
}
```

### `mcpManager/probe`  (package: `dsh-mcp-manager`)

描述：Probe one server for a management surface; see {@link McpServerManager.probe}.

参数：
- `[0]` **name**（source=json）—— the configured or registered server key.
  - schema: `{"type": "string"}`

结果：the stage-classified probe outcome and independent cleanup result.

```json
{
 "oneOf": [
  {
   "type": "object",
   "properties": {
    "ok": {
     "type": "boolean",
     "const": true
    },
    "serverInfo": {
     "type": "object",
     "properties": {
      "name": {
       "type": "string"
      },
      "version": {
       "type": "string"
      }
     },
     "required": [
      "name",
      "version"
     ],
     "additionalProperties": true,
     "description": "Server-reported identity from `initialize`, when the SDK captured one."
    },
    "toolNames": {
     "type": "array",
     "items": {
      "type": "string"
     },
     "description": "Advertised raw tool names, capped at the resolved `maxToolNames`."
    },
    "truncated": {
     "type": "boolean",
     "description": "Whether the advertised list was longer than the returned `toolNames`."
    },
    "durationMs": {
     "type": "number",
     "description": "Wall-clock duration of negotiation and listing, excluding cleanup, in milliseconds."
    },
    "closeConfirmed": {
     "type": "boolean",
     "description": "Transport closure is confirmed, or no transport was created; unfinished negotiation remains unconfirmed."
    },
    "cleanupError": {
     "type": "string",
     "description": "Cleanup failed or exceeded its budget, including after confirmed transport closure."
    }
   },
   "required": [
    "ok",
    "toolNames",
    "truncated",
    "durationMs",
    "closeConfirmed"
   ],
   "additionalProperties": true
  },
  {
   "type": "object",
   "properties": {
    "ok": {
     "type": "boolean",
     "const": false
    },
    "stage": {
     "type": "string",
     "enum": [
      "connect",
      "list"
     ]
    },
    "message": {
     "type": "string",
     "description": "Diagnostic text of the failure."
    },
    "durationMs": {
     "type": "number",
     "description": "Wall-clock duration until the failure in milliseconds."
    },
    "closeConfirmed": {
     "type": "boolean",
     "description": "Transport closure is confirmed, or no transport was created; unfinished negotiation remains unconfirmed."
    },
    "cleanupError": {
     "type": "string",
     "description": "Cleanup failed or exceeded its budget, including after confirmed transport closure."
    }
   },
   "required": [
    "ok",
    "stage",
    "message",
    "durationMs",
    "closeConfirmed"
   ],
   "additionalProperties": true
  }
 ]
}
```

### `mcpManager/restart`  (package: `dsh-mcp-manager`)

描述：Rebuild one server from its current entry: dispose the live instance (if any) and mount a fresh one with a fresh reconnect budget. Incomplete cleanup blocks replacement until the Host restarts.

参数：
- `[0]` **name**（source=json）—— the configured or registered server key.
  - schema: `{"type": "string"}`

结果：

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-mcp-manager#mcpManager/restart:result',
        create: _deepseek_ai_dsh_mcp_manager_mcpManager_restart_result$schema,
        jsonSchemaError: 'typert tool schema: unsupported void value',
      }
```

### `mcpManager/watch`  (package: `dsh-mcp-manager`)

描述：Stream server views for a management surface: the current list on subscription, then the complete list again after each status or entry change.

参数：

结果：coalesced server lists until the subscriber or the manager stops.

```json
{
 "type": "array",
 "items": {
  "type": "object",
  "properties": {
   "name": {
    "type": "string",
    "description": "The configured `servers` key or registered name; also the instance's `serverName` tool namespace."
   },
   "configured": {
    "type": "boolean",
    "description": "Whether a configured or registered entry still exists; false for retained cleanup failures after removal."
   },
   "status": {
    "oneOf": [
     {
      "type": "object",
      "properties": {
       "state": {
        "type": "string",
        "const": "closed"
       },
       "result": {
        "type": "object",
        "properties": {
         "closeConfirmed": {
          "type": "boolean",
          "description": "Transport closure is confirmed, or no transport was created; unfinished negotiation remains unconfirmed."
         },
         "cleanupError": {
          "type": "string",
          "description": "Cleanup failed or exceeded its budget, including after confirmed transport closure."
         }
        },
        "required": [
         "closeConfirmed"
        ],
        "additionalProperties": true
       }
      },
      "required": [
       "state",
       "result"
      ],
      "additionalProperties": true
     },
     {
      "type": "object",
      "properties": {
       "state": {
        "type": "string",
        "const": "waiting-credentials"
       },
       "lastError": {
        "type": "string"
       }
      },
      "required": [
       "state",
       "lastError"
      ],
      "additionalProperties": true
     },
     {
      "type": "object",
      "properties": {
       "state": {
        "type": "string",
        "const": "connecting",
        "description": "A connection attempt is in flight."
       },
       "attempt": {
        "type": "number",
        "description": "Consecutive failed attempts in the current outage before this one; `0` on the first-ever attempt."
       }
      },
      "required": [
       "state",
       "attempt"
      ],
      "additionalProperties": true
     },
     {
      "type": "object",
      "properties": {
       "state": {
        "type": "string",
        "const": "connected",
        "description": "Connected with the current tool generation registered."
       },
       "toolCount": {
        "type": "number",
        "description": "Registered tools in the live generation."
       },
       "connectedAt": {
        "type": "number",
        "description": "Epoch milliseconds when connect plus initial tool sync completed."
       }
      },
      "required": [
       "state",
       "toolCount",
       "connectedAt"
      ],
      "additionalProperties": true
     },
     {
      "type": "object",
      "properties": {
       "state": {
        "type": "string",
        "const": "reconnecting",
        "description": "Waiting out the backoff delay before the next reconnect attempt."
       },
       "attempt": {
        "type": "number",
        "description": "Consecutive failed attempts in the current outage."
       },
       "maxAttempts": {
        "type": "number",
        "description": "Attempt budget per outage from the resolved reconnect policy."
       },
       "nextRetryAtMs": {
        "type": "number",
        "description": "Epoch milliseconds when the next attempt fires."
       },
       "lastError": {
        "type": "string",
        "description": "Most recent connection failure, as diagnostic text."
       }
      },
      "required": [
       "state",
       "attempt",
       "maxAttempts",
       "nextRetryAtMs",
       "lastError"
      ],
      "additionalProperties": true
     },
     {
      "type": "object",
      "properties": {
       "state": {
        "type": "string",
        "const": "failed",
        "description": "The supervisor stopped automatic attempts. `gave-up` exhausted the per-outage attempt budget and unregistered the tools; `reconnect-disabled` lost (or never made) the connection with reconnect off; `unclosed-generation` blocks replacement because transport closure or local cleanup is incomplete."
       },
       "reason": {
        "type": "string",
        "enum": [
         "gave-up",
         "reconnect-disabled",
         "unclosed-generation"
        ],
        "description": "Which supervisor stop produced this terminal state."
       },
       "lastError": {
        "type": "string",
        "description": "Most recent connection failure, when one was observed."
       }
      },
      "required": [
       "state",
       "reason"
      ],
      "additionalProperties": true
     },
     {
      "type": "object",
      "properties": {
       "state": {
        "type": "string",
        "const": "disabled"
       }
      },
      "required": [
       "state"
      ],
      "additionalProperties": true
     },
     {
      "type": "object",
      "properties": {
       "state": {
        "type": "string",
        "const": "failed"
       },
       "reason": {
        "type": "string",
        "const": "load"
       },
       "lastError": {
        "type": "string"
       }
      },
      "required": [
       "state",
       "reason",
       "lastError"
      ],
      "additionalProperties": true
     },
     {
      "type": "object",
      "properties": {
       "state": {
        "type": "string",
        "const": "failed"
       },
       "reason": {
        "type": "string",
        "const": "cleanup"
       },
       "result": {
        "type": "object",
        "properties": {
         "closeConfirmed": {
          "type": "boolean",
          "description": "Transport closure is confirmed, or no transport was created; unfinished negotiation remains unconfirmed."
         },
         "cleanupError": {
          "type": "string",
          "description": "Cleanup failed or exceeded its budget, including after confirmed transport closure."
         }
        },
        "required": [
         "closeConfirmed"
        ],
        "additionalProperties": true
       }
      },
      "required": [
       "state",
       "reason",
       "result"
      ],
      "additionalProperties": true
     }
    ],
    "description": "Current runtime status."
   }
  },
  "required": [
   "name",
   "configured",
   "status"
  ],
  "additionalProperties": true
 }
}
```

### `officeKit/generation`  (package: `dsh-office-kit`)

描述：Read the current rendering generation before reusing a Client PDF.

参数：

结果：provider lifetime, replaced with rendering, font, or engine configuration.

```json
{
 "type": "string"
}
```

### `officeKit/render`  (package: `dsh-office-kit`)

描述：Read and convert one Office file using the Session's ordinary filesystem authorization.

参数：
- `[0]` **workspaceFileScope**（source=lookup, wire=workspaceFileScopeId）—— Session header lookup shared with workspaceFiles.
  - schema: `{"type": "string"}`
- `[1]` **path**（source=json）—— absolute or workspace-relative Office path.
  - schema: `{"type": "string"}`
- `[2]` **priority**（source=json）—— foreground preview or speculative background work.
  - schema: `{"type": "string", "enum": ["foreground", "background"]}`

结果：complete PDF bytes with original source identity and missing font families.

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-office-kit/types#RenderedDocumentBytes',
        create: _deepseek_ai_dsh_office_kit_officeKit_render_result$schema,
        description: 'complete PDF bytes with original source identity and missing font families.',
        jsonSchemaError: 'typert tool schema: unsupported selected reference Uint8Array',
        decode: value => _deepseek_ai_dsh_office_kit_officeKit_render_result$schema().parse(value),
        encode: (value, writeBytes) => $encode0(value, writeBytes, [], new Map()),
      }
```

### `subagents/interruptByUser`  (package: `dsh-subagent`)

描述：Remote face of {@link interrupt}, authorized on the target Session by the Gateway. No catalog, history, persistence, or parent Agent lookup runs. Absent, idle, and already-completed targets are accepted no-ops there.

参数：
- `[0]` **childSessionId**（source=json）—— durable child session id to interrupt.
  - schema: `{"type": "string"}`

结果：acknowledgement that the cancel signal was admitted, not that the target is quiescent.

```json
{
 "type": "object",
 "properties": {
  "accepted": {
   "type": "boolean",
   "const": true
  }
 },
 "required": [
  "accepted"
 ],
 "additionalProperties": true
}
```

### `terminal/watch`  (package: `dsh-api-terminal-controller`)

描述：Stream the Session's retained terminals as whole sets, without resolving or activating an Agent.

参数：
- `[0]` **sessionId**（source=json）—— watched Session identity.
  - schema: `{"type": "string"}`

结果：the current set, then a new set after creation, removal or any published state change.

```json
{
 "type": "array",
 "items": {
  "type": "object",
  "properties": {
   "id": {
    "type": "string"
   },
   "title": {
    "type": "string"
   },
   "shell": {
    "type": "object",
    "properties": {
     "path": {
      "type": "string"
     },
     "args": {
      "type": "array",
      "items": {
       "type": "string"
      }
     },
     "name": {
      "type": "string"
     }
    },
    "required": [
     "path",
     "args",
     "name"
    ],
    "additionalProperties": true
   },
   "cwd": {
    "type": "string",
    "description": "Initial working directory; shell directory changes do not update this field."
   },
   "cols": {
    "type": "number"
   },
   "rows": {
    "type": "number"
   },
   "state": {
    "type": "string",
    "enum": [
     "running",
     "failed",
     "exited"
    ]
   },
   "exitCode": {
    "oneOf": [
     {
      "type": "null",
      "const": null
     },
     {
      "type": "number"
     }
    ]
   },
   "error": {
    "type": "string"
   },
   "origin": {
    "type": "string",
    "enum": [
     "user",
     "agent"
    ],
    "description": "Party that created the terminal."
   },
   "control": {
    "type": "string",
    "enum": [
     "user",
     "agent"
    ],
    "description": "Input owner; an agent terminal passes to the user on the user's first typed input and never returns."
   },
   "sandboxMode": {
    "type": "string",
    "enum": [
     "read-only",
     "workspace-write",
     "danger-full-access"
    ],
    "description": "Sandbox mode an agent terminal started under; user terminals run with system-user permissions and omit it."
   }
  },
  "required": [
   "id",
   "title",
   "shell",
   "cwd",
   "cols",
   "rows",
   "state",
   "exitCode",
   "origin",
   "control"
  ],
  "additionalProperties": true
 }
}
```

### `web/listProviders`  (package: `dsh-web`)

描述：List every registered search and fetch provider for provider pickers. Availability does not filter the lists.

参数：

结果：detached `{ id, title, description }` entries per capability, in registration order.

```js
{
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-web/types#WebProviderList',
        create: _deepseek_ai_dsh_web_web_listProviders_result$schema,
        description: 'detached `{ id, title, description }` entries per capability, in registration order.',
        jsonSchemaError: 'typert tool schema: selected dictionaries require string keys and unconstrained JSON values',
      }
```

### `workspaceFiles/search`  (package: `dsh-api-workspace-files`)

描述：Search files and their inferred ancestor directories with packaged ripgrep. Native ignore rules apply unless disabled; VCS metadata, symlinks and empty directories are absent. Within the bounded set of listed files, matches sort by depth, then workspace-relative path.

参数：
- `[0]` **workspaceFileScope**（source=lookup, wire=workspaceFileScopeId）—— header-derived workspace root for the Session identity on the wire.
  - schema: `{"type": "string"}`
- `[1]` **request**（source=json）—— the query, literal exclusions, and hidden/ignore switches.
  - schema: `{"type": "object", "properties": {"query": {"type": "string", "description": "Case-insensitive substring to find. Surrounding whitespace is trimmed, and an empty result is refused. Without `/` it matches entry names; with `/` it matches `/`-joined workspace-relative paths."}, "excludedPaths": {"type": "array", "items": {"type": "string"}, "description": "Literal exclusions passed as escaped rg globs. A value without `/` excludes that name at any depth; a value with `/` excludes the exact path rooted at the workspace. Glob metacharacters in names are not interpreted as patterns."}, "includeHidden": {"type": "boolean", "description": "Whether rg includes hidden files and directories; VCS metadata remains excluded."}, "respectGitignore": {"type": "boolean", "description": "Whether native rg ignore rules apply, including repository detection, nested and parent `.gitignore`, `.git/info/exclude`, global Git excludes and `.ignore`. The service does not read `core.ignorecase`; false passes `--no-ignore`."}}, "required": ["query", "excludedPaths", "includeHidden", "respectGitignore"], "additionalProperties": true}`

结果：matches capped by search.maxResults; incomplete marks excess rg files or exit 2.

```json
{
 "type": "object",
 "properties": {
  "matches": {
   "type": "array",
   "items": {
    "type": "object",
    "properties": {
     "path": {
      "type": "string",
      "description": "`/`-joined path relative to the workspace root, never empty."
     },
     "type": {
      "type": "string",
      "enum": [
       "file",
       "directory",
       "other"
      ],
      "description": "What the entry resolves to, as {@link WorkspaceDirectoryEntry.type} reports it."
     }
    },
    "required": [
     "path",
     "type"
    ],
    "additionalProperties": true
   },
   "description": "Matching files and inferred ancestor directories from the bounded set of listed files, sorted by depth then workspace-relative path and cut to the configured result cap."
  },
  "truncated": {
   "type": "boolean",
   "description": "Whether matches exceeded the result cap after scanning the bounded file set."
  },
  "incomplete": {
   "type": "boolean",
   "description": "Whether rg listed more files than the file cap and was terminated early, or exited with code 2 to report partial discovery."
  }
 },
 "required": [
  "matches",
  "truncated",
  "incomplete"
 ],
 "additionalProperties": true
}
```

## 2. 删除方法（5）

- `account/ackBonusNotified`（原 package `dsh-api-account-controller`）：Record one displayed bonus as notified for the account it belongs to.
- `account/getUnnotifiedBonuses`（原 package `dsh-api-account-controller`）：Query the granted bonuses Platform has not yet recorded as displayed.
- `officeToPdf/generation`（原 package `dsh-office-to-pdf`）：Read the current rendering generation before reusing a Client PDF.
- `officeToPdf/render`（原 package `dsh-office-to-pdf`）：Read and convert one Office file using the Session's ordinary filesystem authorization.
- `subagents/interruptByParent`（原 package `dsh-subagent`）：Remote face of {@link interrupt} under one durable parent address. No catalog, history, persistence, or parent Agent lookup runs: the core primitive alone authorizes the address against the live Activation, which is what keeps a live child interruptible while its parent Agent is offline. Absent, idle, and already-completed targets are accepted no-ops there.

## 3. 结构契约变更（32）

### `account/cancelSignIn`  (package: `dsh-deepseek-account`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[0]` | initializing | failed |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[1]` | waiting-browser | cancelled |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[2]` | exchanging | initializing |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[3]` | committing | waiting-browser |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[4]` | succeeded | exchanging |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[5]` | cancelled | committing |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[6]` | expired | succeeded |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[7]` | failed | expired |

### `account/getBalance`  (package: `dsh-deepseek-account`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/typeSymbol` | @deepseek-ai/dsh-api-account-controller#account/getBalance:result | @deepseek-ai/dsh-deepseek-account#account/getBalance:result |

### `account/getProfile`  (package: `dsh-deepseek-account`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/typeSymbol` | @deepseek-ai/dsh-api-account-controller#account/getProfile:result | @deepseek-ai/dsh-deepseek-account#account/getProfile:result |

### `account/getState`  (package: `dsh-deepseek-account`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[0]` | initializing | failed |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[1]` | waiting-browser | cancelled |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[2]` | exchanging | initializing |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[3]` | committing | waiting-browser |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[4]` | succeeded | exchanging |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[5]` | cancelled | committing |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[6]` | expired | succeeded |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[7]` | failed | expired |

### `account/hasRunningAccountTasks`  (package: `dsh-deepseek-account`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/typeSymbol` | @deepseek-ai/dsh-api-account-controller#account/hasRunningAccountTasks:result | @deepseek-ai/dsh-deepseek-account#account/hasRunningAccountTasks:result |

### `account/signOut`  (package: `dsh-deepseek-account`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[0]` | initializing | failed |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[1]` | waiting-browser | cancelled |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[2]` | exchanging | initializing |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[3]` | committing | waiting-browser |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[4]` | succeeded | exchanging |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[5]` | cancelled | committing |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[6]` | expired | succeeded |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[7]` | failed | expired |

### `account/startSignIn`  (package: `dsh-deepseek-account`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/parameters[1]/typeSymbol` | @deepseek-ai/dsh-api-account-controller#account/startSignIn:callbackOrigin | @deepseek-ai/dsh-deepseek-account#account/startSignIn:callbackOrigin |
| `/parameters[2]/typeSymbol` | @deepseek-ai/dsh-api-account-controller#account/startSignIn:loginSource | @deepseek-ai/dsh-deepseek-account#account/startSignIn:loginSource |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[0]` | initializing | failed |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[1]` | waiting-browser | cancelled |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[2]` | exchanging | initializing |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[3]` | committing | waiting-browser |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[4]` | succeeded | exchanging |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[5]` | cancelled | committing |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[6]` | expired | succeeded |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[7]` | failed | expired |

### `account/watch`  (package: `dsh-deepseek-account`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/properties/attempt/oneOf[0]/type` | null | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/additionalProperties` | true | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/authorizeUrl/type` | string | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/errorCode/enum[0]` | expired | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/errorCode/enum[1]` | no-response | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/errorCode/enum[2]` | network | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/errorCode/enum[3]` | protocol | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/errorCode/enum[4]` | storage | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/errorCode/type` | string | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/expiresAt/type` | number | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/id/type` | string | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[0]` | initializing | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[1]` | waiting-browser | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[2]` | exchanging | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[3]` | committing | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[4]` | succeeded | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[5]` | cancelled | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[6]` | expired | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/enum[7]` | failed | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/properties/phase/type` | string | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/required[0]` | id | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/required[1]` | phase | (none) |
| `/result/jsonSchema/properties/attempt/oneOf[1]/type` | object | (none) |
| `/result/jsonSchema/properties/links/additionalProperties` | true | (none) |
| `/result/jsonSchema/properties/links/properties/topUpUrl/type` | string | (none) |
| `/result/jsonSchema/properties/links/properties/usageUrl/type` | string | (none) |
| `/result/jsonSchema/properties/links/required[0]` | usageUrl | (none) |
| `/result/jsonSchema/properties/links/required[1]` | topUpUrl | (none) |
| `/result/jsonSchema/properties/links/type` | object | (none) |
| `/result/jsonSchema/properties/state/additionalProperties` | (none) | true |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[0]/type` | (none) | null |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/additionalProperties` | (none) | true |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/authorizeUrl/type` | (none) | string |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/errorCode/enum[0]` | (none) | expired |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/errorCode/enum[1]` | (none) | no-response |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/errorCode/enum[2]` | (none) | network |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/errorCode/enum[3]` | (none) | protocol |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/errorCode/enum[4]` | (none) | storage |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/errorCode/type` | (none) | string |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/expiresAt/type` | (none) | number |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/id/type` | (none) | string |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/phase/enum[0]` | (none) | failed |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/phase/enum[1]` | (none) | cancelled |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/phase/enum[2]` | (none) | initializing |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/phase/enum[3]` | (none) | waiting-browser |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/phase/enum[4]` | (none) | exchanging |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/phase/enum[5]` | (none) | committing |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/phase/enum[6]` | (none) | succeeded |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/phase/enum[7]` | (none) | expired |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/properties/phase/type` | (none) | string |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/required[0]` | (none) | id |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/required[1]` | (none) | phase |
| `/result/jsonSchema/properties/state/properties/attempt/oneOf[1]/type` | (none) | object |
| `/result/jsonSchema/properties/state/properties/links/additionalProperties` | (none) | true |
| `/result/jsonSchema/properties/state/properties/links/properties/topUpUrl/type` | (none) | string |
| `/result/jsonSchema/properties/state/properties/links/properties/usageUrl/type` | (none) | string |
| `/result/jsonSchema/properties/state/properties/links/required[0]` | (none) | usageUrl |
| `/result/jsonSchema/properties/state/properties/links/required[1]` | (none) | topUpUrl |
| `/result/jsonSchema/properties/state/properties/links/type` | (none) | object |
| `/result/jsonSchema/properties/state/properties/status/enum[0]` | (none) | signed-out |
| `/result/jsonSchema/properties/state/properties/status/enum[1]` | (none) | credential-stored |
| `/result/jsonSchema/properties/state/properties/status/type` | (none) | string |
| `/result/jsonSchema/properties/state/required[0]` | (none) | status |
| `/result/jsonSchema/properties/state/required[1]` | (none) | links |
| `/result/jsonSchema/properties/state/required[2]` | (none) | attempt |
| `/result/jsonSchema/properties/state/type` | (none) | object |
| `/result/jsonSchema/properties/status/enum[0]` | signed-out | (none) |
| `/result/jsonSchema/properties/status/enum[1]` | credential-stored | (none) |
| `/result/jsonSchema/properties/status/type` | string | (none) |
| `/result/jsonSchema/properties/type/enum[0]` | (none) | snapshot |
| `/result/jsonSchema/properties/type/enum[1]` | (none) | credentials-changed |
| `/result/jsonSchema/properties/type/type` | (none) | string |
| `/result/jsonSchema/required[0]` | status | type |
| `/result/jsonSchema/required[1]` | links | state |
| `/result/jsonSchema/required[2]` | attempt | (none) |
| `/result/typeSymbol` | @deepseek-ai/dsh-deepseek-account/types#AccountView | @deepseek-ai/dsh-deepseek-account/types#AccountUpdate |

### `account/watchExpiry`  (package: `dsh-deepseek-account`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/typeSymbol` | @deepseek-ai/dsh-api-account-controller#account/watchExpiry:result | @deepseek-ai/dsh-deepseek-account#account/watchExpiry:result |

### `dynamicCordisRunner/inventory`  (package: `dsh-cordis-host-runner`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/items/properties/latestRun/properties/client/properties/status/enum[0]` | failed | running |
| `/result/jsonSchema/items/properties/latestRun/properties/client/properties/status/enum[1]` | running | failed |
| `/result/jsonSchema/items/properties/latestRun/properties/host/properties/status/enum[0]` | failed | running |
| `/result/jsonSchema/items/properties/latestRun/properties/host/properties/status/enum[1]` | running | failed |
| `/result/jsonSchema/items/properties/latestRun/properties/status/enum[0]` | cancelled | running |
| `/result/jsonSchema/items/properties/latestRun/properties/status/enum[2]` | running | rejected |
| `/result/jsonSchema/items/properties/latestRun/properties/status/enum[3]` | rejected | awaiting-approval |
| `/result/jsonSchema/items/properties/latestRun/properties/status/enum[4]` | awaiting-approval | cancelled |

### `dynamicCordisRunner/settleUserRun`  (package: `dsh-cordis-host-runner`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/oneOf[1]/properties/reason/enum[0]` | cancelled | not-running |
| `/result/jsonSchema/oneOf[1]/properties/reason/enum[1]` | not-running | plugin-missing |
| `/result/jsonSchema/oneOf[1]/properties/reason/enum[2]` | plugin-missing | rejected |
| `/result/jsonSchema/oneOf[1]/properties/reason/enum[3]` | rejected | host-half-failed |
| `/result/jsonSchema/oneOf[1]/properties/reason/enum[4]` | host-half-failed | client-half-failed |
| `/result/jsonSchema/oneOf[1]/properties/reason/enum[5]` | client-half-failed | package-missing |
| `/result/jsonSchema/oneOf[1]/properties/reason/enum[6]` | package-missing | invalid-mode |
| `/result/jsonSchema/oneOf[1]/properties/reason/enum[7]` | invalid-mode | transition-in-flight |
| `/result/jsonSchema/oneOf[1]/properties/reason/enum[8]` | transition-in-flight | cancelled |

### `forgePanel/changeRequestChecks`  (package: `dsh-experimental-client-ui-forge`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/properties/baseSha/type` | (none) | string |
| `/result/jsonSchema/properties/branchUpdates/items/enum[0]` | (none) | merge |
| `/result/jsonSchema/properties/branchUpdates/items/enum[1]` | (none) | rebase |
| `/result/jsonSchema/properties/branchUpdates/items/type` | (none) | string |
| `/result/jsonSchema/properties/branchUpdates/type` | (none) | array |
| `/result/jsonSchema/properties/canRequestReviews/type` | (none) | boolean |
| `/result/jsonSchema/properties/canRerunChecks/type` | (none) | boolean |
| `/result/jsonSchema/properties/checks/items/properties/rerun/oneOf[0]/type` | (none) | null |
| `/result/jsonSchema/properties/checks/items/properties/rerun/oneOf[1]/type` | (none) | string |
| `/result/jsonSchema/properties/checks/items/properties/state/enum[0]` | cancelled | pending |
| `/result/jsonSchema/properties/checks/items/properties/state/enum[1]` | pending | neutral |
| `/result/jsonSchema/properties/checks/items/properties/state/enum[2]` | neutral | success |
| `/result/jsonSchema/properties/checks/items/properties/state/enum[3]` | success | cancelled |
| `/result/jsonSchema/properties/checks/items/required[6]` | (none) | rerun |
| `/result/jsonSchema/properties/closingIssues/items/additionalProperties` | (none) | true |
| `/result/jsonSchema/properties/closingIssues/items/properties/number/type` | (none) | number |
| `/result/jsonSchema/properties/closingIssues/items/properties/repository/additionalProperties` | (none) | true |
| `/result/jsonSchema/properties/closingIssues/items/properties/repository/properties/forge/type` | (none) | string |
| `/result/jsonSchema/properties/closingIssues/items/properties/repository/properties/host/type` | (none) | string |
| `/result/jsonSchema/properties/closingIssues/items/properties/repository/properties/name/type` | (none) | string |
| `/result/jsonSchema/properties/closingIssues/items/properties/repository/properties/owner/type` | (none) | string |
| `/result/jsonSchema/properties/closingIssues/items/properties/repository/required[0]` | (none) | forge |
| `/result/jsonSchema/properties/closingIssues/items/properties/repository/required[1]` | (none) | host |
| `/result/jsonSchema/properties/closingIssues/items/properties/repository/required[2]` | (none) | owner |
| `/result/jsonSchema/properties/closingIssues/items/properties/repository/required[3]` | (none) | name |
| `/result/jsonSchema/properties/closingIssues/items/properties/repository/type` | (none) | object |
| `/result/jsonSchema/properties/closingIssues/items/properties/state/enum[0]` | (none) | open |
| `/result/jsonSchema/properties/closingIssues/items/properties/state/enum[1]` | (none) | closed |
| `/result/jsonSchema/properties/closingIssues/items/properties/state/type` | (none) | string |
| `/result/jsonSchema/properties/closingIssues/items/properties/title/type` | (none) | string |
| `/result/jsonSchema/properties/closingIssues/items/properties/url/type` | (none) | string |
| `/result/jsonSchema/properties/closingIssues/items/required[0]` | (none) | repository |
| `/result/jsonSchema/properties/closingIssues/items/required[1]` | (none) | number |
| `/result/jsonSchema/properties/closingIssues/items/required[2]` | (none) | title |
| `/result/jsonSchema/properties/closingIssues/items/required[3]` | (none) | state |
| `/result/jsonSchema/properties/closingIssues/items/required[4]` | (none) | url |
| `/result/jsonSchema/properties/closingIssues/items/type` | (none) | object |
| `/result/jsonSchema/properties/closingIssues/type` | (none) | array |
| `/result/jsonSchema/properties/closingIssuesTruncated/type` | (none) | boolean |
| `/result/jsonSchema/properties/reviewers/items/additionalProperties` | (none) | true |
| `/result/jsonSchema/properties/reviewers/items/properties/avatarUrl/oneOf[0]/type` | (none) | null |
| `/result/jsonSchema/properties/reviewers/items/properties/avatarUrl/oneOf[1]/type` | (none) | string |
| `/result/jsonSchema/properties/reviewers/items/properties/codeOwner/type` | (none) | boolean |
| `/result/jsonSchema/properties/reviewers/items/properties/kind/enum[0]` | (none) | user |
| `/result/jsonSchema/properties/reviewers/items/properties/kind/enum[1]` | (none) | bot |
| `/result/jsonSchema/properties/reviewers/items/properties/kind/enum[2]` | (none) | team |
| `/result/jsonSchema/properties/reviewers/items/properties/kind/type` | (none) | string |
| `/result/jsonSchema/properties/reviewers/items/properties/login/type` | (none) | string |
| `/result/jsonSchema/properties/reviewers/items/properties/requested/type` | (none) | boolean |
| `/result/jsonSchema/properties/reviewers/items/properties/reviewedSha/oneOf[0]/type` | (none) | null |
| `/result/jsonSchema/properties/reviewers/items/properties/reviewedSha/oneOf[1]/type` | (none) | string |
| `/result/jsonSchema/properties/reviewers/items/properties/verdict/oneOf[0]/type` | (none) | null |
| `/result/jsonSchema/properties/reviewers/items/properties/verdict/oneOf[1]/enum[0]` | (none) | dismissed |
| `/result/jsonSchema/properties/reviewers/items/properties/verdict/oneOf[1]/enum[1]` | (none) | approved |
| `/result/jsonSchema/properties/reviewers/items/properties/verdict/oneOf[1]/enum[2]` | (none) | changes-requested |
| `/result/jsonSchema/properties/reviewers/items/properties/verdict/oneOf[1]/enum[3]` | (none) | commented |
| `/result/jsonSchema/properties/reviewers/items/properties/verdict/oneOf[1]/type` | (none) | string |
| `/result/jsonSchema/properties/reviewers/items/required[0]` | (none) | kind |
| `/result/jsonSchema/properties/reviewers/items/required[1]` | (none) | login |
| `/result/jsonSchema/properties/reviewers/items/required[2]` | (none) | avatarUrl |
| `/result/jsonSchema/properties/reviewers/items/required[3]` | (none) | requested |
| `/result/jsonSchema/properties/reviewers/items/required[4]` | (none) | codeOwner |
| `/result/jsonSchema/properties/reviewers/items/required[5]` | (none) | verdict |
| `/result/jsonSchema/properties/reviewers/items/required[6]` | (none) | reviewedSha |
| `/result/jsonSchema/properties/reviewers/items/type` | (none) | object |
| `/result/jsonSchema/properties/reviewers/type` | (none) | array |
| `/result/jsonSchema/properties/reviewersTruncated/type` | (none) | boolean |
| `/result/jsonSchema/properties/threadsTruncated/type` | (none) | boolean |
| `/result/jsonSchema/required[14]` | reviewDecision | branchUpdates |
| `/result/jsonSchema/required[15]` | checks | reviewDecision |
| `/result/jsonSchema/required[16]` | checksRollup | reviewers |
| `/result/jsonSchema/required[17]` | checksTruncated | reviewersTruncated |
| `/result/jsonSchema/required[18]` | unresolvedThreads | canRequestReviews |
| `/result/jsonSchema/required[19]` | updatedAt | checks |
| `/result/jsonSchema/required[20]` | closedAt | checksRollup |
| `/result/jsonSchema/required[21]` | (none) | checksTruncated |
| `/result/jsonSchema/required[22]` | (none) | canRerunChecks |
| `/result/jsonSchema/required[23]` | (none) | unresolvedThreads |
| `/result/jsonSchema/required[24]` | (none) | threadsTruncated |
| `/result/jsonSchema/required[25]` | (none) | closingIssues |
| `/result/jsonSchema/required[26]` | (none) | closingIssuesTruncated |
| `/result/jsonSchema/required[27]` | (none) | updatedAt |
| `/result/jsonSchema/required[28]` | (none) | closedAt |

### `forgePanel/changeRequestDiff`  (package: `dsh-experimental-client-ui-forge`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/parameters[2]/jsonSchema/oneOf[0]/type` | (none) | null |
| `/parameters[2]/jsonSchema/oneOf[1]/additionalProperties` | (none) | true |
| `/parameters[2]/jsonSchema/oneOf[1]/properties/from/type` | (none) | string |
| `/parameters[2]/jsonSchema/oneOf[1]/properties/to/type` | (none) | string |
| `/parameters[2]/jsonSchema/oneOf[1]/required[0]` | (none) | from |
| `/parameters[2]/jsonSchema/oneOf[1]/required[1]` | (none) | to |
| `/parameters[2]/jsonSchema/oneOf[1]/type` | (none) | object |
| `/parameters[2]/name` | (none) | commits |
| `/parameters[2]/source` | (none) | json |
| `/parameters[2]/typeSymbol` | (none) | @deepseek-ai/dsh-experimental-client-ui-forge#forgePanel/changeRequestDiff:commits |
| `/result/raw` | {
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-experimental-client-ui-forge/types#ForgeChangeRequestDiff',
        create: _deepseek_ai_dsh_experimental_client_ui_forge_forgePanel_changeRequestDiff_result$schema,
        description: 'the files, the head commit, each file\'s web page, and the commits the checkout has beyond the head.',
        jsonSchemaError: 'typert tool schema: | {
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-experimental-client-ui-forge/types#ForgeChangeRequestDiff',
        create: _deepseek_ai_dsh_experimental_client_ui_forge_forgePanel_changeRequestDiff_result$schema,
        description: 'the files, the commit they end at, each file\'s web page, and the commits the checkout has beyond the head.',
        jsonSchemaError: 'typert tool  |

### `forgePanel/localDiff`  (package: `dsh-experimental-client-ui-forge`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/parameters[1]/jsonSchema/oneOf[0]/properties/base/oneOf[0]/type` | (none) | null |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/base/oneOf[1]/type` | (none) | string |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[0]/type` | (none) | null |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[1]/additionalProperties` | (none) | true |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[1]/properties/from/type` | (none) | string |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[1]/properties/to/type` | (none) | string |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[1]/required[0]` | (none) | from |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[1]/required[1]` | (none) | to |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[1]/type` | (none) | object |
| `/parameters[1]/jsonSchema/oneOf[0]/required[1]` | (none) | base |
| `/parameters[1]/jsonSchema/oneOf[0]/required[2]` | (none) | commits |
| `/result/jsonSchema/properties/files/items/properties/digest/type` | (none) | string |
| `/result/jsonSchema/properties/files/items/properties/layer/enum[0]` | (none) | interface |
| `/result/jsonSchema/properties/files/items/properties/layer/enum[1]` | (none) | implementation |
| `/result/jsonSchema/properties/files/items/properties/layer/enum[2]` | (none) | test |
| `/result/jsonSchema/properties/files/items/properties/layer/type` | (none) | string |
| `/result/jsonSchema/properties/files/items/required[10]` | (none) | digest |
| `/result/jsonSchema/properties/files/items/required[9]` | (none) | layer |

### `forgePanel/localText`  (package: `dsh-experimental-client-ui-forge`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/parameters[1]/jsonSchema/oneOf[0]/properties/base/oneOf[0]/type` | (none) | null |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/base/oneOf[1]/type` | (none) | string |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[0]/type` | (none) | null |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[1]/additionalProperties` | (none) | true |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[1]/properties/from/type` | (none) | string |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[1]/properties/to/type` | (none) | string |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[1]/required[0]` | (none) | from |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[1]/required[1]` | (none) | to |
| `/parameters[1]/jsonSchema/oneOf[0]/properties/commits/oneOf[1]/type` | (none) | object |
| `/parameters[1]/jsonSchema/oneOf[0]/required[1]` | (none) | base |
| `/parameters[1]/jsonSchema/oneOf[0]/required[2]` | (none) | commits |

### `forgePanel/submitReview`  (package: `dsh-experimental-client-ui-forge`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/oneOf[1]/properties/code/enum[2]` | unsupported | stale |
| `/result/jsonSchema/oneOf[1]/properties/code/enum[3]` | stale | unsupported |

### `job/follow`  (package: `dsh-api-job-controller`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/oneOf[0]/properties/job/properties/status/enum[0]` | failed | running |
| `/result/jsonSchema/oneOf[0]/properties/job/properties/status/enum[1]` | running | stopping |
| `/result/jsonSchema/oneOf[0]/properties/job/properties/status/enum[2]` | stopping | completed |
| `/result/jsonSchema/oneOf[0]/properties/job/properties/status/enum[3]` | completed | killed |
| `/result/jsonSchema/oneOf[0]/properties/job/properties/status/enum[4]` | killed | failed |
| `/result/jsonSchema/oneOf[2]/properties/job/properties/status/enum[0]` | failed | running |
| `/result/jsonSchema/oneOf[2]/properties/job/properties/status/enum[1]` | running | stopping |
| `/result/jsonSchema/oneOf[2]/properties/job/properties/status/enum[2]` | stopping | completed |
| `/result/jsonSchema/oneOf[2]/properties/job/properties/status/enum[3]` | completed | killed |
| `/result/jsonSchema/oneOf[2]/properties/job/properties/status/enum[4]` | killed | failed |

### `job/list`  (package: `dsh-api-job-controller`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/properties/jobs/items/properties/status/enum[0]` | failed | running |
| `/result/jsonSchema/properties/jobs/items/properties/status/enum[1]` | running | stopping |
| `/result/jsonSchema/properties/jobs/items/properties/status/enum[2]` | stopping | completed |
| `/result/jsonSchema/properties/jobs/items/properties/status/enum[3]` | completed | killed |
| `/result/jsonSchema/properties/jobs/items/properties/status/enum[4]` | killed | failed |

### `liveshareShares/capabilities`  (package: `dsh-experimental-liveshare`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/additionalProperties` | true | (none) |
| `/result/jsonSchema/properties/capabilities/items/additionalProperties` | true | (none) |
| `/result/jsonSchema/properties/capabilities/items/properties/plugin/type` | string | (none) |
| `/result/jsonSchema/properties/capabilities/items/properties/readable/items/type` | string | (none) |
| `/result/jsonSchema/properties/capabilities/items/properties/readable/type` | array | (none) |
| `/result/jsonSchema/properties/capabilities/items/properties/writable/items/type` | string | (none) |
| `/result/jsonSchema/properties/capabilities/items/properties/writable/type` | array | (none) |
| `/result/jsonSchema/properties/capabilities/items/required[0]` | plugin | (none) |
| `/result/jsonSchema/properties/capabilities/items/required[1]` | readable | (none) |
| `/result/jsonSchema/properties/capabilities/items/required[2]` | writable | (none) |
| `/result/jsonSchema/properties/capabilities/items/type` | object | (none) |
| `/result/jsonSchema/properties/capabilities/type` | array | (none) |
| `/result/jsonSchema/required[0]` | capabilities | (none) |
| `/result/jsonSchema/type` | object | (none) |
| `/result/raw` | null | {
        mode: 'strict',
        typeSymbol: '@deepseek-ai/dsh-experimental-liveshare/types#ShareCapabilityOffers',
        create: _deepseek_ai_dsh_experimental_liveshare_liveshareShares_capabilities_result$schema,
        description: 'The live offer list.',
        jsonSchemaError: 'typert tool schema: selected dictionaries require string keys and unconstrained JSON values',
      } |

### `liveshareShares/create`  (package: `dsh-experimental-liveshare`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/properties/sessionIds/items/type` | (none) | string |
| `/result/jsonSchema/properties/sessionIds/type` | (none) | array |
| `/result/jsonSchema/required[3]` | sessionMode | sessionIds |
| `/result/jsonSchema/required[4]` | access | sessionMode |
| `/result/jsonSchema/required[5]` | (none) | access |

### `liveshareShares/list`  (package: `dsh-experimental-liveshare`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/properties/shares/items/properties/sessionIds/items/type` | (none) | string |
| `/result/jsonSchema/properties/shares/items/properties/sessionIds/type` | (none) | array |
| `/result/jsonSchema/properties/shares/items/properties/viewerCapabilities/items/additionalProperties` | true | (none) |
| `/result/jsonSchema/properties/shares/items/properties/viewerCapabilities/items/properties/plugin/type` | string | (none) |
| `/result/jsonSchema/properties/shares/items/properties/viewerCapabilities/items/properties/readable/items/type` | string | (none) |
| `/result/jsonSchema/properties/shares/items/properties/viewerCapabilities/items/properties/readable/type` | array | (none) |
| `/result/jsonSchema/properties/shares/items/properties/viewerCapabilities/items/properties/writable/items/type` | string | (none) |
| `/result/jsonSchema/properties/shares/items/properties/viewerCapabilities/items/properties/writable/type` | array | (none) |
| `/result/jsonSchema/properties/shares/items/properties/viewerCapabilities/items/required[0]` | plugin | (none) |
| `/result/jsonSchema/properties/shares/items/properties/viewerCapabilities/items/required[1]` | readable | (none) |
| `/result/jsonSchema/properties/shares/items/properties/viewerCapabilities/items/required[2]` | writable | (none) |
| `/result/jsonSchema/properties/shares/items/properties/viewerCapabilities/items/type` | object | (none) |
| `/result/jsonSchema/properties/shares/items/properties/viewerCapabilities/type` | array | (none) |
| `/result/jsonSchema/properties/shares/items/properties/viewerPluginAccess/enum[0]` | (none) | none |
| `/result/jsonSchema/properties/shares/items/properties/viewerPluginAccess/enum[1]` | (none) | all |
| `/result/jsonSchema/properties/shares/items/properties/viewerPluginAccess/enum[2]` | (none) | custom |
| `/result/jsonSchema/properties/shares/items/properties/viewerPluginAccess/type` | (none) | string |
| `/result/jsonSchema/properties/shares/items/properties/viewerPlugins/items/additionalProperties` | (none) | true |
| `/result/jsonSchema/properties/shares/items/properties/viewerPlugins/items/properties/endpoints/items/type` | (none) | string |
| `/result/jsonSchema/properties/shares/items/properties/viewerPlugins/items/properties/endpoints/type` | (none) | array |
| `/result/jsonSchema/properties/shares/items/properties/viewerPlugins/items/properties/mode/enum[0]` | (none) | none |
| `/result/jsonSchema/properties/shares/items/properties/viewerPlugins/items/properties/mode/enum[1]` | (none) | selected |
| `/result/jsonSchema/properties/shares/items/properties/viewerPlugins/items/properties/mode/enum[2]` | (none) | all |
| `/result/jsonSchema/properties/shares/items/properties/viewerPlugins/items/properties/mode/type` | (none) | string |
| `/result/jsonSchema/properties/shares/items/properties/viewerPlugins/items/properties/plugin/type` | (none) | string |
| `/result/jsonSchema/properties/shares/items/properties/viewerPlugins/items/required[0]` | (none) | plugin |
| `/result/jsonSchema/properties/shares/items/properties/viewerPlugins/items/required[1]` | (none) | mode |
| `/result/jsonSchema/properties/shares/items/properties/viewerPlugins/items/type` | (none) | object |
| `/result/jsonSchema/properties/shares/items/properties/viewerPlugins/type` | (none) | array |
| `/result/jsonSchema/properties/shares/items/required[2]` | sessionTitle | sessionIds |
| `/result/jsonSchema/properties/shares/items/required[3]` | link | sessionTitle |
| `/result/jsonSchema/properties/shares/items/required[4]` | createdAt | link |
| `/result/jsonSchema/properties/shares/items/required[5]` | allowsFork | createdAt |
| `/result/jsonSchema/properties/shares/items/required[6]` | sessionMode | allowsFork |
| `/result/jsonSchema/properties/shares/items/required[7]` | viewerCapabilities | sessionMode |
| `/result/jsonSchema/properties/shares/items/required[8]` | online | viewerPlugins |
| `/result/jsonSchema/properties/shares/items/required[9]` | (none) | online |

### `liveshareShares/setCapabilities`  (package: `dsh-experimental-liveshare`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/parameters[0]/jsonSchema/properties/capabilities/items/additionalProperties` | true | (none) |
| `/parameters[0]/jsonSchema/properties/capabilities/items/properties/plugin/type` | string | (none) |
| `/parameters[0]/jsonSchema/properties/capabilities/items/properties/readable/items/type` | string | (none) |
| `/parameters[0]/jsonSchema/properties/capabilities/items/properties/readable/type` | array | (none) |
| `/parameters[0]/jsonSchema/properties/capabilities/items/properties/writable/items/type` | string | (none) |
| `/parameters[0]/jsonSchema/properties/capabilities/items/properties/writable/type` | array | (none) |
| `/parameters[0]/jsonSchema/properties/capabilities/items/required[0]` | plugin | (none) |
| `/parameters[0]/jsonSchema/properties/capabilities/items/required[1]` | readable | (none) |
| `/parameters[0]/jsonSchema/properties/capabilities/items/required[2]` | writable | (none) |
| `/parameters[0]/jsonSchema/properties/capabilities/items/type` | object | (none) |
| `/parameters[0]/jsonSchema/properties/capabilities/type` | array | (none) |
| `/parameters[0]/jsonSchema/properties/viewerPluginAccess/enum[0]` | (none) | none |
| `/parameters[0]/jsonSchema/properties/viewerPluginAccess/enum[1]` | (none) | all |
| `/parameters[0]/jsonSchema/properties/viewerPluginAccess/enum[2]` | (none) | custom |
| `/parameters[0]/jsonSchema/properties/viewerPluginAccess/type` | (none) | string |
| `/parameters[0]/jsonSchema/properties/viewerPlugins/items/additionalProperties` | (none) | true |
| `/parameters[0]/jsonSchema/properties/viewerPlugins/items/properties/endpoints/items/type` | (none) | string |
| `/parameters[0]/jsonSchema/properties/viewerPlugins/items/properties/endpoints/type` | (none) | array |
| `/parameters[0]/jsonSchema/properties/viewerPlugins/items/properties/mode/enum[0]` | (none) | none |
| `/parameters[0]/jsonSchema/properties/viewerPlugins/items/properties/mode/enum[1]` | (none) | selected |
| `/parameters[0]/jsonSchema/properties/viewerPlugins/items/properties/mode/enum[2]` | (none) | all |
| `/parameters[0]/jsonSchema/properties/viewerPlugins/items/properties/mode/type` | (none) | string |
| `/parameters[0]/jsonSchema/properties/viewerPlugins/items/properties/plugin/type` | (none) | string |
| `/parameters[0]/jsonSchema/properties/viewerPlugins/items/required[0]` | (none) | plugin |
| `/parameters[0]/jsonSchema/properties/viewerPlugins/items/required[1]` | (none) | mode |
| `/parameters[0]/jsonSchema/properties/viewerPlugins/items/type` | (none) | object |
| `/parameters[0]/jsonSchema/properties/viewerPlugins/type` | (none) | array |
| `/result/jsonSchema/properties/viewerCapabilities/items/additionalProperties` | true | (none) |
| `/result/jsonSchema/properties/viewerCapabilities/items/properties/plugin/type` | string | (none) |
| `/result/jsonSchema/properties/viewerCapabilities/items/properties/readable/items/type` | string | (none) |
| `/result/jsonSchema/properties/viewerCapabilities/items/properties/readable/type` | array | (none) |
| `/result/jsonSchema/properties/viewerCapabilities/items/properties/writable/items/type` | string | (none) |
| `/result/jsonSchema/properties/viewerCapabilities/items/properties/writable/type` | array | (none) |
| `/result/jsonSchema/properties/viewerCapabilities/items/required[0]` | plugin | (none) |
| `/result/jsonSchema/properties/viewerCapabilities/items/required[1]` | readable | (none) |
| `/result/jsonSchema/properties/viewerCapabilities/items/required[2]` | writable | (none) |
| `/result/jsonSchema/properties/viewerCapabilities/items/type` | object | (none) |
| `/result/jsonSchema/properties/viewerCapabilities/type` | array | (none) |
| `/result/jsonSchema/properties/viewerPluginAccess/enum[0]` | (none) | none |
| `/result/jsonSchema/properties/viewerPluginAccess/enum[1]` | (none) | all |
| `/result/jsonSchema/properties/viewerPluginAccess/enum[2]` | (none) | custom |
| `/result/jsonSchema/properties/viewerPluginAccess/type` | (none) | string |
| `/result/jsonSchema/properties/viewerPlugins/items/additionalProperties` | (none) | true |
| `/result/jsonSchema/properties/viewerPlugins/items/properties/endpoints/items/type` | (none) | string |
| `/result/jsonSchema/properties/viewerPlugins/items/properties/endpoints/type` | (none) | array |
| `/result/jsonSchema/properties/viewerPlugins/items/properties/mode/enum[0]` | (none) | none |
| `/result/jsonSchema/properties/viewerPlugins/items/properties/mode/enum[1]` | (none) | selected |
| `/result/jsonSchema/properties/viewerPlugins/items/properties/mode/enum[2]` | (none) | all |
| `/result/jsonSchema/properties/viewerPlugins/items/properties/mode/type` | (none) | string |
| `/result/jsonSchema/properties/viewerPlugins/items/properties/plugin/type` | (none) | string |
| `/result/jsonSchema/properties/viewerPlugins/items/required[0]` | (none) | plugin |
| `/result/jsonSchema/properties/viewerPlugins/items/required[1]` | (none) | mode |
| `/result/jsonSchema/properties/viewerPlugins/items/type` | (none) | object |
| `/result/jsonSchema/properties/viewerPlugins/type` | (none) | array |
| `/result/jsonSchema/required[1]` | viewerCapabilities | viewerPluginAccess |
| `/result/jsonSchema/required[2]` | (none) | viewerPlugins |

### `pluginManager/cancelInstall`  (package: `dsh-plugin-manager`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/properties/status/enum[0]` | cancelled | not-running |
| `/result/jsonSchema/properties/status/enum[1]` | not-running | cancelled |

### `pluginManager/inspect`  (package: `dsh-plugin-manager`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/oneOf[0]/properties/kind/enum[1]` | path | git |
| `/result/jsonSchema/oneOf[0]/properties/kind/enum[2]` | git | path |
| `/result/jsonSchema/oneOf[1]/properties/problem/enum[0]` | network | unknown |
| `/result/jsonSchema/oneOf[1]/properties/problem/enum[1]` | unknown | network |

### `pluginManager/installBundle`  (package: `dsh-plugin-manager`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/parameters[1]/jsonSchema/properties/expected/additionalProperties` | (none) | true |
| `/parameters[1]/jsonSchema/properties/expected/properties/name/type` | (none) | string |
| `/parameters[1]/jsonSchema/properties/expected/properties/version/type` | (none) | string |
| `/parameters[1]/jsonSchema/properties/expected/required[0]` | (none) | name |
| `/parameters[1]/jsonSchema/properties/expected/required[1]` | (none) | version |
| `/parameters[1]/jsonSchema/properties/expected/type` | (none) | object |

### `pluginManager/updateBundle`  (package: `dsh-plugin-manager`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/parameters[2]/jsonSchema/properties/spec/type` | (none) | string |

### `speech/catalog`  (package: `dsh-experimental-api-speech-to-text`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[0]/properties/phase/enum[0]` | cancelled | ready |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[0]/properties/phase/enum[1]` | ready | cancelled |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[0]/properties/steps/items/properties/status/enum[0]` | cancelled | running |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[0]/properties/steps/items/properties/status/enum[2]` | running | pending |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[0]/properties/steps/items/properties/status/enum[3]` | pending | complete |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[0]/properties/steps/items/properties/status/enum[4]` | complete | cancelled |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[1]/properties/steps/items/properties/status/enum[0]` | cancelled | running |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[1]/properties/steps/items/properties/status/enum[2]` | running | pending |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[1]/properties/steps/items/properties/status/enum[3]` | pending | complete |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[1]/properties/steps/items/properties/status/enum[4]` | complete | cancelled |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[2]/properties/steps/items/properties/status/enum[0]` | cancelled | running |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[2]/properties/steps/items/properties/status/enum[2]` | running | pending |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[2]/properties/steps/items/properties/status/enum[3]` | pending | complete |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[2]/properties/steps/items/properties/status/enum[4]` | complete | cancelled |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/download/properties/reason/enum[0]` | network | unknown |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/download/properties/reason/enum[1]` | storage | timeout |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/download/properties/reason/enum[2]` | unknown | network |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/download/properties/reason/enum[3]` | timeout | storage |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/steps/items/properties/status/enum[0]` | cancelled | running |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/steps/items/properties/status/enum[2]` | running | pending |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/steps/items/properties/status/enum[3]` | pending | complete |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/steps/items/properties/status/enum[4]` | complete | cancelled |

### `speech/follow`  (package: `dsh-experimental-api-speech-to-text`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[0]/properties/phase/enum[0]` | cancelled | ready |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[0]/properties/phase/enum[1]` | ready | cancelled |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[0]/properties/steps/items/properties/status/enum[0]` | cancelled | running |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[0]/properties/steps/items/properties/status/enum[2]` | running | pending |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[0]/properties/steps/items/properties/status/enum[3]` | pending | complete |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[0]/properties/steps/items/properties/status/enum[4]` | complete | cancelled |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[1]/properties/steps/items/properties/status/enum[0]` | cancelled | running |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[1]/properties/steps/items/properties/status/enum[2]` | running | pending |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[1]/properties/steps/items/properties/status/enum[3]` | pending | complete |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[1]/properties/steps/items/properties/status/enum[4]` | complete | cancelled |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[2]/properties/steps/items/properties/status/enum[0]` | cancelled | running |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[2]/properties/steps/items/properties/status/enum[2]` | running | pending |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[2]/properties/steps/items/properties/status/enum[3]` | pending | complete |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[2]/properties/steps/items/properties/status/enum[4]` | complete | cancelled |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/download/properties/reason/enum[0]` | network | unknown |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/download/properties/reason/enum[1]` | storage | timeout |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/download/properties/reason/enum[2]` | unknown | network |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/download/properties/reason/enum[3]` | timeout | storage |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/steps/items/properties/status/enum[0]` | cancelled | running |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/steps/items/properties/status/enum[2]` | running | pending |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/steps/items/properties/status/enum[3]` | pending | complete |
| `/result/jsonSchema/properties/providers/items/properties/preparation/oneOf[3]/properties/steps/items/properties/status/enum[4]` | complete | cancelled |

### `subagents/prompt`  (package: `dsh-subagent`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/parameters[0]/jsonSchema/properties/mode/const` | continuable | (none) |
| `/parameters[0]/jsonSchema/properties/mode/type` | string | (none) |
| `/parameters[0]/jsonSchema/properties/parentSessionId/type` | string | (none) |
| `/parameters[0]/jsonSchema/required[1]` | parentSessionId | childSessionId |
| `/parameters[0]/jsonSchema/required[2]` | childSessionId | delivery |
| `/parameters[0]/jsonSchema/required[3]` | mode | content |
| `/parameters[0]/jsonSchema/required[4]` | delivery | (none) |
| `/parameters[0]/jsonSchema/required[5]` | content | (none) |

### `terminal/attach`  (package: `dsh-api-terminal-controller`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/oneOf[0]/properties/info/properties/control/enum[0]` | (none) | user |
| `/result/jsonSchema/oneOf[0]/properties/info/properties/control/enum[1]` | (none) | agent |
| `/result/jsonSchema/oneOf[0]/properties/info/properties/control/type` | (none) | string |
| `/result/jsonSchema/oneOf[0]/properties/info/properties/origin/enum[0]` | (none) | user |
| `/result/jsonSchema/oneOf[0]/properties/info/properties/origin/enum[1]` | (none) | agent |
| `/result/jsonSchema/oneOf[0]/properties/info/properties/origin/type` | (none) | string |
| `/result/jsonSchema/oneOf[0]/properties/info/properties/sandboxMode/enum[0]` | (none) | read-only |
| `/result/jsonSchema/oneOf[0]/properties/info/properties/sandboxMode/enum[1]` | (none) | workspace-write |
| `/result/jsonSchema/oneOf[0]/properties/info/properties/sandboxMode/enum[2]` | (none) | danger-full-access |
| `/result/jsonSchema/oneOf[0]/properties/info/properties/sandboxMode/type` | (none) | string |
| `/result/jsonSchema/oneOf[0]/properties/info/properties/state/enum[0]` | failed | running |
| `/result/jsonSchema/oneOf[0]/properties/info/properties/state/enum[1]` | running | failed |
| `/result/jsonSchema/oneOf[0]/properties/info/required[8]` | (none) | origin |
| `/result/jsonSchema/oneOf[0]/properties/info/required[9]` | (none) | control |
| `/result/jsonSchema/oneOf[2]/properties/info/properties/control/enum[0]` | (none) | user |
| `/result/jsonSchema/oneOf[2]/properties/info/properties/control/enum[1]` | (none) | agent |
| `/result/jsonSchema/oneOf[2]/properties/info/properties/control/type` | (none) | string |
| `/result/jsonSchema/oneOf[2]/properties/info/properties/origin/enum[0]` | (none) | user |
| `/result/jsonSchema/oneOf[2]/properties/info/properties/origin/enum[1]` | (none) | agent |
| `/result/jsonSchema/oneOf[2]/properties/info/properties/origin/type` | (none) | string |
| `/result/jsonSchema/oneOf[2]/properties/info/properties/sandboxMode/enum[0]` | (none) | read-only |
| `/result/jsonSchema/oneOf[2]/properties/info/properties/sandboxMode/enum[1]` | (none) | workspace-write |
| `/result/jsonSchema/oneOf[2]/properties/info/properties/sandboxMode/enum[2]` | (none) | danger-full-access |
| `/result/jsonSchema/oneOf[2]/properties/info/properties/sandboxMode/type` | (none) | string |
| `/result/jsonSchema/oneOf[2]/properties/info/properties/state/enum[0]` | failed | running |
| `/result/jsonSchema/oneOf[2]/properties/info/properties/state/enum[1]` | running | failed |
| `/result/jsonSchema/oneOf[2]/properties/info/required[8]` | (none) | origin |
| `/result/jsonSchema/oneOf[2]/properties/info/required[9]` | (none) | control |

### `terminal/create`  (package: `dsh-api-terminal-controller`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/properties/control/enum[0]` | (none) | user |
| `/result/jsonSchema/properties/control/enum[1]` | (none) | agent |
| `/result/jsonSchema/properties/control/type` | (none) | string |
| `/result/jsonSchema/properties/origin/enum[0]` | (none) | user |
| `/result/jsonSchema/properties/origin/enum[1]` | (none) | agent |
| `/result/jsonSchema/properties/origin/type` | (none) | string |
| `/result/jsonSchema/properties/sandboxMode/enum[0]` | (none) | read-only |
| `/result/jsonSchema/properties/sandboxMode/enum[1]` | (none) | workspace-write |
| `/result/jsonSchema/properties/sandboxMode/enum[2]` | (none) | danger-full-access |
| `/result/jsonSchema/properties/sandboxMode/type` | (none) | string |
| `/result/jsonSchema/properties/state/enum[0]` | failed | running |
| `/result/jsonSchema/properties/state/enum[1]` | running | failed |
| `/result/jsonSchema/required[8]` | (none) | origin |
| `/result/jsonSchema/required[9]` | (none) | control |

### `terminal/list`  (package: `dsh-api-terminal-controller`)

| JSON 路径 | rc.1 | rc.2 |
|---|---|---|
| `/result/jsonSchema/items/properties/control/enum[0]` | (none) | user |
| `/result/jsonSchema/items/properties/control/enum[1]` | (none) | agent |
| `/result/jsonSchema/items/properties/control/type` | (none) | string |
| `/result/jsonSchema/items/properties/origin/enum[0]` | (none) | user |
| `/result/jsonSchema/items/properties/origin/enum[1]` | (none) | agent |
| `/result/jsonSchema/items/properties/origin/type` | (none) | string |
| `/result/jsonSchema/items/properties/sandboxMode/enum[0]` | (none) | read-only |
| `/result/jsonSchema/items/properties/sandboxMode/enum[1]` | (none) | workspace-write |
| `/result/jsonSchema/items/properties/sandboxMode/enum[2]` | (none) | danger-full-access |
| `/result/jsonSchema/items/properties/sandboxMode/type` | (none) | string |
| `/result/jsonSchema/items/properties/state/enum[0]` | failed | running |
| `/result/jsonSchema/items/properties/state/enum[1]` | running | failed |
| `/result/jsonSchema/items/required[8]` | (none) | origin |
| `/result/jsonSchema/items/required[9]` | (none) | control |

