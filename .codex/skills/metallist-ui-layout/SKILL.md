---
name: metallist-ui-layout
description: Rework an existing Metallist screen to the shared list, create, and detail page architecture using the project's shadcn components and responsive layout. Use for registry, request, catalog, and operational UI refactors.
---

# Metallist screen layout

Read `AGENTS.md` and `DESIGN.md` first. Inspect the current screen, routes, API permissions, and shared components before editing. Preserve the screen's business validation and audit behavior.

## Page architecture

- Open a section on its list or overview. Put creation on a separate route. Give each editable record a direct route that survives refresh and browser history navigation.
- Put the primary create action above the list. Use the shared shadcn `Table` for desktop data lists; let wide tables fill the main area up to the sidebar. Use compact stacked rows below 768 px without page-level horizontal scroll.
- Put an icon-only ellipsis trigger in each row. Use the shared shadcn `DropdownMenu` portal so opening actions does not change row height. Show only actions allowed by the role and object state; keep the server as the final authority.
- Use the shared `Breadcrumb` in the application header for section and parent navigation on desktop and mobile. Do not add a duplicate «К списку» button in page content.
- Use `Button` variants from `DESIGN.md`: `primary` for the main save or create action, `secondary` for a neighboring neutral action, `destructive-secondary` for deletion. Use `Button asChild` for a download link. Keep an action group's controls at the same height; align a record title input with that group on desktop.
- Keep form surfaces separate from data tables. Avoid card padding or white space above table headers; use the darker shared table header and text colors.
- Keep `Combobox` input backgrounds unified with their `InputGroup`; do not set a global input background on its inner control.

## Verify

Check direct links, refresh, browser back and forward, role-dependent actions, empty and error states, and unsaved changes. Inspect desktop and 320–430 px widths. Open a row menu on both widths and confirm that it overlays without increasing row height. Open a combobox and check its border and background. Run the UI build and any relevant isolated tests.
