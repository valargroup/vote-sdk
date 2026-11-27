// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ProxyRoundOption } from "./ProxyRoundOption";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let container: HTMLDivElement;
let root: Root;
beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); });

it("stays hidden until the chain reports proxy delegation", async () => {
  await act(async () => root.render(<ProxyRoundOption supported={false} checked={false} onChange={vi.fn()} />));
  expect(container.innerHTML).toBe("");
});

it("sets the proxy flag and states the approval floor", async () => {
  const onChange = vi.fn();
  await act(async () => root.render(<ProxyRoundOption supported checked={false} onChange={onChange} />));
  expect(container.textContent).toContain("Proxy delegation round");
  expect(container.textContent).toContain("at least 2 coordinator approvals");
  await act(async () => container.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
  expect(onChange).toHaveBeenCalledWith(true);
});
