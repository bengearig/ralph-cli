# Tracker: Billing revamp

Goal: replace the invoice generator and move customers onto monthly plans.

## Milestone 1: Invoice engine

- [x] Extract the invoice template into its own module
- [x] Add currency rounding rules (blocked earlier on finance sign-off, now resolved)
- [ ] Render invoices as PDF
  - Description: produce a PDF per invoice using the shared template.
  - Acceptance: a sample invoice renders with correct totals and page breaks.
- [ ] Email invoices to customers

## Milestone 2: Plans

- [~] Model monthly plans in the database
- [!] Migrate legacy annual customers — blocked: waiting on the data-retention policy
- [-] Support weekly plans (dropped, out of scope)
