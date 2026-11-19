# Pending generated-operation exceptions

The vendored OpenAPI source contains 284 operations. The normalized input to
`okapi-gen-typed` contains the other 280 operations; the four below are
excluded only because the typed generator cannot represent their wire or
response behavior safely. `internal/codegen` checks each exact operation ID,
method, path, and relevant schema before excluding it. `script/check-generated`
fails if the source inventory changes unexpectedly.

| Operation ID | Method | Path | Reason |
| --- | --- | --- | --- |
| `handle_get_self_v3_self_get` | GET | `/v3/self` | Four-way response union is not generated as a typed result. |
| `handle_upload_attachment_v3_organizations__org_id__attachments_post` | POST | `/v3/organizations/{org_id}/attachments` | Multipart file body is not generated. |
| `upload_enterprise_blueprint_file_endpoint_v3_enterprise_snapshot_setup_blueprints__blueprint_id__files_post` | POST | `/v3/enterprise/snapshot-setup/blueprints/{blueprint_id}/files` | Multipart file body is not generated. |
| `upload_org_blueprint_file_v3_organizations__org_id__snapshot_setup_blueprints__blueprint_id__files_post` | POST | `/v3/organizations/{org_id}/snapshot-setup/blueprints/{blueprint_id}/files` | Multipart file body is not generated. |

These operations are explicit pending work for the later SDK session. This
bootstrap does not add handwritten replacements; the generated API surface
must not be treated as complete until all four are implemented there.
