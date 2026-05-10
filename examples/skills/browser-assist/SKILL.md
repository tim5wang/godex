---
name: browser-assist
description: Assisted browser automation for login, captcha, account selection, and other user-gated web flows.
when_to_use:
  - when a browser task reaches login, captcha, two-factor auth, payment confirmation, file picker, or account consent
  - when headless browser automation cannot see or complete an interaction but a user can finish it in a visible browser
recommended_bundles:
  - browser
sections:
  - core
---

## Core

Prefer normal `browser` actions first: `open`, `snapshot`, `find`, `click`, `type`, `fill_form`, `wait_network_idle`, and `capture_page`.

When the page requires user assistance, call `browser` with action `handoff`. Provide either the current `page_id` or a `url`, and include a short `reason` such as "login required" or "captcha required". This opens or switches to a visible browser and returns a `waiting_for_user` status.

Tell the user exactly what to complete in the browser. After they say it is done, call `browser` with action `resume` and the returned `page_id`. Use the returned snapshot to continue the task.

Do not ask the user for passwords, verification codes, or private account details in chat. Let them type sensitive information directly into the visible browser.
