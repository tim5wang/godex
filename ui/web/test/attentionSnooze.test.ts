import { describe, expect, it } from "vitest";
import dayjs from "dayjs";
import { SNOOZE_CHOICES, snoozeUntilAt } from "../src/features/workdesktop/AttentionInbox";

/**
 * The snooze windows are the one place the UI invents a timestamp, so they are pinned here
 * rather than only exercised through the browser.
 *
 * Two properties matter and both are easy to break by "simplifying":
 *  - every choice must be comfortably in the FUTURE, because the server rejects an until_at
 *    that is not after its own now, and a browser clock a little behind the server would turn
 *    a marginal window into a 400;
 *  - "tomorrow" must land on a stable wall-clock time (09:00), not "24h from now": a reminder
 *    that fires at 23:40 is not what anyone means by tomorrow.
 */
describe("snoozeUntilAt", () => {
  const now = dayjs("2026-11-09T14:30:00.000Z");

  it("offers the three explicit windows", () => {
    expect([...SNOOZE_CHOICES]).toEqual(["tomorrow", "threeDays", "nextWeek"]);
  });

  it("lands tomorrow on 09:00 local rather than 24 hours out", () => {
    const at = dayjs(snoozeUntilAt("tomorrow", now));
    expect(at.isAfter(now)).toBe(true);
    expect(at.hour()).toBe(9);
    expect(at.minute()).toBe(0);
    expect(at.second()).toBe(0);
    expect(at.millisecond()).toBe(0);
    // The TOMORROW DATE at 09:00 (asserted on the calendar day, not on a 24h delta: diff(day)
    // truncates, so a snooze taken in the evening is legitimately less than one full day out).
    expect(at.format("YYYY-MM-DD")).toBe(now.add(1, "day").format("YYYY-MM-DD"));
  });

  it("keeps the medium and long windows comfortably in the future", () => {
    for (const choice of ["threeDays", "nextWeek"] as const) {
      const at = dayjs(snoozeUntilAt(choice, now));
      expect(at.isAfter(now)).toBe(true);
    }
    expect(dayjs(snoozeUntilAt("threeDays", now)).diff(now, "day")).toBe(3);
    expect(dayjs(snoozeUntilAt("nextWeek", now)).diff(now, "day")).toBe(7);
  });

  it("stays in the future even when the browser clock is behind the server", () => {
    // Worst realistic case: the item is snoozed at 23:59 local, i.e. minutes before the
    // "tomorrow 09:00" it asks for. It must still be a future instant.
    const lateNight = dayjs("2026-11-09T23:59:00.000Z");
    const at = dayjs(snoozeUntilAt("tomorrow", lateNight));
    expect(at.isAfter(lateNight)).toBe(true);
    expect(at.hour()).toBe(9);
  });
});
