---
name: pdf-form-filler
description: >-
  Fill AcroForm fields in a fillable PDF from a set of key/value answers, then
  flatten the result. Use when the user hands you a blank PDF form plus the data
  to put in it and wants a filled copy back.
license: Apache-2.0
allowed-tools:
  - fill_pdf_form
metadata:
  aisotropy:
    # Operator-visible import manifest (ADR-0022). Everything here becomes an
    # explicit aisotropy primitive — nothing is auto-injected at runtime.
    persona-product-id: pdf-form-filler
    required-scope: write:pdf
    scripts:
      - path: scripts/fill_form.py
        interpreter: python3
        map-to: typed-tool          # "typed-tool" | "mcp"
        tool-name: fill_pdf_form
        args:
          - name: source_path
            type: string
            required: true
            description: Path to the blank fillable PDF.
          - name: answers_json
            type: string
            required: true
            description: JSON object of AcroForm field name -> value.
          - name: flatten
            type: bool
            required: false
            description: Flatten the form so fields are no longer editable.
    references:
      - path: references/acroform-fields.md
        doc-id: pdf-form-filler/acroform-fields
---

# PDF form filler

You fill fillable (AcroForm) PDF forms. Follow this procedure exactly.

1. Confirm you have (a) the path to a blank fillable PDF and (b) the answers to
   place in it. If either is missing, ask for it before doing anything else.
2. If you do not already know the form's field names, read
   `references/acroform-fields.md` for how AcroForm field names are structured
   and how to enumerate them.
3. Call the `fill_pdf_form` tool with `source_path`, `answers_json` (a JSON
   object mapping each AcroForm field name to its value), and `flatten: true`
   unless the user explicitly wants the form left editable.
4. Report the path of the filled PDF the tool returns. Never invent field
   values — if an answer is missing for a required field, stop and ask.

Do not attempt to edit the raw PDF bytes yourself; always go through the tool.
