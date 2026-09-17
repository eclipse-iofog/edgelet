# EdgeletAPI v1 RBAC Resources and Verbs

This document defines canonical EdgeletAPI v1 endpoint-to-RBAC mapping.
All mappings are deny-by-default: if a route is not explicitly mapped, authorization must fail.

## Canonical HTTP Method to Verb Mapping

- `GET` -> `get`
- `POST` -> `create`
- `PATCH`/`PUT` -> `update`
- `DELETE` -> `delete`

Evaluator alias policy is canonical + alias tolerant:
- `patch`/`put` are accepted as `update`.

## Endpoint to Resource Mapping

- Endpoint: `GET /v1/system/status`
  - Resource: `system/status`
  - Verb: `get`
  - Scope examples:
    - `system:localadmin:*` (local admin token)
    - serviceaccount rule including `system/status` + `get`

- Endpoint: `GET /v1/system/info`
  - Resource: `system/info`
  - Verb: `get`
  - Scope examples:
    - local admin
    - serviceaccount with `system/info:get`

- Endpoint: `GET /v1/system/version`
  - Resource: `system/version`
  - Verb: `get`

- Endpoint: `POST /v1/system/provision`
  - Resource: `system/provision`
  - Verb: `create`

- Endpoint: `DELETE /v1/system/provision`
  - Resource: `system/provision`
  - Verb: `delete`

- Endpoint: `POST /v1/system/reload`
  - Resource: `system/reload`
  - Verb: `create`

- Endpoint: `POST /v1/system/prune`
  - Resource: `system/prune`
  - Verb: `create`
  - Compatibility note: top-level `edgelet prune` is removed; use `edgelet system prune` or `edgelet image prune`.

- Endpoint: `GET /v1/images`
  - Resource: `images`
  - Verb: `get`

- Endpoint: `POST /v1/images:pull`
  - Resource: `images/pull`
  - Verb: `create`

- Endpoint: `GET /v1/images:pull/{operationId}`
  - Resource: `images/pull/status`
  - Verb: `get`

- Endpoint: `POST /v1/images:load`
  - Resource: `images/load`
  - Verb: `create`

- Endpoint: `POST /v1/images:prune`
  - Resource: `images/prune`
  - Verb: `create`

- Endpoint: `POST /v1/images:remove`
  - Resource: `images/remove`
  - Verb: `create`

- Endpoint: `GET /v1/models`
  - Resource: `models`
  - Verb: `get`

- Endpoint: `GET /v1/models/{name}`
  - Resource: `models`
  - Verb: `get`
  - ResourceName: `{name}`

- Endpoint: `DELETE /v1/models/{name}`
  - Resource: `models`
  - Verb: `delete`
  - ResourceName: `{name}`

- Endpoint: `POST /v1/models:pull`
  - Resource: `models/pull`
  - Verb: `create`

- Endpoint: `GET /v1/models:pull/{operationId}`
  - Resource: `models/pull/status`
  - Verb: `get`

- Endpoint: `POST /v1/models:prune`
  - Resource: `models/prune`
  - Verb: `create`

- Endpoint: `GET /v1/system/gps`
  - Resource: `system/gps`
  - Verb: `get`

- Endpoint: `POST /v1/system/gps`
  - Resource: `system/gps`
  - Verb: `create`

- Endpoint: `GET /v1/system/config`
  - Resource: `system/config`
  - Verb: `get`

- Endpoint: `PATCH /v1/system/config`
  - Resource: `system/config`
  - Verb: `update`

- Endpoint: `POST /v1/system/controller/cert`
  - Resource: `system/controller/cert`
  - Verb: `update`

- Endpoint: `POST /v1/system/config/switch`
  - Resource: `system/config/switch`
  - Verb: `update`

- Endpoint: `GET /v1/microservices/config`
  - Resource: `microservices/config/self`
  - Verb: `get`
  - Identity binding: server resolves caller UUID from JWT claim `iofog.org.microservice.uuid`

- Endpoint: `GET /v1/microservices/control` (WSS upgrade)
  - Resource: `microservices/control/self`
  - Verb: `get`
  - Identity binding: server resolves caller UUID from JWT claim `iofog.org.microservice.uuid`

- Endpoint: `GET /v1/ms`
  - Resource: `microservices`
  - Verb: `get`

- Endpoint: `GET /v1/ms/{id}`
  - Resource: `microservices`
  - Verb: `get`
  - ResourceName: `{id}`

- Endpoint: `DELETE /v1/ms/{id}`
  - Resource: `microservices`
  - Verb: `delete`
  - ResourceName: `{id}`

- Endpoint: `POST /v1/ms/{id}/start`
  - Resource: `microservices`
  - Verb: `create`
  - ResourceName: `{id}`

- Endpoint: `POST /v1/ms/{id}/stop`
  - Resource: `microservices`
  - Verb: `create`
  - ResourceName: `{id}`

- Endpoint: `POST /v1/ms/{id}/restart`
  - Resource: `microservices`
  - Verb: `create`
  - ResourceName: `{id}`

- Endpoint: `POST /v1/ms/{id}/kill`
  - Resource: `microservices`
  - Verb: `create`
  - ResourceName: `{id}`

- Endpoint: `GET /v1/ms/{id}/logs`
  - Resource: `microservices`
  - Verb: `get`
  - ResourceName: `{id}`

- Endpoint: `GET /v1/ms/{id}/logs:stream` (WSS upgrade)
  - Resource: `microservices`
  - Verb: `get`
  - ResourceName: `{id}`

- Endpoint: `POST /v1/ms/{id}/exec/sessions`
  - Resource: `microservices`
  - Verb: `create`
  - ResourceName: `{id}`

- Endpoint: `GET /v1/ms/{id}/exec/sessions/{sessionId}`
  - Resource: `microservices`
  - Verb: `get`
  - ResourceName: `{id}`

- Endpoint: `DELETE /v1/ms/{id}/exec/sessions/{sessionId}`
  - Resource: `microservices`
  - Verb: `delete`
  - ResourceName: `{id}`

- Endpoint: `GET /v1/ms/{id}/exec/sessions/{sessionId}:attach` (WSS upgrade)
  - Resource: `microservices`
  - Verb: `get`
  - ResourceName: `{id}`

- Endpoint: `POST /v1/deploy/microservices:apply`
  - Resource: `deploy/microservices`
  - Verb: `create`

- Endpoint: `GET /v1/deploy/microservices:apply/{operationId}`
  - Resource: `deploy/microservices/apply/status`
  - Verb: `get`

- Endpoint: `POST /v1/deploy/microservices:validate`
  - Resource: `deploy/microservices`
  - Verb: `create`

- Endpoint: `GET /v1/deploy/microservices`
  - Resource: `deploy/microservices`
  - Verb: `get`

- Endpoint: `GET /v1/deploy/microservices/{id}`
  - Resource: `deploy/microservices`
  - Verb: `get`

- Endpoint: `DELETE /v1/deploy/microservices/{id}`
  - Resource: `deploy/microservices`
  - Verb: `delete`

- Endpoint: `POST /v1/deploy/registries:apply`
  - Resource: `deploy/registries`
  - Verb: `create`

- Endpoint: `POST /v1/deploy/registries:validate`
  - Resource: `deploy/registries`
  - Verb: `create`

- Endpoint: `GET /v1/deploy/registries`
  - Resource: `deploy/registries`
  - Verb: `get`

- Endpoint: `GET /v1/deploy/registries/{id}`
  - Resource: `deploy/registries`
  - Verb: `get`

- Endpoint: `DELETE /v1/deploy/registries/{id}`
  - Resource: `deploy/registries`
  - Verb: `delete`

- Endpoint: `POST /v1/deploy/models:apply`
  - Resource: `deploy/models`
  - Verb: `create`

- Endpoint: `POST /v1/deploy/models:validate`
  - Resource: `deploy/models`
  - Verb: `create`

- Endpoint: `POST /v1/deploy/runtimeclasses:apply`
  - Resource: `deploy/runtimeclasses`
  - Verb: `create`

- Endpoint: `GET /v1/deploy/runtimeclasses:apply/{operationId}`
  - Resource: `deploy/runtimeclasses/apply/status`
  - Verb: `get`

- Endpoint: `POST /v1/deploy/runtimeclasses:validate`
  - Resource: `deploy/runtimeclasses`
  - Verb: `create`

- Endpoint: `GET /v1/deploy/runtimeclasses`
  - Resource: `deploy/runtimeclasses`
  - Verb: `get`

- Endpoint: `GET /v1/deploy/runtimeclasses/{name}`
  - Resource: `deploy/runtimeclasses`
  - Verb: `get`

- Endpoint: `DELETE /v1/deploy/runtimeclasses/{name}`
  - Resource: `deploy/runtimeclasses`
  - Verb: `delete`

- Endpoint: `GET /v1/deploy/runtimeclasses:delete/{operationId}`
  - Resource: `deploy/runtimeclasses/delete/status`
  - Verb: `get`

- Endpoint: `GET /v1/auth/whoami`
  - Resource: `auth/whoami`
  - Verb: `get`

- Endpoint: `GET /v1/auth/tokens`
  - Resource: `auth/tokens`
  - Verb: `get`

- Endpoint: `POST /v1/auth/tokens/revoke`
  - Resource: `auth/tokens`
  - Verb: `create`

