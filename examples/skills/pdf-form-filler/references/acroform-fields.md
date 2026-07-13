# AcroForm field reference

Non-behavioral background content for the `pdf-form-filler` skill. Under
aisotropy this file is **ingested knowledge** (ADR-0018, `KindDocument`), not
prompt behavior — it is retrieved by RAG when relevant, never auto-injected.

## Field naming

AcroForm fields are addressed by their fully-qualified name. Nested fields use a
dotted path from the field tree root, e.g. `applicant.address.postcode`.

| Field type | AcroForm `/FT` | Value shape |
|---|---|---|
| Text | `/Tx` | string |
| Checkbox | `/Btn` | on-state name (e.g. `/Yes`) or `/Off` |
| Radio group | `/Btn` | the selected kid's on-state name |
| Dropdown / list | `/Ch` | one of the export values |

## Enumerating fields

Most PDF libraries expose the field tree: iterate the AcroForm `/Fields` array,
following each field's `/Kids` to reach terminal (widget) fields. The terminal
field's fully-qualified name is what the `answers_json` object keys on.

## Gotchas

- Checkbox on-state names are per-field; do not assume `/Yes`.
- A flattened form is no longer fillable — only flatten the final copy.
- Some forms use the same field name on multiple pages; setting the value once
  updates every occurrence.
