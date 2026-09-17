// @vitest-environment jsdom
import { act, useLayoutEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { IMTVerificationAcknowledgment } from "./IMTVerificationAcknowledgment";
import { useIMTAcknowledgment } from "../hooks/useIMTAcknowledgment";
import { imtVerificationPrompt, rootHex } from "../utils/imtVerification";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let container: HTMLDivElement;
let root: Root;
let capture: () => () => void;
const roundId = "ab".repeat(32);

function Harness({ context }: { context: string }) {
  const acknowledgment = useIMTAcknowledgment(context);
  useLayoutEffect(() => { capture = acknowledgment.capture; }, [acknowledgment.capture]);
  return <>
    <IMTVerificationAcknowledgment rounds={[{ roundId, snapshotHeight: "3459350", circuitRoot: "cd".repeat(32) }]}
      chainId="test-chain" network="test" checked={acknowledgment.checked} onChange={acknowledgment.setChecked} />
    <button disabled={!acknowledgment.checked}>Continue</button>
  </>;
}

beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});
const checkbox = () => container.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
const button = () => Array.from(container.querySelectorAll("button")).find((b) => b.textContent === "Continue")!;

it("requires only a checkbox and opening the instructions does not acknowledge", async () => {
  await act(async () => root.render(<Harness context="round-a/wallet-a" />));
  expect(button().disabled).toBe(true);
  expect(capture).toThrow("Acknowledge");
  expect(container.querySelector('input[type="file"]')).toBeNull();
  const link = container.querySelector("a")!;
  expect(link.href).toContain("verify-round-imt-ai.md");
  await act(async () => link.click());
  expect(checkbox().checked).toBe(false);
  await act(async () => checkbox().click());
  expect(button().disabled).toBe(false);
  expect(capture()).not.toThrow();
});

describe.each(["round", "wallet", "chain", "snapshot", "endorser", "batch"])('%s context changes', (change) => {
  it("clear acknowledgment and invalidate an in-flight signing guard", async () => {
    await act(async () => root.render(<Harness context="original" />));
    await act(async () => checkbox().click());
    const guard = capture();
    await act(async () => root.render(<Harness context={change} />));
    expect(button().disabled).toBe(true);
    expect(guard).toThrow("Acknowledge");
    await act(async () => checkbox().click());
    expect(guard).toThrow("Acknowledge");
    expect(capture()).not.toThrow();
  });
});

it("invalidates on clear/recheck, Keplr account events, and unmount", async () => {
  await act(async () => root.render(<Harness context="same" />));
  await act(async () => checkbox().click());
  const originalGuard = capture();
  await act(async () => checkbox().click());
  await act(async () => checkbox().click());
  expect(originalGuard).toThrow("Acknowledge");
  const guard = capture();
  await act(async () => {
    window.dispatchEvent(new Event("keplr_keystorechange"));
    expect(guard).toThrow("Acknowledge");
  });
  expect(button().disabled).toBe(true);
  await act(async () => checkbox().click());
  const unmountGuard = capture();
  await act(async () => root.render(null));
  expect(unmountGuard).toThrow("Acknowledge");
});

it("copies an exact-round prompt without private endpoints and displays root bytes unchanged", () => {
  const prompt = imtVerificationPrompt(roundId, "test-chain", "test");
  expect(prompt).toContain(`round ${roundId}`);
  expect(prompt).toContain("Do not substitute a newer round");
  expect(prompt).toContain("test-chain");
  expect(rootHex(btoa(String.fromCharCode(1) + "\0".repeat(31)))).toBe("01" + "00".repeat(31));
  expect(rootHex("bad")).toBe("");
});
