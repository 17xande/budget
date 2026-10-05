import { assertEquals, assertMatch } from "@std/assert";
import { formatLocal } from "./localtime.ts";

Deno.test("formats in the requested timezone", () => {
  // 22:30 UTC is already the next day in Johannesburg (UTC+2): the whole reason
  // a UTC timestamp must not be shown unlabelled.
  const text = formatLocal("2026-10-05T22:30:00Z", "Africa/Johannesburg");
  assertMatch(text ?? "", /6/);
  assertMatch(text ?? "", /00:30/);
});

Deno.test("refuses garbage rather than printing Invalid Date", () => {
  assertEquals(formatLocal("banana"), null);
});
