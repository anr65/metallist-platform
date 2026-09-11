---
name: metallist-registry-import
description: Build or review ingestion of Metallist XLSX registries, payment files, Telegram-derived rows, and legacy tabular data. Use for upload, parsing, mapping, validation, deduplication, preview, approval, reprocessing, and import audit trails.
---

# Metallist registry import

Read the applicable `docs/integrations/`, `docs/as-is/source-data-formats.md`, and anonymized fixtures before changing an importer. A source format observed once is not a stable contract unless documentation says so.

## Preserve evidence

- Store the original file or message unchanged in protected object storage.
- Record source, uploader or sender, receipt time, original filename, content type, size, checksum, and storage reference.
- Create an `import_batch` with an explicit parser version and lifecycle state.
- Preserve source sheet name, row number, raw values, normalized values, validation results, and links to created domain objects.
- Never overwrite the original batch when reprocessing. Create a new attempt or version linked to it.

## Deterministic import flow

Use a staged flow equivalent to:

`received -> parsed -> validated -> previewed -> approved -> applied`

Names may follow the documented state model, but parsing and validation must not silently publish financial operations. Apply an approved batch transactionally or provide an explicit, auditable partial-application policy.

## Parsing and validation rules

- Match columns through an explicit, versioned header mapping. Reject ambiguous matches.
- Preserve the source value before trimming or normalization.
- Normalize dates, time zones, decimal separators, thousand separators, identifiers, and blank values deliberately.
- Validate amounts, required fields, merchant, registry identity, card reference, dates, and allowed row types.
- Never identify a card solely by its last four digits when multiple cards can collide.
- Report every rejected or skipped row with a machine-readable code and a user-facing explanation.
- Treat formulas and cached formula values cautiously; document which representation is accepted.
- Do not ingest macros or execute embedded content.
- Never import PINs. Redact unnecessary personal and card data from fixtures, logs, and error messages.

## Duplicate protection

Detect at least:

- exact file duplicates by checksum;
- repeated batches under a source-specific external ID;
- repeated rows under a documented business key;
- duplicate posting attempts through downstream idempotency keys.

Do not treat similar-looking rows as duplicates without preserving them for operator review.

## Verification

Maintain anonymized fixtures for every supported format and important failure mode. Test stable reprocessing, duplicate detection, header variations, blank/formula cells, invalid amounts, colliding last-four values, partial failures, and the boundary between approval and ledger posting. Reconcile imported totals against source totals without modifying the raw evidence.
